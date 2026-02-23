// background.js - Service worker for X-Ray extension
//
// Voice session lifecycle:
//   1. User clicks Snapshot → schema generates on server
//   2. SCHEMA_READY arrives → offscreen doc auto-created, Gemini Live session starts (no mic yet)
//   3. User toggles Mic ON → audio streams to Gemini
//   4. User toggles Mic OFF → audio stops, session stays alive
//   5. User clicks kill (power) button → session torn down

const DEFAULT_WS_URL = 'ws://localhost:8080/ws';

let ws = null;
let reconnectTimer = null;
let wsUrl = DEFAULT_WS_URL;

// Voice state: session can be alive while mic is off.
let sessionTabId = null;    // Tab ID with an active voice session (null = no session).
let sessionReady = false;   // True once Gemini Live setup is complete.
let micActive = false;      // True when mic is streaming audio.
let pendingAutoMic = false; // When true, auto-enable mic once session is ready (one-click flow).
const schemaReadyTabs = new Set();
const pendingIntents = new Map(); // tabId → intent string (queued before schema ready)

// Keep-alive: Chrome MV3 service workers die after ~30s of inactivity.
chrome.alarms.create('keepalive', { periodInMinutes: 0.4 }); // ~24s
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === 'keepalive' && ws && ws.readyState === WebSocket.OPEN) {
    // No-op — just being awake keeps the service worker alive.
  }
});

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
    if (reconnectTimer) {
      clearInterval(reconnectTimer);
      reconnectTimer = null;
    }
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
        const targetTab = msg.tab_id;
        const direction = msg.direction || 'down';
        if (targetTab) {
          chrome.tabs.sendMessage(targetTab, { type: 'SCROLL', direction }, (response) => {
            if (chrome.runtime.lastError || !response) {
              console.error('X-Ray: Scroll failed', chrome.runtime.lastError);
              return;
            }
            // Send updated summary back to server
            if (ws && ws.readyState === WebSocket.OPEN) {
              ws.send(JSON.stringify({
                type: 'DOM_UPDATE',
                tab_id: targetTab,
                summary: response.summary,
                url: response.url
              }));
              console.log('X-Ray: Scroll', direction, '— sent DOM_UPDATE for tab', targetTab);
            }
          });
        }
        break;
      }

      case 'SCHEMA_READY': {
        const tabId = msg.tab_id;
        console.log('X-Ray: Schema ready (tab', tabId, ')');
        if (tabId) schemaReadyTabs.add(tabId);

        // Flush any queued intent for this tab.
        if (tabId && pendingIntents.has(tabId) && ws && ws.readyState === WebSocket.OPEN) {
          const intent = pendingIntents.get(tabId);
          pendingIntents.delete(tabId);
          console.log('X-Ray: Flushing queued intent for tab', tabId, ':', intent);
          ws.send(JSON.stringify({ type: 'NAVIGATE', tab_id: tabId, intent }));
        }

        // Auto-start voice session when schema arrives (if no session exists yet).
        if (tabId && sessionTabId === null) {
          console.log('X-Ray: Auto-starting voice session for tab', tabId);
          startSession(tabId);
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

// --- CDP capture: full-page screenshot + accessibility tree ---

const CDP_MAX_HEIGHT = 16384; // Cap to avoid enormous images on infinite-scroll pages

// Single CDP session that captures full-page screenshot + AX tree + mache-id mapping.
// Falls back to viewport-only screenshot (captureVisibleTab) if debugger attach fails.
async function captureWithCDP(tabId) {
  try {
    await chrome.debugger.attach({ tabId }, '1.3');
  } catch (e) {
    console.warn('X-Ray: debugger attach failed, falling back to viewport screenshot:', e.message);
    const dataUrl = await chrome.tabs.captureVisibleTab(null, { format: 'png' });
    return { screenshot: dataUrl.split(',')[1], axMap: new Map(), axSummary: '' };
  }

  try {
    // 1. Full-page screenshot via CDP
    const { cssContentSize } = await chrome.debugger.sendCommand(
      { tabId }, 'Page.getLayoutMetrics', {}
    );
    const captureWidth = cssContentSize.width;
    const captureHeight = Math.min(cssContentSize.height, CDP_MAX_HEIGHT);

    const { data: screenshot } = await chrome.debugger.sendCommand(
      { tabId }, 'Page.captureScreenshot', {
        format: 'png',
        captureBeyondViewport: true,
        clip: { x: 0, y: 0, width: captureWidth, height: captureHeight, scale: 1 }
      }
    );

    // 2. Get full AX tree
    const { nodes: axNodes } = await chrome.debugger.sendCommand(
      { tabId }, 'Accessibility.getFullAXTree', {}
    );

    // 3. Get DOM document root
    const { root } = await chrome.debugger.sendCommand(
      { tabId }, 'DOM.getDocument', { depth: 0 }
    );

    // 4. Find all data-mache-id elements via CDP querySelectorAll
    const { nodeIds } = await chrome.debugger.sendCommand(
      { tabId }, 'DOM.querySelectorAll',
      { nodeId: root.nodeId, selector: '[data-mache-id]' }
    );

    // 5. Batch DOM.describeNode calls with Promise.all for concurrency
    const descriptions = await Promise.all(
      nodeIds.map(nid =>
        chrome.debugger.sendCommand({ tabId }, 'DOM.describeNode', { nodeId: nid })
      )
    );

    // 6. Build macheId → backendNodeId mapping
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

    // 7. Build backendNodeId → AX node lookup
    const backendToAX = new Map();
    for (const ax of axNodes) {
      if (ax.backendDOMNodeId) backendToAX.set(ax.backendDOMNodeId, ax);
    }

    // 8. Join: macheId → {role, name, properties}
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

    // 9. Build compact AX summary for the server
    const axSummary = formatAXTree(axNodes);

    console.log(`X-Ray: CDP capture — full-page ${captureWidth}x${captureHeight}px, ${axNodes.length} AX nodes, ${axMap.size} mapped`);
    return { screenshot, axMap, axSummary };
  } finally {
    try {
      await chrome.debugger.detach({ tabId });
    } catch (_) { /* already detached */ }
  }
}

// Format the AX tree into a compact text representation for the Cartographer.
// Only includes nodes with meaningful roles (skips generic/none/ignored).
function formatAXTree(axNodes) {
  const skipRoles = new Set(['none', 'generic', 'InlineTextBox', 'StaticText', 'ignored']);
  const lines = [];
  for (const ax of axNodes) {
    const role = ax.role?.value || '';
    if (skipRoles.has(role) || !role) continue;
    const name = ax.name?.value || '';
    const props = (ax.properties || [])
      .filter(p => p.value?.value === true || p.value?.value === 'true')
      .map(p => p.name)
      .join(',');
    let line = `${role}`;
    if (name) line += `: "${name.substring(0, 80)}"`;
    if (props) line += ` [${props}]`;
    lines.push(line);
    if (lines.length >= 200) break; // Cap for prompt size
  }
  return lines.join('\n');
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
// Uses a single CDP session for full-page screenshot + AX tree enrichment.
async function captureAndSend(tabId) {
  // Get summary from content script
  let response;
  try {
    response = await chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' });
  } catch (e) {
    console.error('X-Ray: Content script error', e);
    return;
  }
  if (!response || !response.summary) {
    console.error('X-Ray: No summary from content script');
    return;
  }

  // Single CDP session: full-page screenshot + AX tree + mache-id mapping
  const cdpData = await captureWithCDP(tabId).catch(e => {
    console.warn('X-Ray: CDP capture failed, sending without screenshot:', e);
    return { screenshot: '', axMap: new Map(), axSummary: '' };
  });

  // Enrich summary with AX data
  const enrichedSummary = cdpData.axMap.size > 0
    ? enrichSummaryWithAX(response.summary, cdpData.axMap)
    : response.summary;

  if (ws && ws.readyState === WebSocket.OPEN) {
    const msg = {
      type: 'DOM_SNAPSHOT',
      tab_id: tabId,
      url: response.url,
      summary: enrichedSummary,
      screenshot: cdpData.screenshot
    };
    if (cdpData.axSummary) {
      msg.ax_tree = cdpData.axSummary;
    }
    ws.send(JSON.stringify(msg));
    console.log('X-Ray: Sent DOM_SNAPSHOT for tab', tabId,
      cdpData.axMap.size > 0 ? `(${cdpData.axMap.size} AX-enriched elements)` : '(no AX data)');
  } else {
    console.error('X-Ray: WebSocket not connected');
  }
}

// --- Voice session lifecycle ---

async function hasOffscreen() {
  const contexts = await chrome.runtime.getContexts({
    contextTypes: ['OFFSCREEN_DOCUMENT']
  });
  return contexts.length > 0;
}

// Start a voice session for the given tab. Mic starts OFF — user toggles it.
async function startSession(tabId) {
  // Tear down any existing session first.
  if (await hasOffscreen()) {
    try { chrome.runtime.sendMessage({ type: 'VOICE_STOP' }); } catch (_) {}
    await chrome.offscreen.closeDocument();
  }

  sessionTabId = tabId;
  sessionReady = false;
  micActive = false;

  await chrome.offscreen.createDocument({
    url: 'offscreen.html#tab=' + tabId,
    reasons: ['USER_MEDIA', 'AUDIO_PLAYBACK'],
    justification: 'Voice navigator needs microphone and audio playback'
  });
  console.log('X-Ray: Voice session starting for tab', tabId);
  updateBadge();
}

async function killSession() {
  if (await hasOffscreen()) {
    try { chrome.runtime.sendMessage({ type: 'VOICE_STOP' }); } catch (_) {}
    setTimeout(async () => {
      try {
        if (await hasOffscreen()) await chrome.offscreen.closeDocument();
      } catch (_) {}
    }, 200);
  }
  sessionTabId = null;
  sessionReady = false;
  micActive = false;
  pendingAutoMic = false;
  updateBadge();
}

function setMic(on) {
  micActive = on;
  try {
    chrome.runtime.sendMessage({ type: on ? 'MIC_ON' : 'MIC_OFF' });
  } catch (_) {}
  updateBadge();
}

// Badge on extension icon reflects session/mic state at a glance.
function updateBadge() {
  if (micActive) {
    chrome.action.setBadgeText({ text: 'MIC' });
    chrome.action.setBadgeBackgroundColor({ color: '#22c55e' }); // green
  } else if (sessionTabId !== null && sessionReady) {
    chrome.action.setBadgeText({ text: 'ON' });
    chrome.action.setBadgeBackgroundColor({ color: '#3b82f6' }); // blue
  } else if (sessionTabId !== null) {
    chrome.action.setBadgeText({ text: '...' });
    chrome.action.setBadgeBackgroundColor({ color: '#eab308' }); // yellow
  } else {
    chrome.action.setBadgeText({ text: '' });
  }
}

function getState() {
  return {
    session: sessionTabId !== null,
    sessionConnecting: sessionTabId !== null && !sessionReady,
    sessionTabId,
    mic: micActive,
    wsConnected: ws && ws.readyState === WebSocket.OPEN,
  };
}

// --- Message handlers from popup and offscreen ---

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  switch (msg.type) {
    case 'TRIGGER_SNAPSHOT':
      chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
        if (tabs[0]) {
          pendingAutoMic = true; // One-click flow: auto-enable mic once session is ready.
          captureAndSend(tabs[0].id);
          updateBadge();
          sendResponse({ ok: true, tabId: tabs[0].id });
        } else {
          sendResponse({ ok: false, error: 'No active tab' });
        }
      });
      return true;

    case 'TOGGLE_MIC':
      if (sessionTabId === null) {
        sendResponse({ ok: false, error: 'No voice session — click Snapshot first' });
      } else if (!sessionReady) {
        sendResponse({ ok: false, error: 'Session connecting...' });
      } else {
        setMic(!micActive);
        sendResponse({ ok: true, ...getState() });
      }
      return false;

    case 'KILL_SESSION':
      killSession().then(() => {
        sendResponse(getState());
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
        sendResponse({ hasSchema: tabId ? schemaReadyTabs.has(tabId) : false, tabId });
      });
      return true;

    case 'GET_VOICE_STATE':
      sendResponse(getState());
      return false;

    case 'VOICE_STATUS':
      console.log('X-Ray: Voice status:', msg.status, msg.text);
      if (msg.status === 'ready') {
        sessionReady = true;
        // One-click flow: snapshot set pendingAutoMic, now session is ready — turn on mic.
        if (pendingAutoMic) {
          pendingAutoMic = false;
          setMic(true);
        }
      } else if (msg.status === 'disconnected' || msg.status === 'error') {
        sessionTabId = null;
        sessionReady = false;
        micActive = false;
        pendingAutoMic = false;
      }
      updateBadge();
      break;
  }
});

// --- Keyboard shortcut: Alt+V toggles mic ---

chrome.commands.onCommand.addListener((command) => {
  if (command === 'toggle-voice') {
    if (sessionTabId !== null && sessionReady) {
      setMic(!micActive);
    }
  }
});
