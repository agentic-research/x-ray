package cfbrowser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/agentic-research/x-ray/internal/api"
)

// Compile-time interface check.
var _ api.BrowserBackend = (*Client)(nil)

// Client implements api.BrowserBackend by talking to a Cloudflare Worker
// that wraps Puppeteer + Browser Rendering API.
type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client

	mu       sync.Mutex
	sessions map[int]string // tabID → CF sessionID
}

// NewClient creates a new CF Browser Rendering client.
func NewClient(baseURL, apiToken string) *Client {
	return &Client{
		baseURL:    baseURL,
		apiToken:   apiToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		sessions:   make(map[int]string),
	}
}

// SessionForTab returns the CF session ID mapped to a synthetic tab ID.
// Creates a mapping if one doesn't exist (returns "" in that case).
func (c *Client) SessionForTab(tabID int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[tabID]
}

// SetSessionForTab maps a synthetic tab ID to a CF session ID.
func (c *Client) SetSessionForTab(tabID int, sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[tabID] = sessionID
}

// RemoveSession removes the tab→session mapping.
func (c *Client) RemoveSession(tabID int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, tabID)
}

// CreateSession launches a browser on CF, navigates to url, and injects cookies.
func (c *Client) CreateSession(ctx context.Context, url string, cookies []api.CookieMsg) (string, error) {
	body := map[string]any{"url": url}
	if len(cookies) > 0 {
		body["cookies"] = cookies
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := c.post(ctx, "/session", body, &resp); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return resp.ID, nil
}

// Navigate navigates an existing session to a new URL.
func (c *Client) Navigate(ctx context.Context, sessionID, url string) error {
	return c.post(ctx, "/session/"+sessionID+"/navigate", map[string]any{"url": url}, nil)
}

// RequestSummary builds the DOM registry and returns the summary.
func (c *Client) RequestSummary(ctx context.Context, sessionID string) (api.SummaryResponse, error) {
	var resp struct {
		Summary string `json:"summary"`
		URL     string `json:"url"`
	}
	if err := c.post(ctx, "/session/"+sessionID+"/summary", nil, &resp); err != nil {
		return api.SummaryResponse{}, fmt.Errorf("request summary: %w", err)
	}
	return api.SummaryResponse{Summary: resp.Summary, URL: resp.URL}, nil
}

// CaptureScreenshot returns a base64-encoded PNG screenshot.
func (c *Client) CaptureScreenshot(ctx context.Context, sessionID string, width, height float64) (string, error) {
	body := map[string]any{}
	if width > 0 && height > 0 {
		body["width"] = width
		body["height"] = height
	}
	var resp struct {
		Base64 string `json:"base64"`
	}
	if err := c.post(ctx, "/session/"+sessionID+"/screenshot", body, &resp); err != nil {
		return "", fmt.Errorf("screenshot: %w", err)
	}
	return resp.Base64, nil
}

// FullAXTree returns enriched AX data as summary-line additions.
func (c *Client) FullAXTree(ctx context.Context, sessionID string) (string, error) {
	var resp struct {
		Enriched string `json:"enriched"`
	}
	if err := c.post(ctx, "/session/"+sessionID+"/ax-tree", nil, &resp); err != nil {
		return "", fmt.Errorf("ax tree: %w", err)
	}
	return resp.Enriched, nil
}

// PageText returns the visible innerText of the page.
func (c *Client) PageText(ctx context.Context, sessionID string) (string, error) {
	var resp struct {
		Text string `json:"text"`
	}
	if err := c.post(ctx, "/session/"+sessionID+"/page-text", nil, &resp); err != nil {
		return "", fmt.Errorf("page text: %w", err)
	}
	return resp.Text, nil
}

// LayoutMetrics returns the CSS content dimensions.
func (c *Client) LayoutMetrics(ctx context.Context, sessionID string) (float64, float64, error) {
	var resp struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}
	if err := c.post(ctx, "/session/"+sessionID+"/layout", nil, &resp); err != nil {
		return 0, 0, fmt.Errorf("layout metrics: %w", err)
	}
	return resp.Width, resp.Height, nil
}

// ExecuteAction dispatches click/type/enter/focus on a mache element.
func (c *Client) ExecuteAction(ctx context.Context, sessionID, macheID, action, payload string) error {
	return c.post(ctx, "/session/"+sessionID+"/action", map[string]any{
		"macheID": macheID,
		"action":  action,
		"payload": payload,
	}, nil)
}

// Scroll scrolls the page and returns the updated DOM state.
func (c *Client) Scroll(ctx context.Context, sessionID, direction string) (api.DOMUpdate, error) {
	var resp struct {
		Summary        string              `json:"summary"`
		ResolvedItems  map[string][]string `json:"resolved_items"`
		AtBottom       bool                `json:"at_bottom"`
		AtTop          bool                `json:"at_top"`
		ScrollMoved    bool                `json:"scroll_moved"`
		ScrollY        float64             `json:"scroll_y"`
		ScrollHeight   float64             `json:"scroll_height"`
		ViewportHeight float64             `json:"viewport_height"`
	}
	if err := c.post(ctx, "/session/"+sessionID+"/scroll", map[string]any{"direction": direction}, &resp); err != nil {
		return api.DOMUpdate{}, fmt.Errorf("scroll: %w", err)
	}
	return api.DOMUpdate{
		Summary:        resp.Summary,
		ResolvedItems:  resp.ResolvedItems,
		AtBottom:       resp.AtBottom,
		AtTop:          resp.AtTop,
		ScrollMoved:    resp.ScrollMoved,
		ScrollY:        resp.ScrollY,
		ScrollHeight:   resp.ScrollHeight,
		ViewportHeight: resp.ViewportHeight,
	}, nil
}

// CloseSession closes the browser and cleans up.
func (c *Client) CloseSession(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/session/"+sessionID, nil)
	if err != nil {
		return err
	}
	if c.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("close session: HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

// post sends a POST request to the worker and decodes the response.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	} else {
		reqBody = bytes.NewReader([]byte("{}"))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, respBody)
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w (body: %s)", err, respBody)
		}
	}
	return nil
}
