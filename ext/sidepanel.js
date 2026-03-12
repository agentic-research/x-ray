const logEl = document.getElementById('log');
const emptyEl = document.getElementById('empty');
const wsDot = document.getElementById('ws-dot');
const autoScrollBtn = document.getElementById('auto-scroll-btn');
const clearBtn = document.getElementById('clear-btn');
const filterAllBtn = document.getElementById('filter-all');
const filterActionsBtn = document.getElementById('filter-actions');

const MAX_ENTRIES = 500;
let autoScroll = true;
let filter = 'all'; // 'all' | 'actions'
const buffer = [];

// Icon -> type mapping for color coding.
const ICON_TYPE = {
  'S': 'STATUS', 'C': 'STATUS', '!': 'ERROR',
  'A': 'EXECUTE', 'G': 'GOTO', 'V': 'SCROLL',
  'R': 'SCHEMA', '--': 'SYS',
  'U': 'VOICE', 'T': 'MODEL'
};

function connectPort() {
  const port = chrome.runtime.connect({ name: 'sidepanel' });
  port.onMessage.addListener((msg) => {
    if (msg.type === 'AGENT_LOG') {
      addEntry(msg);
    } else if (msg.type === 'WS_STATUS') {
      wsDot.classList.toggle('connected', msg.connected);
    }
  });
  port.onDisconnect.addListener(() => {
    wsDot.classList.remove('connected');
    // Reconnect after 1s (service worker may have restarted).
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

  if (filter === 'actions' && !['EXECUTE', 'GOTO', 'SCROLL'].includes(entry.type)) return;

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
  // Prune DOM.
  while (logEl.children.length > MAX_ENTRIES) logEl.removeChild(logEl.firstChild);
  if (autoScroll) logEl.scrollTop = logEl.scrollHeight;
}

function escapeHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function rerender() {
  logEl.innerHTML = '';
  const filtered = filter === 'all' ? buffer : buffer.filter(e => ['EXECUTE', 'GOTO', 'SCROLL'].includes(e.type));
  for (const entry of filtered) appendDOM(entry);
  if (logEl.children.length === 0) {
    const e = document.createElement('div');
    e.id = 'empty';
    e.textContent = 'No matching entries';
    logEl.appendChild(e);
  }
}

// Auto-scroll: pause when user scrolls up, resume at bottom.
logEl.addEventListener('scroll', () => {
  const atBottom = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 30;
  autoScroll = atBottom;
  autoScrollBtn.classList.toggle('active', autoScroll);
});

autoScrollBtn.addEventListener('click', () => {
  autoScroll = !autoScroll;
  autoScrollBtn.classList.toggle('active', autoScroll);
  if (autoScroll) logEl.scrollTop = logEl.scrollHeight;
});

clearBtn.addEventListener('click', () => {
  buffer.length = 0;
  rerender();
});

filterAllBtn.addEventListener('click', () => {
  filter = 'all';
  filterAllBtn.classList.add('active');
  filterActionsBtn.classList.remove('active');
  rerender();
});

filterActionsBtn.addEventListener('click', () => {
  filter = 'actions';
  filterActionsBtn.classList.add('active');
  filterAllBtn.classList.remove('active');
  rerender();
});

// Get initial WS status.
chrome.runtime.sendMessage({ type: 'CHECK_SCHEMA' }, (resp) => {
  if (resp && resp.wsConnected) wsDot.classList.add('connected');
});

connectPort();
