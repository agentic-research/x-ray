package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
)

// Sender abstracts the WebSocket write path.
type Sender interface {
	SendJSON(msg map[string]any) error
}

type cdpResponse struct {
	Result json.RawMessage
	Error  string
}

// EventMsg carries a CDP event to a per-tab subscriber.
type EventMsg struct {
	Method string
	Params json.RawMessage
}

// Proxy mediates CDP commands through the Chrome extension WebSocket.
type Proxy struct {
	sender  Sender
	nextID  atomic.Int64
	pending sync.Map // int64 -> chan cdpResponse
	attachM sync.Map // int -> chan error (tabID -> attach result)
	detachM sync.Map // int -> chan struct{}

	eventSubs sync.Map // int (tabID) -> chan EventMsg
}

// New creates a CDP proxy with the given sender.
func New(sender Sender) *Proxy {
	return &Proxy{sender: sender}
}

// SetSender updates the sender (e.g., on WS reconnect).
func (p *Proxy) SetSender(s Sender) {
	p.sender = s
}

// Attach asks the extension to chrome.debugger.attach(tabId, '1.3').
func (p *Proxy) Attach(ctx context.Context, tabID int) error {
	ch := make(chan error, 1)
	p.attachM.Store(tabID, ch)
	defer p.attachM.Delete(tabID)

	if err := p.sender.SendJSON(map[string]any{
		"type":   "CDP_ATTACH",
		"tab_id": tabID,
	}); err != nil {
		return fmt.Errorf("cdp attach: send: %w", err)
	}

	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Detach asks the extension to chrome.debugger.detach(tabId).
func (p *Proxy) Detach(ctx context.Context, tabID int) error {
	ch := make(chan struct{}, 1)
	p.detachM.Store(tabID, ch)
	defer p.detachM.Delete(tabID)

	if err := p.sender.SendJSON(map[string]any{
		"type":   "CDP_DETACH",
		"tab_id": tabID,
	}); err != nil {
		return fmt.Errorf("cdp detach: send: %w", err)
	}

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Send sends a CDP command and waits for the result.
func (p *Proxy) Send(ctx context.Context, tabID int, method string, params any) (json.RawMessage, error) {
	id := p.nextID.Add(1)
	ch := make(chan cdpResponse, 1)
	p.pending.Store(id, ch)
	defer p.pending.Delete(id)

	msg := map[string]any{
		"type":       "CDP_SEND",
		"cdp_id":     id,
		"tab_id":     tabID,
		"cdp_method": method,
	}
	if params != nil {
		msg["cdp_params"] = params
	}

	if err := p.sender.SendJSON(msg); err != nil {
		return nil, fmt.Errorf("cdp %s: send: %w", method, err)
	}

	select {
	case resp := <-ch:
		if resp.Error != "" {
			return nil, fmt.Errorf("cdp %s: %s", method, resp.Error)
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("cdp %s: %w", method, ctx.Err())
	}
}

// --- Inbound handlers (called by the WS message router) ---

// HandleAttached is called when CDP_ATTACHED arrives.
func (p *Proxy) HandleAttached(tabID int) {
	if v, ok := p.attachM.Load(tabID); ok {
		v.(chan error) <- nil
	}
}

// HandleAttachFailed is called when CDP_ATTACH_FAILED arrives.
func (p *Proxy) HandleAttachFailed(tabID int, errMsg string) {
	if v, ok := p.attachM.Load(tabID); ok {
		v.(chan error) <- fmt.Errorf("%s", errMsg)
	}
}

// HandleDetached is called when CDP_DETACHED arrives.
func (p *Proxy) HandleDetached(tabID int) {
	if v, ok := p.detachM.Load(tabID); ok {
		v.(chan struct{}) <- struct{}{}
	}
}

// HandleResult is called when CDP_RESULT arrives.
func (p *Proxy) HandleResult(id int64, result json.RawMessage) {
	if v, ok := p.pending.Load(id); ok {
		v.(chan cdpResponse) <- cdpResponse{Result: result}
	}
}

// HandleError is called when CDP_ERROR arrives.
func (p *Proxy) HandleError(id int64, errMsg string) {
	if v, ok := p.pending.Load(id); ok {
		v.(chan cdpResponse) <- cdpResponse{Error: errMsg}
	}
}

// SubscribeEvents returns a channel that receives CDP events for the given tabID.
// The caller must call UnsubscribeEvents when done to avoid leaking the channel.
func (p *Proxy) SubscribeEvents(tabID int) <-chan EventMsg {
	ch := make(chan EventMsg, 8)
	p.eventSubs.Store(tabID, ch)
	return ch
}

// UnsubscribeEvents removes the event subscription for the given tabID and closes its channel.
func (p *Proxy) UnsubscribeEvents(tabID int) {
	if v, ok := p.eventSubs.LoadAndDelete(tabID); ok {
		close(v.(chan EventMsg))
	}
}

// HandleEvent is called when CDP_EVENT arrives.
func (p *Proxy) HandleEvent(tabID int, method string, params json.RawMessage) {
	// Route to per-tab subscriber (used by CaptureLayerTree).
	if v, ok := p.eventSubs.Load(tabID); ok {
		select {
		case v.(chan EventMsg) <- EventMsg{Method: method, Params: params}:
		default:
			// Channel full — subscriber too slow; drop event.
		}
	}
}
