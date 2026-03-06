package api

import "context"

// BrowserBackend abstracts the browser interaction layer.
// The extension WebSocket path (default) does not use this interface — it uses
// the existing channel-based orchestration in captureGo/dispatchAction.
// The Cloudflare Browser Rendering path implements this interface, providing
// the same data (summary, screenshot, AX tree, page text) via HTTP.
type BrowserBackend interface {
	// CreateSession launches a new browser, navigates to url, and injects cookies.
	CreateSession(ctx context.Context, url string, cookies []CookieMsg) (sessionID string, err error)

	// Navigate navigates an existing session to a new URL.
	Navigate(ctx context.Context, sessionID, url string) error

	// RequestSummary builds the DOM registry and returns the element summary + page URL.
	RequestSummary(ctx context.Context, sessionID string) (SummaryResponse, error)

	// CaptureScreenshot returns a base64-encoded PNG screenshot.
	// If clip is nil, captures the full page.
	CaptureScreenshot(ctx context.Context, sessionID string, width, height float64) (base64PNG string, err error)

	// FullAXTree returns the raw accessibility tree as enrichment-ready text lines.
	// The backend is responsible for joining AX nodes to mache IDs.
	FullAXTree(ctx context.Context, sessionID string) (enrichedLines string, err error)

	// PageText returns the visible innerText of the page.
	PageText(ctx context.Context, sessionID string) (string, error)

	// LayoutMetrics returns the CSS content dimensions.
	LayoutMetrics(ctx context.Context, sessionID string) (width, height float64, err error)

	// ExecuteAction dispatches a click/type/enter/focus action on a mache element.
	ExecuteAction(ctx context.Context, sessionID, macheID, action, payload string) error

	// Scroll scrolls the page and returns the updated DOM state.
	Scroll(ctx context.Context, sessionID, direction string) (DOMUpdate, error)

	// CloseSession closes the browser and cleans up resources.
	CloseSession(ctx context.Context, sessionID string) error
}
