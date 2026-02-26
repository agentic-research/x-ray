# Known Architectural Issues & Bugs

This document outlines several concurrency, state management, and OS-integration bugs currently present in the X-Ray architecture.

## 1. Chrome Window Focus (macOS Permissions & Extension API)
**Symptom:** Running `task demo` and issuing a `goto` command when Chrome is already running in the background causes Chrome to navigate silently without bringing the window to the foreground.

**Root Causes:**
1. **Missing Extension Focus API:** When the WebSocket is connected, `ext/background.js` uses `chrome.tabs.update(tabId, {url})` to navigate. This updates the tab but does *not* request window focus. It needs to be paired with `chrome.windows.update(tab.windowId, { focused: true })`.
2. **macOS AppleScript Permissions:** When the WebSocket is disconnected, the Go backend falls back to `osascript -e 'tell application "Google Chrome" to activate'`. If the terminal running X-Ray hasn't been explicitly granted "Automation" or "Accessibility" permissions in macOS System Settings > Privacy & Security, the OS silently blocks the activation command.

## 2. Chrome Extension WebSocket Sleep (The Timeout Bug)
**Symptom:** 30 seconds after a successful navigation in voice mode, the agent unexpectedly announces: *"Navigated to [URL] but page load timed out."*

**Root Cause:**
Chrome Manifest V3 service workers automatically suspend themselves after ~30 seconds of inactivity, terminating the WebSocket connection.
1. When you speak a command while the extension is asleep, the Go backend routes the command to **Tab 0** (the fallback session).
2. The `Doer` for Tab 0 dispatches the `goto` via OS commands, which wakes up Chrome.
3. The extension connects and reports its real ID (e.g., Tab 1684134498). The Cartographer processes the page for the real tab.
4. The backend attempts to unblock the Tab 0 `Doer` (via `oldSess.SignalSchemaReady()`), but the `Doer` continues executing its multi-step loop *bound to the empty engine of Tab 0*.
5. Tab 0 never receives a DOM snapshot, so the next action times out exactly 30 seconds later.

**Status: RESOLVED** — The Go backend already sends WebSocket ping frames every 20s (`websocket.go`, `HandleWebSocket` keep-alive goroutine), which keeps the Chrome MV3 service worker alive. The Tab 0 Doer adoption issue is addressed by the early tab promotion logic in `handleDOMSnapshot`.

## 3. Data Race: `TabSession.SchemaReady` Reassignment
**Symptom:** Panics, deadlocks, or `-race` detector failures when navigating.

**Root Cause:**
The `SchemaReady` channel is used as a synchronization primitive to wait for the Cartographer.
*   In `handleNavigate`, a goroutine blocks reading the channel: `select { case <-sess.SchemaReady: }`
*   Concurrently, if a `goto` or `rescan` is triggered, `ResetSchema()` creates a brand new channel: `s.SchemaReady = make(chan struct{})`.
Replacing a channel pointer while another goroutine is `select`ing on the old pointer is a data race. Furthermore, the goroutine blocked on the old channel will never wake up because the old channel is never closed.

## 4. Data Race: `TabSession.Engine` Pointer
**Symptom:** `-race` detector failures or nil pointer dereferences during concurrent reads/writes to the VFS.

**Root Cause:**
The `Engine` pointer on `TabSession` is completely unguarded by a mutex.
*   The HTTP POST `/navigate` endpoint and `handleDOMSnapshot` concurrently read the engine (e.g., `sess.Engine.HasSchema()`, `sess.Engine.MergeSchema()`).
*   The `Doer` concurrently overwrites the pointer entirely during `goto` and `rescan`: `d.sess.Engine = mache.NewEngine()`.

## 5. Shared `Navigator` Callback Overwrites
**Symptom:** Panics (nil pointer dereference) in `navigator.Agent` when running HTTP navigation concurrently with Voice navigation.

**Root Cause:**
The `Navigator` instance is shared per tab. When an intent is handled, ephemeral callbacks are injected:
```go
sess.Navigator.SetScrollFunc(func(...) { ... })
defer sess.Navigator.SetScrollFunc(nil)
```
If two intents are executing concurrently for the same tab, they overwrite each other's global `scrollTool.scrollFn` without locking. Whichever intent finishes first will execute its `defer` and nil out the callback, crashing the surviving intent when it attempts to scroll.

## 6. Goroutine Leak on Tab Close
**Symptom:** Memory leak of goroutines over the lifetime of the application.

**Root Cause:**
In `websocket.go`, when a tab closes, it calls `sess.Doer.Cancel()`. This cancels the context of the *currently executing goal*, but the `Doer`'s main run loop (`go doer.Run(context.Background())`) uses a background context and its `goalCh` is never closed. The goroutine blocks forever.

## 7. The Stale Extension ID Bug (Memory Leak & Context Bloat)
**Symptom:** `mache-ID` numbers continuously increase without bounding across rescans and page mutations (e.g., `mache-15024`).

**Root Cause:**
In `ext/content.js`, `idCounter` strictly increments and never resets. The function `buildRegistry()` is invoked iteratively or fully upon `RESCAN`. If a single-page app heavily mutates the DOM (swapping large views and removing elements), `idCounter` balloons.
Over time (e.g., if a tab stays open for a few days), the sheer volume of unique IDs inflates the context passed to the LLM and leads to a slow memory leak in `elementRegistry` and `reverseRegistry` since abandoned node references linger within JS maps without explicit garbage collection.
**Fix required:** Reset the `idCounter` and cleanly flush the node registries specifically during full-page `goto` or non-targeted `rescan`.

## 8. Gemini Live `GoAway` Reconnection Bug
**Symptom:** A network error appears during voice chat: `SendClientContent error: use of closed network connection`.

**Root Cause:**
In `internal/api/voice.go`, Google's Live API occasionally issues a `GoAway` message asking clients to reconnect gracefully. While the main loop handles reconnection by tearing down the current session and establishing a new one, the `sender` goroutines bridging the `mic` and `textIn` channels are *not torn down*.
1. First session gets `GoAway`, loops and creates a second session.
2. The outer loop spins up a *new* goroutine for the second session's send loop.
3. The *original* goroutine is still blocking on `<-textIn`.
When you type text, both goroutines compete for it. If the old, defunct goroutine wins the data race, it tries to submit it to the closed session, panicking with `use of closed network connection`.
**Fix required:** Pass a scoped cancellable `context.Context` to the sender goroutines (`sessionCtx, sessionCancel := context.WithCancel(ctx)`) to ensure old goroutines exit when `GoAway` restarts a session.

## 9. Overlay Drift & Premature Snapshotting (SPA Layout Shifts)
**Symptom:** The colored Set-of-Mark bounding boxes appear to "move" or be misaligned with the underlying web elements in the captured screenshot.

**Root Cause:**
There is a race condition between DOM layout stabilization and the screenshot capture process:
1. `ext/background.js` listens for `chrome.tabs.onUpdated` and triggers a snapshot as soon as `changeInfo.status === 'complete'`. This equates to the `window.onload` event.
2. On modern Single Page Applications (SPAs) like React or Next.js, `onload` fires *before* async data is fetched and rendered. Furthermore, web fonts and lazy-loaded images continue to load and shift the DOM layout (Cumulative Layout Shift) *after* `onload`.
3. When `content.js` draws the overlay, it calculates `getBoundingClientRect()` and places `position: absolute` div elements over the UI.
4. If an image or font finishes loading between the `DRAW_OVERLAY` command and the CDP `Page.captureScreenshot` command, the underlying DOM elements get pushed down the page, but the absolutely positioned overlay boxes stay exactly where they were initially drawn.

**Fix required:** Implement a layout stabilization debounce or network idle check before calling `DRAW_OVERLAY` and capturing the snapshot, or use a `ResizeObserver` to delay capture until the layout stops shifting.
