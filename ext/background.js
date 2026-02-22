// background.js - Service worker for X-Ray extension

let ws = null;
let reconnectTimer = null;

function connectWebSocket() {
  if (ws && ws.readyState === WebSocket.OPEN) return;

  ws = new WebSocket('ws://localhost:8080/ws');

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
      case 'EXECUTE_ACTION':
        chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
          if (tabs[0]) {
            chrome.tabs.sendMessage(tabs[0].id, {
              type: 'EXECUTE_ACTION',
              mache_id: msg.mache_id,
              action: msg.action
            });
          }
        });
        break;

      case 'SCHEMA_READY':
        console.log('X-Ray: Schema ready', msg.schema);
        break;

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

// Capture snapshot from the active tab and send to server.
// Only called explicitly — NOT on reconnect.
function captureAndSend(tabId) {
  chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' }, (response) => {
    if (chrome.runtime.lastError) {
      console.error('X-Ray: Content script error', chrome.runtime.lastError);
      return;
    }
    if (!response || !response.summary) {
      console.error('X-Ray: No summary from content script');
      return;
    }

    // captureVisibleTab only available in service worker
    chrome.tabs.captureVisibleTab(null, { format: 'png' }, (dataUrl) => {
      if (chrome.runtime.lastError) {
        console.error('X-Ray: Screenshot error', chrome.runtime.lastError);
        return;
      }

      // dataUrl is "data:image/png;base64,<base64data>"
      const base64Data = dataUrl.split(',')[1];

      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({
          type: 'DOM_SNAPSHOT',
          url: response.url,
          summary: response.summary,
          screenshot: base64Data
        }));
        console.log('X-Ray: Sent DOM_SNAPSHOT to server');
      } else {
        console.error('X-Ray: WebSocket not connected');
      }
    });
  });
}

// Manual trigger: extension icon click
chrome.action.onClicked.addListener((tab) => {
  captureAndSend(tab.id);
});

// Connect on startup
connectWebSocket();
