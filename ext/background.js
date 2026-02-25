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

// Load configured WebSocket URL, then connect.
chrome.storage.local.get({ wsUrl: DEFAULT_WS_URL }, (items) => {
  wsUrl = items.wsUrl;
  console.log('X-Ray: Using WebSocket URL:', wsUrl);
  connectWebSocket();
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
    // Tell server which tab is currently active (voice daemon needs this).
    chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
      if (tabs[0] && ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'TAB_ACTIVATED', tab_id: tabs[0].id }));
        console.log('X-Ray: Sent initial TAB_ACTIVATED for tab', tabs[0].id);
      }
    });
  };

  ws.onmessage = (event) => {
    const msg = JSON.parse(event.data);
    console.log('X-Ray: Received', msg.type, msg);

    switch (msg.type) {
      case 'EXECUTE_ACTION': {
        const targetTab = msg.tab_id || null;
        if (targetTab != null) {
          chrome.tabs.sendMessage(targetTab, {
            type: 'EXECUTE_ACTION',
            mache_id: msg.mache_id,
            action: msg.action
          });
        } else {
          chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
            if (tabs[0]) {
              chrome.tabs.sendMessage(tabs[0].id, {
                type: 'EXECUTE_ACTION',
                mache_id: msg.mache_id,
                action: msg.action
              });
            }
          });
        }
        break;
      }

      case 'SCROLL': {
        const direction = msg.direction || 'down';
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
        if (!url) break;
        // Resolve tab: use provided tab_id, fall back to active tab if 0/missing.
        const doGoto = (targetTab) => {
          if (!targetTab) return;
          console.log('X-Ray: Navigating tab', targetTab, 'to', url);
          schemaReadyTabs.delete(targetTab);
          gotoInFlight.add(targetTab); // Suppress auto-snapshot from persistent listener
          chrome.tabs.update(targetTab, { url }, () => {
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
                console.log('X-Ray: Page loaded, auto-capturing snapshot for tab', targetTab);
                pendingSnapshots.add(targetTab);
                captureAndSend(targetTab);
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
        break;
    }
  };

  ws.onclose = () => {
    console.log('X-Ray: Disconnected, will retry in 5s');
    ws = null;
    if (!reconnectTimer) {
      reconnectTimer = setInterval(connectWebSocket, 5000);
    }
  };

  ws.onerror = (err) => {
    console.error('X-Ray: WebSocket error', err);
    ws.close();
  };
}

// --- CDP capture: scaled screenshot + AX enrichment ---

const CDP_MAX_HEIGHT = 16384;  // Cap for infinite-scroll pages
const CDP_TARGET_WIDTH = 800;  // Scaled-down width for Gemini (topology, not pixels)
const CDP_JPEG_QUALITY = 60;   // Low quality is fine for zone identification

// Single CDP session: scaled full-page JPEG screenshot + AX-to-mache-id mapping.
// Falls back to viewport-only screenshot if debugger attach fails.
// When targetMacheId is provided, crops the screenshot to that element's bounding box
// (magnifying glass mode for targeted rescan).
async function captureWithCDP(tabId, targetMacheId = null) {
  try {
    await chrome.debugger.attach({ tabId }, '1.3');
  } catch (e) {
    console.warn('X-Ray: debugger attach failed, falling back to viewport screenshot:', e.message);
    const dataUrl = await chrome.tabs.captureVisibleTab(null, { format: 'jpeg', quality: CDP_JPEG_QUALITY });
    return { screenshot: dataUrl.split(',')[1], axMap: new Map() };
  }

  try {
    // 1. Get page dimensions
    const { cssContentSize } = await chrome.debugger.sendCommand(
      { tabId }, 'Page.getLayoutMetrics', {}
    );
    const captureWidth = cssContentSize.width;
    const captureHeight = Math.min(cssContentSize.height, CDP_MAX_HEIGHT);

    // 2. Get DOM document root (needed for both crop and mache-id resolution)
    const { root } = await chrome.debugger.sendCommand(
      { tabId }, 'DOM.getDocument', { depth: 0 }
    );

    // 3. Build screenshot clip — crop to target element if magnifying glass mode
    let clip;
    if (targetMacheId) {
      try {
        const { nodeId } = await chrome.debugger.sendCommand(
          { tabId }, 'DOM.querySelector',
          { nodeId: root.nodeId, selector: `[data-mache-id="${targetMacheId}"]` }
        );
        if (nodeId) {
          const { model } = await chrome.debugger.sendCommand(
            { tabId }, 'DOM.getBoxModel', { nodeId }
          );
          // model.border = [x1,y1, x2,y1, x2,y2, x1,y2] (quad points)
          const bx = model.border[0], by = model.border[1];
          const bw = model.border[2] - model.border[0];
          const bh = model.border[5] - model.border[1];
          const pad = 50;
          const cx = Math.max(0, bx - pad);
          const cy = Math.max(0, by - pad);
          const cw = Math.min(captureWidth - cx, bw + 2 * pad);
          const ch = Math.min(captureHeight - cy, bh + 2 * pad);
          clip = {
            x: cx, y: cy, width: cw, height: ch,
            scale: Math.min(1, CDP_TARGET_WIDTH / cw)
          };
          console.log(`X-Ray: Magnifying glass — cropping to ${Math.round(cw)}x${Math.round(ch)}px at (${Math.round(cx)},${Math.round(cy)})`);
        }
      } catch (e) {
        console.warn('X-Ray: Failed to get bounding box for', targetMacheId, '— falling back to full page:', e.message);
      }
    }
    if (!clip) {
      const scale = Math.min(1, CDP_TARGET_WIDTH / captureWidth);
      clip = { x: 0, y: 0, width: captureWidth, height: captureHeight, scale };
    }

    // 4. Capture screenshot with clip
    const { data: screenshot } = await chrome.debugger.sendCommand(
      { tabId }, 'Page.captureScreenshot', {
        format: 'jpeg',
        quality: CDP_JPEG_QUALITY,
        captureBeyondViewport: true,
        clip
      }
    );

    // 5. Get full AX tree (needed for per-element role/name enrichment)
    const { nodes: axNodes } = await chrome.debugger.sendCommand(
      { tabId }, 'Accessibility.getFullAXTree', {}
    );

    // 6. Find all tagged elements and batch resolve macheId → backendNodeId
    const { nodeIds } = await chrome.debugger.sendCommand(
      { tabId }, 'DOM.querySelectorAll',
      { nodeId: root.nodeId, selector: '[data-mache-id]' }
    );

    const descriptions = await Promise.all(
      nodeIds.map(nid =>
        chrome.debugger.sendCommand({ tabId }, 'DOM.describeNode', { nodeId: nid })
      )
    );
    const macheToBackend = new Map();
    for (const { node } of descriptions) {
      const attrs = node.attributes || [];
      for (let i = 0; i < attrs.length; i += 2) {
        if (attrs[i] === 'data-mache-id') {
          macheToBackend.set(attrs[i + 1], node.backendNodeId);
          break;
        }
      }
    }

    // 7. Build backendNodeId → AX node lookup, then join
    const backendToAX = new Map();
    for (const ax of axNodes) {
      if (ax.backendDOMNodeId) backendToAX.set(ax.backendDOMNodeId, ax);
    }
    const axMap = new Map();
    for (const [macheId, backendId] of macheToBackend) {
      const ax = backendToAX.get(backendId);
      if (ax) {
        axMap.set(macheId, {
          role: ax.role?.value || '',
          name: ax.name?.value || '',
          properties: (ax.properties || [])
            .filter(p => ['disabled', 'expanded', 'checked', 'selected'].includes(p.name))
            .map(p => `${p.name}=${p.value?.value}`)
        });
      }
    }

    const scaledW = Math.round(clip.width * clip.scale);
    const scaledH = Math.round(clip.height * clip.scale);
    console.log(`X-Ray: CDP capture — ${scaledW}x${scaledH}px JPEG (q${CDP_JPEG_QUALITY}), ${axMap.size} AX-mapped`,
      targetMacheId ? `[zoomed: ${targetMacheId}]` : '');
    return { screenshot, axMap };
  } finally {
    try {
      await chrome.debugger.detach({ tabId });
    } catch (_) { /* already detached */ }
  }
}

// Enrich summary lines with AX data from the CDP mapping.
// Appends AXRole and AXName to lines that have a matching mache-id.
function enrichSummaryWithAX(summary, axMap) {
  return summary.split('\n').map(line => {
    const match = line.match(/^ID:\s*(mache-\d+)/);
    if (match && axMap.has(match[1])) {
      const ax = axMap.get(match[1]);
      let enriched = line;
      if (ax.role) enriched += ` | AXRole: ${ax.role}`;
      if (ax.name) enriched += ` | AXName: "${ax.name.substring(0, 80)}"`;
      return enriched;
    }
    return line;
  }).join('\n');
}

// Capture snapshot from the given tab and send to server with tab_id.
// Flow: build registry → draw overlay → CDP screenshot (overlay visible) → remove overlay → send.
async function captureAndSend(tabId, isRescan = false, targetMacheId = null) {
  // Step 1: Get summary from content script (builds registry).
  // If the content script isn't loaded (extension reloaded while tab was open), inject it first.
  let response;
  try {
    response = await chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' });
  } catch (e) {
    console.log('X-Ray: Content script not ready, injecting into tab', tabId);
    try {
      await chrome.scripting.executeScript({
        target: { tabId },
        files: ['content.js']
      });
      // Brief delay for script to initialize, then retry.
      await new Promise(r => setTimeout(r, 200));
      response = await chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' });
    } catch (retryErr) {
      console.error('X-Ray: Content script inject/retry failed', retryErr);
      pendingSnapshots.delete(tabId);
      return;
    }
  }
  if (!response || !response.summary) {
    console.error('X-Ray: No summary from content script');
    pendingSnapshots.delete(tabId);
    return;
  }

  // Step 2: Draw Set-of-Mark overlay (bounding boxes + ID labels).
  try {
    await chrome.tabs.sendMessage(tabId, { type: 'DRAW_OVERLAY' });
  } catch (e) {
    console.warn('X-Ray: Draw overlay failed, continuing without:', e.message);
  }

  // Step 3: CDP screenshot (overlay is visible in capture) + AX enrichment.
  // For targeted rescan, pass mache_id so captureWithCDP crops to that element.
  const cdpData = await captureWithCDP(tabId, targetMacheId).catch(e => {
    console.warn('X-Ray: CDP capture failed, sending without screenshot:', e);
    return { screenshot: '', axMap: new Map() };
  });

  // Step 4: Remove overlay so the user doesn't see it.
  try {
    await chrome.tabs.sendMessage(tabId, { type: 'REMOVE_OVERLAY' });
  } catch (_) { /* overlay cleanup is best-effort */ }

  // Step 5: Enrich summary with per-element AX roles/names and send.
  const enrichedSummary = cdpData.axMap.size > 0
    ? enrichSummaryWithAX(response.summary, cdpData.axMap)
    : response.summary;

  if (ws && ws.readyState === WebSocket.OPEN) {
    const payload = {
      type: 'DOM_SNAPSHOT',
      tab_id: tabId,
      url: response.url,
      summary: enrichedSummary,
      screenshot: cdpData.screenshot
    };
    if (isRescan) payload.is_rescan = true;
    ws.send(JSON.stringify(payload));
    console.log('X-Ray: Sent DOM_SNAPSHOT for tab', tabId,
      isRescan ? '(RESCAN — cache bypass)' : '',
      cdpData.axMap.size > 0 ? `(${cdpData.axMap.size} AX-enriched)` : '(no AX)');
  } else {
    console.error('X-Ray: WebSocket not connected');
  }
}

// --- Message handlers from popup ---

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  switch (msg.type) {
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
          const dataUrl = await chrome.tabs.captureVisibleTab(null, { format: 'jpeg', quality: 85 });

          // Remove overlay.
          try { await chrome.tabs.sendMessage(tabId, { type: 'REMOVE_OVERLAY' }); } catch (_) {}
          const timestamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
          chrome.downloads.download({
            url: dataUrl,
            filename: `xray-overlay-${timestamp}.jpg`,
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
          tabId
        });
      });
      return true;
  }
});

// --- Track active tab for voice daemon ---
chrome.tabs.onActivated.addListener((activeInfo) => {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ type: 'TAB_ACTIVATED', tab_id: activeInfo.tabId }));
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
  // Invalidate stale schema (URL changed, old schema is for the previous page).
  if (schemaReadyTabs.has(tabId)) {
    schemaReadyTabs.delete(tabId);
  }

  // Not connected to server — no point snapshotting.
  if (!ws || ws.readyState !== WebSocket.OPEN) return;

  console.log('X-Ray: URL changed (manual nav), auto-snapshot for tab', tabId, '→', url);
  pendingSnapshots.add(tabId);
  captureAndSend(tabId);
});

// Clean up tracking when tabs close.
chrome.tabs.onRemoved.addListener((tabId) => {
  lastKnownUrls.delete(tabId);
  schemaReadyTabs.delete(tabId);
  pendingSnapshots.delete(tabId);
  pendingIntents.delete(tabId);
  gotoInFlight.delete(tabId);
});
