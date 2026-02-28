const snapshotBtn = document.getElementById('snapshot-btn');
const overlayBtn = document.getElementById('overlay-btn');
const exportBtn = document.getElementById('export-btn');
const intentInput = document.getElementById('intent-input');
const intentBtn = document.getElementById('intent-btn');
const statusEl = document.getElementById('status');

// Check connection and auto-snapshot if no schema exists for this tab.
chrome.runtime.sendMessage({ type: 'CHECK_SCHEMA' }, (resp) => {
  if (chrome.runtime.lastError || !resp) {
    statusEl.textContent = 'Not connected to agentd';
    statusEl.className = 'error';
    return;
  }
  if (!resp.wsConnected) {
    statusEl.textContent = 'Not connected to agentd';
    statusEl.className = 'error';
    return;
  }
  // Check if current tab is a restricted URL (chrome://, about:, etc.)
  chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
    const url = tabs[0]?.url || '';
    const restricted = /^(chrome|about|edge|brave):\/\//.test(url);
    if (restricted) {
      intentInput.placeholder = 'Type a URL to navigate...';
      statusEl.textContent = 'Navigate to a website first';
      statusEl.className = '';
      return;
    }
    if (resp?.hasSchema) {
      intentInput.placeholder = 'Type a command...';
      statusEl.textContent = 'Ready';
      statusEl.className = 'connected';
    } else if (resp?.pending) {
      intentInput.placeholder = 'Type now — runs when ready...';
      snapshotBtn.textContent = 'Capturing...';
      snapshotBtn.disabled = true;
      statusEl.textContent = 'Generating schema...';
      statusEl.className = '';
    } else {
      intentInput.placeholder = 'Type now — runs when ready...';
      // Auto-trigger snapshot.
      snapshotBtn.textContent = 'Capturing...';
      snapshotBtn.disabled = true;
      statusEl.textContent = 'Auto-capturing page...';
      statusEl.className = '';
      chrome.runtime.sendMessage({ type: 'TRIGGER_SNAPSHOT' }, (snapResp) => {
        if (chrome.runtime.lastError || !snapResp?.ok) {
          statusEl.textContent = snapResp?.error || 'Snapshot failed';
          statusEl.className = 'error';
        } else {
          statusEl.textContent = 'Generating schema...';
          statusEl.className = '';
        }
        snapshotBtn.textContent = 'Snapshot';
        snapshotBtn.disabled = false;
      });
    }
  });
});

// Focus input immediately so user can start typing.
intentInput.focus();

// Listen for SCHEMA_READY broadcast from background.
chrome.runtime.onMessage.addListener((msg) => {
  if (msg.type === 'SCHEMA_READY_EVENT') {
    intentInput.placeholder = 'Type a command...';
    statusEl.textContent = 'Ready';
    statusEl.className = 'connected';
    snapshotBtn.textContent = 'Snapshot';
    snapshotBtn.disabled = false;
  }
});

snapshotBtn.addEventListener('click', () => {
  snapshotBtn.textContent = 'Capturing...';
  snapshotBtn.disabled = true;
  intentInput.placeholder = 'Type now — runs when ready...';
  chrome.runtime.sendMessage({ type: 'TRIGGER_SNAPSHOT' }, (resp) => {
    if (chrome.runtime.lastError || !resp?.ok) {
      statusEl.textContent = resp?.error || 'Snapshot failed';
      statusEl.className = 'error';
    } else {
      statusEl.textContent = 'Generating schema...';
      statusEl.className = '';
    }
    snapshotBtn.textContent = 'Snapshot';
    snapshotBtn.disabled = false;
  });
});

overlayBtn.addEventListener('click', () => {
  overlayBtn.disabled = true;
  chrome.runtime.sendMessage({ type: 'TOGGLE_OVERLAY' }, (resp) => {
    if (chrome.runtime.lastError || !resp?.ok) {
      statusEl.textContent = resp?.error || 'Overlay toggle failed';
      statusEl.className = 'error';
    } else {
      overlayBtn.textContent = resp.visible ? 'Hide' : 'Overlay';
      statusEl.textContent = resp.visible ? 'Overlay visible' : 'Overlay hidden';
      statusEl.className = 'connected';
    }
    overlayBtn.disabled = false;
  });
});

exportBtn.addEventListener('click', () => {
  exportBtn.textContent = 'Exporting...';
  exportBtn.disabled = true;
  chrome.runtime.sendMessage({ type: 'EXPORT_OVERLAY' }, (resp) => {
    if (chrome.runtime.lastError || !resp?.ok) {
      statusEl.textContent = resp?.error || 'Export failed';
      statusEl.className = 'error';
    } else {
      statusEl.textContent = 'Overlay PNG saved';
      statusEl.className = 'connected';
    }
    exportBtn.textContent = 'Export';
    exportBtn.disabled = false;
  });
});

function sendIntent() {
  const intent = intentInput.value.trim();
  if (!intent) return;
  intentBtn.disabled = true;
  intentInput.disabled = true;
  statusEl.textContent = 'Sending...';
  statusEl.className = '';
  chrome.runtime.sendMessage({ type: 'SEND_INTENT', intent }, (resp) => {
    intentBtn.disabled = false;
    intentInput.disabled = false;
    intentInput.focus();
    if (chrome.runtime.lastError || !resp?.ok) {
      statusEl.textContent = resp?.error || 'Intent failed';
      statusEl.className = 'error';
    } else {
      statusEl.textContent = resp.message || 'Intent sent';
      statusEl.className = resp.message?.startsWith('Queued') ? '' : 'connected';
      intentInput.value = '';
    }
  });
}

intentBtn.addEventListener('click', sendIntent);
intentInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') sendIntent();
});
