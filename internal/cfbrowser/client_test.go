package cfbrowser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agentic-research/x-ray/internal/api"
)

func TestCreateSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["url"] != "http://example.com" {
			t.Errorf("unexpected url: %v", body["url"])
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess-123"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	id, err := c.CreateSession(context.Background(), "http://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "sess-123" {
		t.Errorf("got id %q, want sess-123", id)
	}
}

func TestCreateSessionWithCookies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		cookies, ok := body["cookies"].([]any)
		if !ok || len(cookies) != 1 {
			t.Errorf("expected 1 cookie, got %v", body["cookies"])
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess-456"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	id, err := c.CreateSession(context.Background(), "http://example.com", []api.CookieMsg{
		{Name: "session", Value: "abc123", Domain: "example.com", Path: "/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "sess-456" {
		t.Errorf("got id %q, want sess-456", id)
	}
}

func TestRequestSummary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/sess-1/summary" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"summary": "ID: mache-0 | BLUE | Bounds: [0.1, 0.2, 0.3, 0.4]",
			"url":     "http://example.com/page",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	resp, err := c.RequestSummary(context.Background(), "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.URL != "http://example.com/page" {
		t.Errorf("got url %q", resp.URL)
	}
	if resp.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

func TestCaptureScreenshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"base64": "iVBORw0KGgo="})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	b64, err := c.CaptureScreenshot(context.Background(), "sess-1", 800, 600)
	if err != nil {
		t.Fatal(err)
	}
	if b64 != "iVBORw0KGgo=" {
		t.Errorf("unexpected base64: %s", b64)
	}
}

func TestExecuteAction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["macheID"] != "mache-5" || body["action"] != "click" {
			t.Errorf("unexpected body: %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	err := c.ExecuteAction(context.Background(), "sess-1", "mache-5", "click", "")
	if err != nil {
		t.Fatal(err)
	}
}

func TestScroll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"summary":         "ID: mache-0 | scrolled",
			"at_bottom":       true,
			"scroll_moved":    true,
			"scroll_y":        1200.0,
			"scroll_height":   3000.0,
			"viewport_height": 800.0,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	update, err := c.Scroll(context.Background(), "sess-1", "down")
	if err != nil {
		t.Fatal(err)
	}
	if !update.AtBottom {
		t.Error("expected at_bottom=true")
	}
	if update.ScrollY != 1200 {
		t.Errorf("expected scroll_y=1200, got %f", update.ScrollY)
	}
}

func TestCloseSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/session/sess-1" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if err := c.CloseSession(context.Background(), "sess-1"); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"browser crashed"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.RequestSummary(context.Background(), "sess-1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer my-secret" {
			t.Errorf("expected Bearer my-secret, got %q", auth)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess-1"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "my-secret")
	_, err := c.CreateSession(context.Background(), "http://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHTTPTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the client timeout.
		time.Sleep(2 * time.Second)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess-1"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	// Override to a short timeout for the test.
	c.httpClient.Timeout = 100 * time.Millisecond

	_, err := c.CreateSession(context.Background(), "http://example.com", nil)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestDefaultHTTPTimeout(t *testing.T) {
	c := NewClient("http://unused", "")
	if c.httpClient.Timeout == 0 {
		t.Fatal("expected non-zero default HTTP timeout, got 0 (no timeout)")
	}
	if c.httpClient.Timeout != 30*time.Second {
		t.Errorf("expected 30s timeout, got %v", c.httpClient.Timeout)
	}
}

func TestSessionForTab(t *testing.T) {
	c := NewClient("http://unused", "")
	c.SetSessionForTab(1, "sess-abc")
	if got := c.SessionForTab(1); got != "sess-abc" {
		t.Errorf("got %q, want sess-abc", got)
	}
	if got := c.SessionForTab(99); got != "" {
		t.Errorf("got %q for unknown tab, want empty", got)
	}
	c.RemoveSession(1)
	if got := c.SessionForTab(1); got != "" {
		t.Errorf("got %q after remove, want empty", got)
	}
}
