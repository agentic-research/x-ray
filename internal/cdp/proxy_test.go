package cdp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type mockSender struct {
	mu   sync.Mutex
	msgs []map[string]any
}

func (m *mockSender) SendJSON(msg map[string]any) error {
	m.mu.Lock()
	m.msgs = append(m.msgs, msg)
	m.mu.Unlock()
	return nil
}

func (m *mockSender) lastMsg() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.msgs) == 0 {
		return nil
	}
	return m.msgs[len(m.msgs)-1]
}

func (m *mockSender) allMsgs() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]map[string]any, len(m.msgs))
	copy(cp, m.msgs)
	return cp
}

func TestSendCommand_Success(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	ctx := context.Background()
	resultJSON := json.RawMessage(`{"frameTree":{"frame":{"id":"main"}}}`)

	// Run Send in a goroutine since it blocks waiting for response.
	var got json.RawMessage
	var gotErr error
	done := make(chan struct{})
	go func() {
		got, gotErr = p.Send(ctx, 42, "Page.getFrameTree", nil)
		close(done)
	}()

	// Wait for the message to be sent.
	deadline := time.After(2 * time.Second)
	for {
		if msg := ms.lastMsg(); msg != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for Send to dispatch message")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// Verify the outbound message.
	msg := ms.lastMsg()
	if msg["type"] != "CDP_SEND" {
		t.Errorf("expected type CDP_SEND, got %v", msg["type"])
	}
	if msg["cdp_method"] != "Page.getFrameTree" {
		t.Errorf("expected method Page.getFrameTree, got %v", msg["cdp_method"])
	}
	if msg["tab_id"] != 42 {
		t.Errorf("expected tab_id 42, got %v", msg["tab_id"])
	}

	// Simulate the response arriving from the extension.
	cdpID := int64(msg["cdp_id"].(int64))
	p.HandleResult(cdpID, resultJSON)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return after HandleResult")
	}

	if gotErr != nil {
		t.Fatalf("unexpected error: %v", gotErr)
	}
	if string(got) != string(resultJSON) {
		t.Errorf("result mismatch: got %s, want %s", got, resultJSON)
	}
}

func TestSendCommand_Error(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	ctx := context.Background()

	var gotErr error
	done := make(chan struct{})
	go func() {
		_, gotErr = p.Send(ctx, 10, "DOM.getDocument", nil)
		close(done)
	}()

	// Wait for the message to be sent.
	deadline := time.After(2 * time.Second)
	for {
		if msg := ms.lastMsg(); msg != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for Send to dispatch message")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	msg := ms.lastMsg()
	cdpID := int64(msg["cdp_id"].(int64))
	p.HandleError(cdpID, "Node not found")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return after HandleError")
	}

	if gotErr == nil {
		t.Fatal("expected error, got nil")
	}
	if got := gotErr.Error(); got != "cdp DOM.getDocument: Node not found" {
		t.Errorf("unexpected error message: %s", got)
	}
}

func TestSendCommand_Timeout(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := p.Send(ctx, 1, "Page.navigate", map[string]string{"url": "https://example.com"})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if ctx.Err() == nil {
		t.Fatal("expected context to be done")
	}
}

func TestAttach_Success(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	ctx := context.Background()
	var gotErr error
	done := make(chan struct{})
	go func() {
		gotErr = p.Attach(ctx, 99)
		close(done)
	}()

	// Wait for the attach message.
	deadline := time.After(2 * time.Second)
	for {
		if msg := ms.lastMsg(); msg != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for Attach to send")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	msg := ms.lastMsg()
	if msg["type"] != "CDP_ATTACH" {
		t.Errorf("expected CDP_ATTACH, got %v", msg["type"])
	}
	if msg["tab_id"] != 99 {
		t.Errorf("expected tab_id 99, got %v", msg["tab_id"])
	}

	// Simulate successful attach.
	p.HandleAttached(99)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Attach did not return")
	}

	if gotErr != nil {
		t.Fatalf("unexpected error: %v", gotErr)
	}
}

func TestAttach_Failed(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	ctx := context.Background()
	var gotErr error
	done := make(chan struct{})
	go func() {
		gotErr = p.Attach(ctx, 50)
		close(done)
	}()

	// Wait for the attach message.
	deadline := time.After(2 * time.Second)
	for {
		if msg := ms.lastMsg(); msg != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for Attach to send")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// Simulate attach failure.
	p.HandleAttachFailed(50, "Cannot attach to this target")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Attach did not return")
	}

	if gotErr == nil {
		t.Fatal("expected error, got nil")
	}
	if got := gotErr.Error(); got != "Cannot attach to this target" {
		t.Errorf("unexpected error message: %s", got)
	}
}

func TestDetach_Success(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	ctx := context.Background()
	var gotErr error
	done := make(chan struct{})
	go func() {
		gotErr = p.Detach(ctx, 77)
		close(done)
	}()

	// Wait for the detach message.
	deadline := time.After(2 * time.Second)
	for {
		if msg := ms.lastMsg(); msg != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for Detach to send")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	msg := ms.lastMsg()
	if msg["type"] != "CDP_DETACH" {
		t.Errorf("expected CDP_DETACH, got %v", msg["type"])
	}

	p.HandleDetached(77)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Detach did not return")
	}

	if gotErr != nil {
		t.Fatalf("unexpected error: %v", gotErr)
	}
}

func TestConcurrentSend(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	const n = 20
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make([]json.RawMessage, n)
	errors := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errors[idx] = p.Send(ctx, 1, "Runtime.evaluate", map[string]string{
				"expression": "1+1",
			})
		}(i)
	}

	// Wait for all messages to be sent.
	deadline := time.After(5 * time.Second)
	for {
		msgs := ms.allMsgs()
		if len(msgs) >= n {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out: only %d/%d messages sent", len(ms.allMsgs()), n)
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// Respond to each one.
	msgs := ms.allMsgs()
	for _, msg := range msgs {
		cdpID := int64(msg["cdp_id"].(int64))
		result := json.RawMessage(`{"result":{"value":2}}`)
		p.HandleResult(cdpID, result)
	}

	// Wait for all goroutines to finish.
	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent sends did not all complete")
	}

	for i := 0; i < n; i++ {
		if errors[i] != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, errors[i])
		}
		if string(results[i]) != `{"result":{"value":2}}` {
			t.Errorf("goroutine %d: unexpected result: %s", i, results[i])
		}
	}
}

func TestSubscribeEvents(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	ch := p.SubscribeEvents(42)

	params := json.RawMessage(`{"layers":[]}`)
	p.HandleEvent(42, "LayerTree.layerTreeDidChange", params)

	select {
	case ev := <-ch:
		if ev.Method != "LayerTree.layerTreeDidChange" {
			t.Errorf("expected method LayerTree.layerTreeDidChange, got %s", ev.Method)
		}
		if string(ev.Params) != `{"layers":[]}` {
			t.Errorf("unexpected params: %s", ev.Params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event was not delivered to subscriber")
	}

	p.UnsubscribeEvents(42)
}

func TestHandleEvent_NoSubscriber(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	// Should not panic when no subscriber exists.
	p.HandleEvent(1, "Page.loadEventFired", json.RawMessage(`{}`))
}
