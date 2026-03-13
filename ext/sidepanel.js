const logEl = document.getElementById('log');
const emptyEl = document.getElementById('empty');
const wsDot = document.getElementById('ws-dot');
const snapshotBtn = document.getElementById('snapshot-btn');
const overlayBtn = document.getElementById('overlay-btn');
const exportBtn = document.getElementById('export-btn');
const cmdInput = document.getElementById('cmd-input');
const cmdBtn = document.getElementById('cmd-btn');

const MAX_ENTRIES = 500;
let autoScroll = true;
let agentConnected = false;
const buffer = [];

// Icon -> type mapping for color coding.
const ICON_TYPE = {
  'S': 'STATUS', 'C': 'STATUS', '!': 'ERROR',
  'A': 'EXECUTE', 'G': 'GOTO', 'V': 'SCROLL',
  'R': 'SCHEMA', '--': 'SYS',
  'U': 'VOICE', 'T': 'MODEL'
};

// --- Connection state: enable/disable toolbar based on agent WS ---
function setConnected(connected) {
  agentConnected = connected;
  wsDot.classList.toggle('connected', connected);
  snapshotBtn.disabled = !connected;
  overlayBtn.disabled = !connected;
  exportBtn.disabled = !connected;
  cmdInput.disabled = !connected;
  cmdBtn.disabled = !connected;
  cmdInput.placeholder = connected ? 'Type a command...' : 'Not connected';
}

function connectPort() {
  const port = chrome.runtime.connect({ name: 'sidepanel' });
  port.onMessage.addListener((msg) => {
    if (msg.type === 'AGENT_LOG') {
      addEntry(msg);
    } else if (msg.type === 'WS_STATUS') {
      setConnected(msg.connected);
    }
  });
  port.onDisconnect.addListener(() => {
    setConnected(false);
    setTimeout(connectPort, 1000);
  });
}

function addEntry(msg) {
  const entry = {
    icon: msg.icon || '?',
    text: msg.text || '',
    ts: msg.ts || Date.now(),
    type: ICON_TYPE[msg.icon] || 'SYS'
  };
  buffer.push(entry);
  while (buffer.length > MAX_ENTRIES) buffer.shift();
  appendDOM(entry);
}

function appendDOM(entry) {
  if (emptyEl) emptyEl.remove();
  const div = document.createElement('div');
  div.className = `entry entry-${entry.type}`;
  const d = new Date(entry.ts);
  const ts = [d.getHours(), d.getMinutes(), d.getSeconds()].map(n => String(n).padStart(2, '0')).join(':');
  div.innerHTML = `<span class="ts">${ts}</span><span class="icon">${entry.icon}</span><span class="text">${escapeHtml(entry.text)}</span>`;
  logEl.appendChild(div);
  while (logEl.children.length > MAX_ENTRIES) logEl.removeChild(logEl.firstChild);
  if (autoScroll) logEl.scrollTop = logEl.scrollHeight;
}

function escapeHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// Auto-scroll: pause when user scrolls up, resume at bottom.
logEl.addEventListener('scroll', () => {
  autoScroll = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 30;
});

// --- Toolbar buttons ---
snapshotBtn.addEventListener('click', () => {
  snapshotBtn.textContent = 'Capturing...';
  snapshotBtn.disabled = true;
  chrome.runtime.sendMessage({ type: 'TRIGGER_SNAPSHOT' }, (resp) => {
    snapshotBtn.textContent = 'Snapshot';
    snapshotBtn.disabled = !agentConnected;
  });
});

overlayBtn.addEventListener('click', () => {
  overlayBtn.disabled = true;
  chrome.runtime.sendMessage({ type: 'TOGGLE_OVERLAY' }, (resp) => {
    if (!chrome.runtime.lastError && resp?.ok) {
      overlayBtn.textContent = resp.visible ? 'Hide' : 'Overlay';
      overlayBtn.classList.toggle('on', resp.visible);
    }
    overlayBtn.disabled = !agentConnected;
  });
});

exportBtn.addEventListener('click', () => {
  exportBtn.textContent = 'Saving...';
  exportBtn.disabled = true;
  chrome.runtime.sendMessage({ type: 'EXPORT_OVERLAY' }, (resp) => {
    exportBtn.textContent = 'Export';
    exportBtn.disabled = !agentConnected;
  });
});

// --- Command input: send intent to background navigator ---
function sendCommand() {
  const text = cmdInput.value.trim();
  if (!text) return;
  cmdInput.disabled = true;
  cmdBtn.disabled = true;
  chrome.runtime.sendMessage({ type: 'SEND_INTENT', intent: text }, (resp) => {
    cmdInput.disabled = !agentConnected;
    cmdBtn.disabled = !agentConnected;
    cmdInput.value = '';
    cmdInput.focus();
  });
}

cmdBtn.addEventListener('click', sendCommand);
cmdInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') sendCommand();
});

// Listen for schema ready broadcasts.
chrome.runtime.onMessage.addListener((msg) => {
  if (msg.type === 'SCHEMA_READY_EVENT') {
    snapshotBtn.textContent = 'Snapshot';
    snapshotBtn.disabled = !agentConnected;
  }
});

// Get initial WS status.
chrome.runtime.sendMessage({ type: 'CHECK_SCHEMA' }, (resp) => {
  setConnected(resp?.wsConnected ?? false);
});

connectPort();
