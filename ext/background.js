// background.js - Service worker for X-Ray extension (DOM bridge only)

const DEFAULT_WS_URL = 'ws://localhost:8080/ws';

let ws = null;
let reconnectTimer = null;
let wsUrl = DEFAULT_WS_URL;

const schemaReadyTabs = new Set();
const pendingSnapshots = new Set();
const pendingIntents = new Map(); // tabId → intent string (queued before schema ready)
const lastKnownUrls = new Map(); // tabId → URL (for detecting manual navigation)
const gotoInFlight = new Set();  // tabIds currently navigated by GOTO_URL (skip auto-snapshot)
const overlayVisible = new Map(); // tabId → boolean (overlay toggle state)
let agentdLaunching = false;
const humanOverlayVisible = new Map(); // tabId → boolean (human overlay toggle state)

// --- Side panel port registry ---
const sidePanelPorts = new Set();
chrome.runtime.onConnect.addListener((port) => {
  if (port.name !== 'sidepanel') return;
  sidePanelPorts.add(port);
  port.onDisconnect.addListener(() => sidePanelPorts.delete(port));
});

// Load configured WebSocket URL, then connect (auto-launching agentd if needed).
chrome.storage.local.get({ wsUrl: DEFAULT_WS_URL }, async (items) => {
  wsUrl = items.wsUrl;
  console.log('X-Ray: Using WebSocket URL:', wsUrl);

  // Health check: is agentd already running?
  const httpBase = wsUrl.replace(/^ws(s?):\/\//, 'http$1://').replace(/\/ws$/, '');
  try {
    const resp = await fetch(`${httpBase}/status`, { signal: AbortSignal.timeout(1500) });
    if (resp.ok) {
      console.log('X-Ray: agentd already running');
      connectWebSocket();
      return;
    }
  } catch (_) {}

  // Not running — auto-launch via native messaging.
  console.log('X-Ray: agentd not running, launching via native messaging');
  launchAgentd(httpBase);
});

// Re-connect when the URL is changed at runtime.
chrome.storage.onChanged.addListener((changes, area) => {
  if (area === 'local' && changes.wsUrl) {
    wsUrl = changes.wsUrl.newValue || DEFAULT_WS_URL;
    console.log('X-Ray: WebSocket URL changed to:', wsUrl);
    if (ws) ws.close();
    connectWebSocket();
  }
});

function connectWebSocket() {
  if (ws && ws.readyState === WebSocket.OPEN) return;

  ws = new WebSocket(wsUrl);

  ws.onopen = () => {
    console.log('X-Ray: Connected to agentd');
    // Server state is fresh on reconnect — clear stale schema tracking
    schemaReadyTabs.clear();
    pendingIntents.clear();
    if (reconnectTimer) {
      clearInterval(reconnectTimer);
      reconnectTimer = null;
    }
    // Tell server which tab is currently active (voice daemon needs this),
    // then trigger an initial capture so the schema is ready immediately.
    chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
      if (tabs[0] && ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'TAB_ACTIVATED', tab_id: tabs[0].id }));
        console.log('X-Ray: Sent initial TAB_ACTIVATED for tab', tabs[0].id);
        // Kick off initial capture for the already-loaded tab (skip chrome:// and other non-http pages).
        const url = tabs[0].url || '';
        if (url.startsWith('http://') || url.startsWith('https://')) {
          captureAndSend(tabs[0].id);
        }
      }
    });
    // Notify side panels of WS connection status.
    for (const port of sidePanelPorts) {
      try { port.postMessage({ type: 'WS_STATUS', connected: true }); } catch (_) {}
    }
  };

  // Forward agent events to content.js for the in-page log overlay,
  // and broadcast to all connected side panel ports.
  function sendAgentLog(tabId, icon, text) {
    const send = (tid) => {
      chrome.tabs.sendMessage(tid, { type: 'AGENT_LOG', icon, text }).catch(() => {});
    };
    if (tabId) { send(tabId); } else {
      chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
        if (tabs[0]) send(tabs[0].id);
      });
    }
    // Broadcast to all connected side panel ports.
    const logEntry = { type: 'AGENT_LOG', icon, text, ts: Date.now() };
    for (const port of sidePanelPorts) {
      try { port.postMessage(logEntry); } catch (_) {}
    }
  }

  ws.onmessage = async (event) => {
    const msg = JSON.parse(event.data);
    console.log('X-Ray: Received', msg.type, msg);

    switch (msg.type) {
      case 'EXECUTE_ACTION': {
        const targetTab = msg.tab_id || null;

        // CV pixel-click is handled by Go via CDP proxy (never reaches here).

        const actionMsg = {
          type: 'EXECUTE_ACTION',
          mache_id: msg.mache_id,
          action: msg.action,
          payload: msg.payload || ''
        };
        if (targetTab != null) {
          chrome.tabs.sendMessage(targetTab, actionMsg);
        } else {
          chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
            if (tabs[0]) {
              chrome.tabs.sendMessage(tabs[0].id, actionMsg);
            }
          });
        }
        sendAgentLog(targetTab, 'A',
          `${msg.action} -> ${msg.mache_id}${msg.payload ? ': ' + msg.payload.substring(0, 40) : ''}`);
        break;
      }

      case 'SCROLL': {
        const direction = msg.direction || 'down';
        sendAgentLog(msg.tab_id || null, 'V', `scroll ${direction}`);
        const doScroll = (targetTab) => {
          chrome.tabs.sendMessage(targetTab, {
            type: 'SCROLL',
            direction,
            selectors: msg.selectors || {}
          }, (response) => {
            if (chrome.runtime.lastError || !response) {
              console.error('X-Ray: Scroll failed', chrome.runtime.lastError);
              return;
            }
            // Send updated summary + resolved items back to server
            if (ws && ws.readyState === WebSocket.OPEN) {
              ws.send(JSON.stringify({
                type: 'DOM_UPDATE',
                tab_id: targetTab,
                summary: response.summary,
                url: response.url,
                resolved_items: response.resolved_items || {}
              }));
              console.log('X-Ray: Scroll', direction, '— sent DOM_UPDATE for tab', targetTab,
                'resolved:', Object.keys(response.resolved_items || {}).length, 'zones');
            }
          });
        };
        if (msg.tab_id) {
          doScroll(msg.tab_id);
        } else {
          chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
            if (tabs[0]) doScroll(tabs[0].id);
          });
        }
        break;
      }

      case 'GOTO_URL': {
        const url = msg.url;
        sendAgentLog(msg.tab_id || null, 'G', `goto -> ${url}`);
        if (!url) break;
        // Resolve tab: use provided tab_id, fall back to active tab if 0/missing.
        const doGoto = (targetTab) => {
          if (!targetTab) return;
          console.log('X-Ray: Navigating tab', targetTab, 'to', url);
          schemaReadyTabs.delete(targetTab);
          gotoInFlight.add(targetTab); // Suppress auto-snapshot from persistent listener
          // Reset content.js registry so mache-IDs restart from 0 (Bug #7 fix).
          chrome.tabs.sendMessage(targetTab, { type: 'RESET_REGISTRY' }).catch(() => {});
          chrome.tabs.update(targetTab, { url }, (tab) => {
            // Bring the Chrome window to the foreground (Bug #1 fix).
            if (tab && tab.windowId) {
              chrome.windows.update(tab.windowId, { focused: true });
            }
            // Wait for page to finish loading, then auto-snapshot.
            // Safety timeout: remove listener if page never completes (blocked, stopped, error).
            let cleaned = false;
            const cleanup = () => {
              if (cleaned) return;
              cleaned = true;
              chrome.tabs.onUpdated.removeListener(listener);
              gotoInFlight.delete(targetTab);
            };
            const listener = (tabId, changeInfo) => {
              if (tabId === targetTab && changeInfo.status === 'complete') {
                cleanup();
                lastKnownUrls.set(targetTab, url); // Sync tracked URL
                console.log('X-Ray: Page loaded, waiting for layout to stabilize (tab', targetTab, ')');
                // Wait for SPA layout shifts to settle before snapshot (Bug #9 fix).
                waitForLayoutStable(targetTab).then(() => {
                  console.log('X-Ray: Layout stable, auto-capturing snapshot for tab', targetTab);
                  pendingSnapshots.add(targetTab);
                  captureAndSend(targetTab);
                });
              }
            };
            chrome.tabs.onUpdated.addListener(listener);
            setTimeout(() => {
              if (!cleaned) {
                console.warn('X-Ray: GOTO_URL listener timeout for tab', targetTab, '— cleaning up');
                cleanup();
              }
            }, 30000); // 30s safety net
          });
        };
        if (msg.tab_id) {
          doGoto(msg.tab_id);
        } else {
          chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
            if (tabs[0]) {
              console.log('X-Ray: GOTO_URL tab_id=0, resolved to active tab', tabs[0].id);
              doGoto(tabs[0].id);
            }
          });
        }
        break;
      }

      case 'RESCAN': {
        const targetMacheId = msg.mache_id || null;
        const doRescan = (tabId) => {
          console.log('X-Ray: Rescan requested for tab', tabId,
            targetMacheId ? `(zoom: ${targetMacheId})` : '(full page)');
          // Reset registry on full-page rescan so IDs restart from 0 (Bug #7 fix).
          if (!targetMacheId) {
            chrome.tabs.sendMessage(tabId, { type: 'RESET_REGISTRY' }).catch(() => {});
          }
          captureAndSend(tabId, true, targetMacheId);
        };
        if (msg.tab_id) {
          doRescan(msg.tab_id);
        } else {
          chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
            if (tabs[0]) doRescan(tabs[0].id);
          });
        }
        break;
      }

      case 'LIST_TABS': {
        chrome.tabs.query({}, (tabs) => {
          const tabList = tabs
            .filter(t => t.url && !/^(chrome|about|edge|brave):\/\//.test(t.url))
            .map(t => ({ id: t.id, title: t.title || '', url: t.url }));
          if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'TABS_LISTED', tabs: tabList }));
          }
          console.log('X-Ray: Listed', tabList.length, 'tabs');
        });
        break;
      }

      case 'CREATE_TAB': {
        const url = msg.url;
        console.log('X-Ray: CREATE_TAB received, url:', url);
        if (!url) break;
        chrome.tabs.create({ url, active: true }, (tab) => {
          if (chrome.runtime.lastError) {
            console.error('X-Ray: CREATE_TAB failed:', chrome.runtime.lastError);
            return;
          }
          if (!tab) {
            console.error('X-Ray: CREATE_TAB returned null tab');
            return;
          }
          console.log('X-Ray: Created tab', tab.id, 'for', url);
          if (tab.windowId) chrome.windows.update(tab.windowId, { focused: true });
          // Notify server immediately (before schema ready) so activeVoiceTab is set.
          if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'TAB_ACTIVATED', tab_id: tab.id }));
          }
          // Wait for page load, then snapshot.
          const listener = (tabId, changeInfo) => {
            if (tabId === tab.id && changeInfo.status === 'complete') {
              chrome.tabs.onUpdated.removeListener(listener);
              lastKnownUrls.set(tab.id, url); // Sync tracked URL
              waitForLayoutStable(tab.id).then(() => {
                pendingSnapshots.add(tab.id);
                captureAndSend(tab.id);
              });
            }
          };
          chrome.tabs.onUpdated.addListener(listener);
          setTimeout(() => chrome.tabs.onUpdated.removeListener(listener), 30000);
        });
        break;
      }

      case 'SWITCH_TAB': {
        const targetTab = msg.tab_id;
        if (!targetTab) break;
        console.log('X-Ray: Switching to tab', targetTab);
        chrome.tabs.update(targetTab, { active: true }, (tab) => {
          if (chrome.runtime.lastError) {
            console.error('X-Ray: Switch tab failed', chrome.runtime.lastError);
            return;
          }
          // Bring the Chrome window to the foreground (Bug #1 fix).
          if (tab && tab.windowId) {
            chrome.windows.update(tab.windowId, { focused: true });
          }
          // Auto-snapshot the newly active tab so the schema updates.
          captureAndSend(targetTab);
        });
        break;
      }

      case 'RESOLVE_SELECTORS': {
        const targetTab = msg.tab_id;
        if (targetTab) {
          chrome.tabs.sendMessage(targetTab, {
            type: 'RESOLVE_SELECTORS',
            selectors: msg.selectors || {}
          }, (response) => {
            if (chrome.runtime.lastError || !response) {
              console.error('X-Ray: Resolve selectors failed', chrome.runtime.lastError);
              // Send empty response so server doesn't hang
              if (ws && ws.readyState === WebSocket.OPEN) {
                ws.send(JSON.stringify({
                  type: 'SELECTORS_RESOLVED',
                  tab_id: targetTab,
                  resolved_items: {}
                }));
              }
              return;
            }
            if (ws && ws.readyState === WebSocket.OPEN) {
              ws.send(JSON.stringify({
                type: 'SELECTORS_RESOLVED',
                tab_id: targetTab,
                resolved_items: response.resolved_items || {}
              }));
              console.log('X-Ray: Selectors resolved for tab', targetTab,
                Object.keys(response.resolved_items || {}).length, 'zones');
            }
          });
        }
        break;
      }

      case 'SCHEMA_READY': {
        const tabId = msg.tab_id;
        console.log('X-Ray: Schema ready (tab', tabId, ')');
        if (tabId) {
          schemaReadyTabs.add(tabId);
          pendingSnapshots.delete(tabId);
          try {
            chrome.runtime.sendMessage({ type: 'SCHEMA_READY_EVENT', tabId });
          } catch (_) {}
        }

        // Forward zone data to content.js for visual rendering.
        if (msg.schema && msg.schema.mounts) {
          chrome.tabs.sendMessage(tabId, {
            type: 'DRAW_ZONES',
            zones: msg.schema.mounts
          });
        }

        sendAgentLog(tabId, 'R', `schema ready -- ${(msg.schema?.mounts?.length || 0)} zones`);

        // Flush any queued intent for this tab.
        if (tabId && pendingIntents.has(tabId) && ws && ws.readyState === WebSocket.OPEN) {
          const intent = pendingIntents.get(tabId);
          pendingIntents.delete(tabId);
          console.log('X-Ray: Flushing queued intent for tab', tabId, ':', intent);
          ws.send(JSON.stringify({ type: 'NAVIGATE', tab_id: tabId, intent }));
        }
        break;
      }

      case 'STATUS':
        console.log('X-Ray: Status -', msg.stage, msg.message);
        sendAgentLog(msg.tab_id || null,
          msg.stage === 'error' ? '!' : msg.stage === 'cartographer' ? 'C' : 'S',
          `[${msg.stage}] ${msg.message}`);
        break;

      // --- Go-driven capture orchestration ---

      case 'REQUEST_SUMMARY': {
        const tabId = msg.tab_id;
        let response;
        try {
          response = await chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' });
        } catch (e) {
          console.log('X-Ray: Content script not ready for REQUEST_SUMMARY, injecting into tab', tabId);
          // Wait for the tab to finish loading before injecting content script.
          try {
            await waitForTabComplete(tabId, 8000);
            await chrome.scripting.executeScript({
              target: { tabId },
              files: ['content.js']
            });
            await new Promise(r => setTimeout(r, 200));
            response = await chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' });
          } catch (retryErr) {
            console.error('X-Ray: Content script inject failed for tab', tabId, retryErr.message);
            break;
          }
        }
        if (response && response.summary && ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({
            type: 'SUMMARY_RESPONSE',
            tab_id: tabId,
            summary: response.summary,
            url: response.url
          }));
        }
        break;
      }

      case 'DRAW_OVERLAY_CMD': {
        const tabId = msg.tab_id;
        try {
          await chrome.tabs.sendMessage(tabId, { type: 'DRAW_OVERLAY' });
        } catch (e) {
          console.warn('X-Ray: Draw overlay (Go cmd) failed:', e.message);
        }
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'OVERLAY_DRAWN', tab_id: tabId }));
        }
        break;
      }

      case 'REMOVE_OVERLAY_CMD': {
        const tabId = msg.tab_id;
        try {
          await chrome.tabs.sendMessage(tabId, { type: 'REMOVE_OVERLAY' });
        } catch (_) { /* best-effort */ }
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'OVERLAY_REMOVED', tab_id: tabId }));
        }
        break;
      }

      case 'DRAW_HUMAN_OVERLAY_CMD': {
        const tabId = msg.tab_id;
        try {
          await chrome.tabs.sendMessage(tabId, { type: 'DRAW_HUMAN_OVERLAY' });
          humanOverlayVisible.set(tabId, true);
        } catch (_) {}
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'HUMAN_OVERLAY_DRAWN', tab_id: tabId }));
        }
        break;
      }

      // --- CDP proxy (Dumb Pipe architecture) ---

      case 'CDP_ATTACH': {
        const tabId = msg.tab_id;
        try {
          await chrome.debugger.attach({ tabId }, '1.3');
          ws.send(JSON.stringify({ type: 'CDP_ATTACHED', tab_id: tabId }));
        } catch (e) {
          ws.send(JSON.stringify({ type: 'CDP_ATTACH_FAILED', tab_id: tabId, cdp_error: e.message }));
        }
        break;
      }

      case 'CDP_SEND': {
        const { cdp_id, tab_id, cdp_method, cdp_params } = msg;
        try {
          const result = await chrome.debugger.sendCommand(
            { tabId: tab_id }, cdp_method, cdp_params || {}
          );
          ws.send(JSON.stringify({ type: 'CDP_RESULT', cdp_id, cdp_result: result }));
        } catch (e) {
          ws.send(JSON.stringify({ type: 'CDP_ERROR', cdp_id, cdp_error: e.message }));
        }
        break;
      }

      case 'CDP_DETACH': {
        const tabId = msg.tab_id;
        try {
          await chrome.debugger.detach({ tabId });
        } catch (_) {}
        ws.send(JSON.stringify({ type: 'CDP_DETACHED', tab_id: tabId }));
        break;
      }
    }
  };

  ws.onclose = () => {
    console.log('X-Ray: Disconnected, will retry in 5s');
    ws = null;
    if (!reconnectTimer) {
      reconnectTimer = setInterval(connectWebSocket, 5000);
    }
    // Notify side panels of WS disconnection.
    for (const port of sidePanelPorts) {
      try { port.postMessage({ type: 'WS_STATUS', connected: false }); } catch (_) {}
    }
  };

  ws.onerror = (err) => {
    console.error('X-Ray: WebSocket error', err);
    ws.close();
  };

  // Keep the Manifest V3 service worker alive by sending a heartbeat.
  // The Go server ignores PING messages — their arrival alone keeps Chrome awake.
  if (!self.xrayPingInterval) {
    self.xrayPingInterval = setInterval(() => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'PING' }));
      }
    }, 20000);
  }
}

// --- Tab loading: wait for tab to reach "complete" status before injecting scripts ---
function waitForTabComplete(tabId, timeoutMs = 8000) {
  return new Promise((resolve, reject) => {
    // Check if already complete.
    chrome.tabs.get(tabId, (tab) => {
      if (chrome.runtime.lastError) {
        reject(new Error(chrome.runtime.lastError.message));
        return;
      }
      if (tab.status === 'complete') {
        resolve();
        return;
      }
      // Wait for onUpdated to fire with status 'complete'.
      const timer = setTimeout(() => {
        chrome.tabs.onUpdated.removeListener(listener);
        reject(new Error(`Tab ${tabId} did not reach complete within ${timeoutMs}ms`));
      }, timeoutMs);
      function listener(updatedTabId, changeInfo) {
        if (updatedTabId === tabId && changeInfo.status === 'complete') {
          clearTimeout(timer);
          chrome.tabs.onUpdated.removeListener(listener);
          resolve();
        }
      }
      chrome.tabs.onUpdated.addListener(listener);
    });
  });
}

// --- Layout stabilization: wait for DOM to stop mutating before snapshot ---
// Injects a MutationObserver into the page and resolves after 500ms of quiet
// (no DOM changes). Safety cap prevents indefinite waiting on busy pages.
async function waitForLayoutStable(tabId, timeoutMs = 3000) {
  try {
    await chrome.scripting.executeScript({
      target: { tabId },
      func: (timeout) => {
        return new Promise(resolve => {
          const quiet = 500;
          let timer = null;
          const reset = () => {
            clearTimeout(timer);
            timer = setTimeout(() => { observer.disconnect(); resolve(); }, quiet);
          };
          const observer = new MutationObserver(reset);
          observer.observe(document.body || document.documentElement, {
            childList: true, subtree: true, attributes: true
          });
          reset();
          setTimeout(() => { observer.disconnect(); resolve(); }, timeout);
        });
      },
      args: [timeoutMs]
    });
  } catch (e) {
    console.warn('X-Ray: waitForLayoutStable failed, proceeding:', e.message);
  }
}

// Capture snapshot from the given tab: delegates to Go server via PAGE_READY.
// Go orchestrates the full pipeline (summary, overlay, CDP screenshot, AX, layers).
async function captureAndSend(tabId, isRescan = false, targetMacheId = null) {
  if (!ws || ws.readyState !== WebSocket.OPEN) return;
  const payload = { type: 'PAGE_READY', tab_id: tabId };
  if (isRescan) payload.is_rescan = true;
  if (targetMacheId) payload.target_mache_id = targetMacheId;
  ws.send(JSON.stringify(payload));
}

// --- Message handlers from popup ---

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  switch (msg.type) {
    case 'TOGGLE_OVERLAY':
      chrome.tabs.query({ active: true, currentWindow: true }, async (tabs) => {
        if (!tabs[0]) {
          sendResponse({ ok: false, error: 'No active tab' });
          return;
        }
        const tabId = tabs[0].id;
        try {
          if (humanOverlayVisible.get(tabId)) {
            await chrome.tabs.sendMessage(tabId, { type: 'REMOVE_HUMAN_OVERLAY' });
            humanOverlayVisible.set(tabId, false);
            sendResponse({ ok: true, visible: false });
          } else {
            // Ensure registry is built before drawing.
            try {
              await chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' });
            } catch (_) {
              await chrome.scripting.executeScript({ target: { tabId }, files: ['content.js'] });
              await new Promise(r => setTimeout(r, 200));
              await chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' });
            }
            await chrome.tabs.sendMessage(tabId, { type: 'DRAW_HUMAN_OVERLAY' });
            humanOverlayVisible.set(tabId, true);
            sendResponse({ ok: true, visible: true });
          }
        } catch (e) {
          sendResponse({ ok: false, error: e.message });
        }
      });
      return true;

    case 'EXPORT_OVERLAY':
      chrome.tabs.query({ active: true, currentWindow: true }, async (tabs) => {
        if (!tabs[0]) {
          sendResponse({ ok: false, error: 'No active tab' });
          return;
        }
        const tabId = tabs[0].id;
        try {
          // Build registry + draw overlay.
          try {
            await chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' });
          } catch (_) {
            await chrome.scripting.executeScript({ target: { tabId }, files: ['content.js'] });
            await new Promise(r => setTimeout(r, 200));
            await chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' });
          }
          await chrome.tabs.sendMessage(tabId, { type: 'DRAW_OVERLAY' });
          // Extra paint settle time — rAF in content.js handles most cases,
          // but some pages (Reddit logged-out) need more time to composite.
          await new Promise(r => setTimeout(r, 300));

          // Capture visible viewport (overlay is on screen).
          const dataUrl = await chrome.tabs.captureVisibleTab(null, { format: 'png' });

          // Remove overlay.
          try { await chrome.tabs.sendMessage(tabId, { type: 'REMOVE_OVERLAY' }); } catch (_) {}
          const timestamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
          chrome.downloads.download({
            url: dataUrl,
            filename: `xray-overlay-${timestamp}.png`,
            saveAs: false
          });
          sendResponse({ ok: true });
        } catch (e) {
          try { await chrome.tabs.sendMessage(tabId, { type: 'REMOVE_OVERLAY' }); } catch (_) {}
          sendResponse({ ok: false, error: e.message });
        }
      });
      return true;

    case 'TRIGGER_SNAPSHOT':
      chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
        if (tabs[0]) {
          const url = tabs[0].url || '';
          if (/^(chrome|about|edge|brave):\/\//.test(url)) {
            sendResponse({ ok: false, error: 'Cannot snapshot restricted page — use goto to navigate first' });
            return;
          }
          const tabId = tabs[0].id;
          lastKnownUrls.set(tabId, url); // Sync so auto-snapshot doesn't re-fire
          if (pendingSnapshots.has(tabId)) {
            sendResponse({ ok: true, tabId });
            return;
          }
          pendingSnapshots.add(tabId);
          captureAndSend(tabId);
          sendResponse({ ok: true, tabId });
        } else {
          sendResponse({ ok: false, error: 'No active tab' });
        }
      });
      return true;

    case 'SEND_INTENT':
      if (!ws || ws.readyState !== WebSocket.OPEN) {
        sendResponse({ ok: false, error: 'Not connected to agentd' });
        return false;
      }
      chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
        const tabId = tabs[0]?.id;
        if (!tabId) {
          sendResponse({ ok: false, error: 'No active tab' });
          return;
        }
        // On restricted URLs (chrome://, about://), there's no schema and never will be.
        const url = tabs[0].url || '';
        const restricted = /^(chrome|about|edge|brave):\/\//.test(url);
        if (restricted && !schemaReadyTabs.has(tabId)) {
          sendResponse({ ok: false, error: 'Navigate to a website first' });
          return;
        }
        if (!schemaReadyTabs.has(tabId)) {
          // Queue intent — it will be sent when SCHEMA_READY arrives.
          pendingIntents.set(tabId, msg.intent);
          console.log('X-Ray: Queued intent for tab', tabId, '(waiting for schema):', msg.intent);
          sendResponse({ ok: true, message: 'Queued — will run when schema is ready' });
          return;
        }
        ws.send(JSON.stringify({
          type: 'NAVIGATE',
          tab_id: tabId,
          intent: msg.intent
        }));
        sendResponse({ ok: true, message: 'Sent: ' + msg.intent });
      });
      return true;

    case 'CHECK_SCHEMA':
      chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
        const tabId = tabs[0]?.id;
        sendResponse({
          hasSchema: tabId ? schemaReadyTabs.has(tabId) : false,
          pending: tabId ? pendingSnapshots.has(tabId) : false,
          wsConnected: ws && ws.readyState === WebSocket.OPEN,
          launching: agentdLaunching,
          tabId
        });
      });
      return true;

    case 'OPEN_VOICE':
      chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
        const tabId = tabs[0]?.id;
        if (!tabId) {
          sendResponse({ ok: false, error: 'No active tab' });
          return;
        }
        const voiceUrl = 'http://localhost:8080/voice-ui?tab=' + tabId;
        chrome.tabs.create({ url: voiceUrl }, () => {
          sendResponse({ ok: true });
        });
      });
      return true;

    case 'OPEN_SIDEPANEL':
      chrome.sidePanel.open({ windowId: chrome.windows.WINDOW_ID_CURRENT }).then(() => {
        sendResponse({ ok: true });
      }).catch(() => {
        sendResponse({ ok: false });
      });
      return true;

    case 'DOM_MUTATED':
      // Forward content script MutationObserver signal to the server.
      if (ws && ws.readyState === WebSocket.OPEN && sender.tab?.id) {
        ws.send(JSON.stringify({ type: 'DOM_MUTATED', tab_id: sender.tab.id }));
      }
      return false;
  }
});

// --- Track active tab for voice daemon ---
// Only send TAB_ACTIVATED for tabs that have a schema (i.e., real content tabs).
// This prevents the voice UI tab (localhost:8080/voice-ui) from polluting
// the server's activeVoiceTab, which would cause voice commands to target
// an empty session instead of the user's actual page.
chrome.tabs.onActivated.addListener((activeInfo) => {
  if (ws && ws.readyState === WebSocket.OPEN && schemaReadyTabs.has(activeInfo.tabId)) {
    ws.send(JSON.stringify({ type: 'TAB_ACTIVATED', tab_id: activeInfo.tabId }));
  }
});

// --- Keyboard shortcut: toggle overlay ---
chrome.commands.onCommand.addListener((command) => {
  if (command === 'toggle-overlay') {
    chrome.tabs.query({ active: true, currentWindow: true }, async (tabs) => {
      if (!tabs[0]) return;
      const tabId = tabs[0].id;
      try {
        if (humanOverlayVisible.get(tabId)) {
          await chrome.tabs.sendMessage(tabId, { type: 'REMOVE_HUMAN_OVERLAY' });
          humanOverlayVisible.set(tabId, false);
        } else {
          try {
            await chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' });
          } catch (_) {
            await chrome.scripting.executeScript({ target: { tabId }, files: ['content.js'] });
            await new Promise(r => setTimeout(r, 200));
            await chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' });
          }
          await chrome.tabs.sendMessage(tabId, { type: 'DRAW_HUMAN_OVERLAY' });
          humanOverlayVisible.set(tabId, true);
        }
      } catch (e) {
        console.error('X-Ray: toggle-overlay failed:', e);
      }
    });
  }
});

// --- Auto-snapshot on manual page navigation ---
// Persistent listener: when the user navigates manually (typing URL, clicking a link),
// detect the URL change and auto-snapshot if no schema exists for the new page.
chrome.tabs.onUpdated.addListener((tabId, changeInfo, tab) => {
  // Only act when page finishes loading.
  if (changeInfo.status !== 'complete') return;

  // Skip if GOTO_URL is handling this tab (it has its own one-time listener).
  if (gotoInFlight.has(tabId)) return;

  // Skip restricted URLs.
  const url = tab.url || '';
  if (/^(chrome|about|edge|brave):\/\//.test(url)) return;

  // Skip if URL hasn't actually changed (e.g., in-page anchor, reload).
  const prev = lastKnownUrls.get(tabId);
  lastKnownUrls.set(tabId, url);
  if (prev === url) return;

  // Skip if snapshot already in progress for this tab.
  if (pendingSnapshots.has(tabId)) {
    schemaReadyTabs.delete(tabId); // Invalidate stale schema for new URL.
    return;
  }
  // Invalidate stale schema and overlay (URL changed, old schema is for the previous page).
  if (schemaReadyTabs.has(tabId)) {
    schemaReadyTabs.delete(tabId);
  }
  overlayVisible.delete(tabId);

  // Not connected to server — no point snapshotting.
  if (!ws || ws.readyState !== WebSocket.OPEN) return;

  console.log('X-Ray: URL changed (manual nav), waiting for layout to stabilize (tab', tabId, ')');
  pendingSnapshots.add(tabId);
  // Wait for SPA layout shifts to settle before snapshot (Bug #9 fix).
  waitForLayoutStable(tabId).then(() => {
    console.log('X-Ray: Layout stable, auto-snapshot for tab', tabId, '→', url);
    captureAndSend(tabId);
  });
});

// Clean up tracking when tabs close and notify the server to prune the session.
chrome.tabs.onRemoved.addListener((tabId) => {
  lastKnownUrls.delete(tabId);
  schemaReadyTabs.delete(tabId);
  pendingSnapshots.delete(tabId);
  pendingIntents.delete(tabId);
  gotoInFlight.delete(tabId);
  overlayVisible.delete(tabId);
  humanOverlayVisible.delete(tabId);
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ type: 'TAB_CLOSED', tab_id: tabId }));
  }
});

// Forward CDP events to Go server for proxy consumers.
chrome.debugger.onEvent.addListener((source, method, params) => {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({
      type: 'CDP_EVENT',
      tab_id: source.tabId,
      cdp_method: method,
      cdp_params: params
    }));
  }
});
