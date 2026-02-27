// Package iterm provides an iTerm2 bridge that projects terminal sessions
// into a mache graph for the Navigator to browse and act upon.
//
// It uses the iTerm2 native API (protobuf over WebSocket) via the public
// github.com/tmc/it2/proto package. The internal/client package of it2
// is not importable, so this is a thin reimplementation of the WebSocket
// request/response loop (~100 lines).
package iterm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
	pb "github.com/tmc/it2/proto"
	protobuf "google.golang.org/protobuf/proto"
)

// Client is a thin iTerm2 API client using the public proto package.
// It handles WebSocket connection, protobuf serialization, and
// request/response correlation.
type Client struct {
	conn    *websocket.Conn
	writeMu sync.Mutex                       // protects WriteMessage calls
	msgs    chan *pb.ServerOriginatedMessage // notifications (id==0)
	done    chan struct{}
	counter atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan *pb.ServerOriginatedMessage
}

// Dial connects to iTerm2's API. It tries the Unix socket first
// (~/Library/Application Support/iTerm2/private/socket), then falls
// back to ws://localhost:1912.
func Dial(ctx context.Context) (*Client, error) {
	headers := buildHeaders()
	c := &Client{
		msgs:    make(chan *pb.ServerOriginatedMessage, 64),
		done:    make(chan struct{}),
		pending: make(map[int64]chan *pb.ServerOriginatedMessage),
	}

	// Try Unix socket first.
	socketPath := filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "iTerm2", "private", "socket")
	if _, err := os.Stat(socketPath); err == nil {
		dialer := websocket.Dialer{
			NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
			Subprotocols: []string{"api.iterm2.com"},
		}
		conn, _, err := dialer.DialContext(ctx, "ws://localhost/", headers)
		if err == nil {
			c.conn = conn
			go c.readLoop()
			return c, nil
		}
	}

	// TCP fallback.
	dialer := websocket.Dialer{
		Subprotocols: []string{"api.iterm2.com"},
	}
	conn, _, err := dialer.DialContext(ctx, "ws://localhost:1912", headers)
	if err != nil {
		return nil, fmt.Errorf("iterm2: connect failed (ensure Python API is enabled in iTerm2 preferences): %w", err)
	}
	c.conn = conn
	go c.readLoop()
	return c, nil
}

func buildHeaders() http.Header {
	h := make(http.Header)
	h.Set("Origin", "ws://localhost/")
	h.Set("x-iterm2-library-version", "go 1.0")
	h.Set("x-iterm2-disable-auth-ui", "true")
	if v := os.Getenv("ITERM2_COOKIE"); v != "" {
		h.Set("x-iterm2-cookie", v)
	}
	if v := os.Getenv("ITERM2_KEY"); v != "" {
		h.Set("x-iterm2-key", v)
	}
	return h
}

// Close shuts down the connection.
func (c *Client) Close() error {
	close(c.done)
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Notifications returns the channel for unsolicited server messages
// (screen updates, layout changes, etc.).
func (c *Client) Notifications() <-chan *pb.ServerOriginatedMessage {
	return c.msgs
}

// readLoop reads WebSocket frames and dispatches them.
func (c *Client) readLoop() {
	defer close(c.msgs)
	for {
		select {
		case <-c.done:
			return
		default:
		}

		msgType, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if msgType != websocket.BinaryMessage {
			continue
		}

		var msg pb.ServerOriginatedMessage
		if err := protobuf.Unmarshal(data, &msg); err != nil {
			continue
		}

		c.mu.Lock()
		if msg.Id != nil && *msg.Id != 0 {
			if ch, ok := c.pending[*msg.Id]; ok {
				ch <- &msg
				delete(c.pending, *msg.Id)
			}
		} else {
			select {
			case c.msgs <- &msg:
			default: // drop if full
			}
		}
		c.mu.Unlock()
	}
}

// send marshals a ClientOriginatedMessage and waits for the correlated response.
func (c *Client) send(ctx context.Context, msg *pb.ClientOriginatedMessage) (*pb.ServerOriginatedMessage, error) {
	if c.conn == nil {
		return nil, fmt.Errorf("iterm2: not connected")
	}

	id := c.counter.Add(1)
	msg.Id = &id

	ch := make(chan *pb.ServerOriginatedMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	data, err := protobuf.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("iterm2: marshal: %w", err)
	}

	c.writeMu.Lock()
	err = c.conn.WriteMessage(websocket.BinaryMessage, data)
	c.writeMu.Unlock()

	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("iterm2: write: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// --- High-level operations ---

// SessionInfo holds metadata about a single iTerm2 session.
type SessionInfo struct {
	SessionID string
	WindowID  string
	TabID     string
	Title     string
	CWD       string
}

// ListSessions returns all active sessions across all windows/tabs.
func (c *Client) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	resp, err := c.send(ctx, &pb.ClientOriginatedMessage{
		Submessage: &pb.ClientOriginatedMessage_ListSessionsRequest{
			ListSessionsRequest: &pb.ListSessionsRequest{},
		},
	})
	if err != nil {
		return nil, err
	}
	lr := resp.GetListSessionsResponse()
	if lr == nil {
		return nil, fmt.Errorf("iterm2: unexpected response type")
	}

	var out []SessionInfo
	for _, w := range lr.GetWindows() {
		wid := w.GetWindowId()
		for _, t := range w.GetTabs() {
			tid := t.GetTabId()
			collectSessions(t.GetRoot(), wid, tid, &out)
		}
	}
	return out, nil
}

func collectSessions(node *pb.SplitTreeNode, wid, tid string, out *[]SessionInfo) {
	if node == nil {
		return
	}
	for _, link := range node.GetLinks() {
		if s := link.GetSession(); s != nil {
			*out = append(*out, SessionInfo{
				SessionID: s.GetUniqueIdentifier(),
				WindowID:  wid,
				TabID:     tid,
				Title:     s.GetTitle(),
			})
		}
		if n := link.GetNode(); n != nil {
			collectSessions(n, wid, tid, out)
		}
	}
}

// GetBuffer returns the last N lines of a session's terminal buffer as raw text.
func (c *Client) GetBuffer(ctx context.Context, sessionID string, lines int32) (string, error) {
	sid := normalizeSessionID(sessionID)
	lr := &pb.LineRange{}
	if lines > 0 {
		lr.TrailingLines = &lines
	} else {
		t := true
		lr.ScreenContentsOnly = &t
	}
	noStyles := false

	resp, err := c.send(ctx, &pb.ClientOriginatedMessage{
		Submessage: &pb.ClientOriginatedMessage_GetBufferRequest{
			GetBufferRequest: &pb.GetBufferRequest{
				Session:       &sid,
				IncludeStyles: &noStyles,
				LineRange:     lr,
			},
		},
	})
	if err != nil {
		return "", err
	}

	br := resp.GetGetBufferResponse()
	if br == nil {
		return "", fmt.Errorf("iterm2: unexpected response type")
	}

	var sb strings.Builder
	for _, entry := range br.GetContents() {
		sb.WriteString(entry.GetText())
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// SendText sends text to a session as if typed by the user.
func (c *Client) SendText(ctx context.Context, sessionID, text string) error {
	sid := normalizeSessionID(sessionID)
	_, err := c.send(ctx, &pb.ClientOriginatedMessage{
		Submessage: &pb.ClientOriginatedMessage_SendTextRequest{
			SendTextRequest: &pb.SendTextRequest{
				Session: &sid,
				Text:    &text,
			},
		},
	})
	return err
}

// ActivateSession brings a session's window/tab to the foreground.
func (c *Client) ActivateSession(ctx context.Context, sessionID string) error {
	sid := normalizeSessionID(sessionID)
	orderFront := true
	selectTab := true
	selectSession := true
	raiseAll := false
	ignoreOther := true

	_, err := c.send(ctx, &pb.ClientOriginatedMessage{
		Submessage: &pb.ClientOriginatedMessage_ActivateRequest{
			ActivateRequest: &pb.ActivateRequest{
				Identifier:       &pb.ActivateRequest_SessionId{SessionId: sid},
				OrderWindowFront: &orderFront,
				SelectTab:        &selectTab,
				SelectSession:    &selectSession,
				ActivateApp: &pb.ActivateRequest_App{
					RaiseAllWindows:   &raiseAll,
					IgnoringOtherApps: &ignoreOther,
				},
			},
		},
	})
	return err
}

// SubscribeScreenUpdates subscribes to screen update notifications for a session.
func (c *Client) SubscribeScreenUpdates(ctx context.Context, sessionID string) error {
	subscribe := true
	notifType := pb.NotificationType_NOTIFY_ON_SCREEN_UPDATE
	resp, err := c.send(ctx, &pb.ClientOriginatedMessage{
		Submessage: &pb.ClientOriginatedMessage_NotificationRequest{
			NotificationRequest: &pb.NotificationRequest{
				Subscribe:        &subscribe,
				NotificationType: &notifType,
				Session:          &sessionID,
			},
		},
	})
	if err != nil {
		return err
	}
	if nr := resp.GetNotificationResponse(); nr != nil {
		if nr.GetStatus() != pb.NotificationResponse_OK {
			return fmt.Errorf("iterm2: subscribe failed: %v", nr.GetStatus())
		}
	}
	return nil
}

// SubscribeLayoutChanges subscribes to layout change notifications (new/closed sessions).
func (c *Client) SubscribeLayoutChanges(ctx context.Context) error {
	subscribe := true
	notifType := pb.NotificationType_NOTIFY_ON_LAYOUT_CHANGE
	resp, err := c.send(ctx, &pb.ClientOriginatedMessage{
		Submessage: &pb.ClientOriginatedMessage_NotificationRequest{
			NotificationRequest: &pb.NotificationRequest{
				Subscribe:        &subscribe,
				NotificationType: &notifType,
			},
		},
	})
	if err != nil {
		return err
	}
	if nr := resp.GetNotificationResponse(); nr != nil {
		if nr.GetStatus() != pb.NotificationResponse_OK {
			return fmt.Errorf("iterm2: layout subscribe failed: %v", nr.GetStatus())
		}
	}
	return nil
}

// GetFocusedSession returns the currently focused session ID.
func (c *Client) GetFocusedSession(ctx context.Context) (string, error) {
	resp, err := c.send(ctx, &pb.ClientOriginatedMessage{
		Submessage: &pb.ClientOriginatedMessage_FocusRequest{
			FocusRequest: &pb.FocusRequest{},
		},
	})
	if err != nil {
		return "", err
	}
	fr := resp.GetFocusResponse()
	if fr == nil {
		return "", fmt.Errorf("iterm2: unexpected response type")
	}
	for _, n := range fr.GetNotifications() {
		if sid, ok := n.GetEvent().(*pb.FocusChangedNotification_Session); ok {
			return sid.Session, nil
		}
	}
	return "", nil
}

// CreateTab creates a new tab. If windowID is empty, it creates a new window.
func (c *Client) CreateTab(ctx context.Context, windowID string) (string, error) {
	req := &pb.CreateTabRequest{}
	if windowID != "" {
		req.WindowId = &windowID
	}

	resp, err := c.send(ctx, &pb.ClientOriginatedMessage{
		Submessage: &pb.ClientOriginatedMessage_CreateTabRequest{
			CreateTabRequest: req,
		},
	})
	if err != nil {
		return "", err
	}

	ctr := resp.GetCreateTabResponse()
	if ctr == nil {
		return "", fmt.Errorf("iterm2: unexpected response type")
	}
	if ctr.GetStatus() != pb.CreateTabResponse_OK {
		return "", fmt.Errorf("iterm2: create tab failed: %v", ctr.GetStatus())
	}
	return ctr.GetSessionId(), nil
}

// normalizeSessionID extracts the UUID part from "w0t1p0:UUID" format.
func normalizeSessionID(id string) string {
	if idx := strings.LastIndex(id, ":"); idx != -1 {
		return id[idx+1:]
	}
	return id
}
