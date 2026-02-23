const snapshotBtn = document.getElementById('snapshot-btn');
const micBtn = document.getElementById('mic-btn');
const killBtn = document.getElementById('kill-btn');
const sessionDot = document.getElementById('session-dot');
const statusEl = document.getElementById('status');

// Poll initial state.
chrome.runtime.sendMessage({ type: 'GET_VOICE_STATE' }, (state) => {
  if (chrome.runtime.lastError) return;
  updateUI(state);
  if (state.wsConnected) {
    statusEl.textContent = 'Connected to agentd';
    statusEl.className = 'connected';
  } else {
    statusEl.textContent = 'Not connected to agentd';
    statusEl.className = 'error';
  }
});

snapshotBtn.addEventListener('click', () => {
  snapshotBtn.textContent = 'Capturing...';
  snapshotBtn.disabled = true;
  chrome.runtime.sendMessage({ type: 'TRIGGER_SNAPSHOT' }, (resp) => {
    if (chrome.runtime.lastError || !resp?.ok) {
      statusEl.textContent = resp?.error || 'Snapshot failed';
      statusEl.className = 'error';
    } else {
      statusEl.textContent = 'Snapshot sent (tab ' + resp.tabId + ')';
      statusEl.className = 'connected';
    }
    snapshotBtn.textContent = 'Snapshot';
    snapshotBtn.disabled = false;
  });
});

micBtn.addEventListener('click', () => {
  micBtn.disabled = true;
  chrome.runtime.sendMessage({ type: 'TOGGLE_MIC' }, (resp) => {
    if (chrome.runtime.lastError || !resp?.ok) {
      statusEl.textContent = resp?.error || 'Mic toggle failed';
      statusEl.className = 'error';
    } else {
      updateUI(resp);
      statusEl.textContent = resp.mic ? 'Mic active — speak naturally' : 'Mic muted';
      statusEl.className = resp.mic ? 'connected' : '';
    }
    micBtn.disabled = false;
  });
});

killBtn.addEventListener('click', () => {
  killBtn.disabled = true;
  chrome.runtime.sendMessage({ type: 'KILL_SESSION' }, (resp) => {
    if (chrome.runtime.lastError) return;
    updateUI(resp);
    statusEl.textContent = 'Voice session ended';
    statusEl.className = '';
    killBtn.disabled = false;
  });
});

function updateUI(state) {
  // Session dot
  if (state.session) {
    sessionDot.className = 'session-dot live';
  } else if (state.sessionConnecting) {
    sessionDot.className = 'session-dot connecting';
  } else {
    sessionDot.className = 'session-dot';
  }

  // Mic button
  if (state.mic) {
    micBtn.innerHTML = '<span id="session-dot" class="session-dot live"></span>Mic: ON';
    micBtn.classList.add('mic-on');
  } else {
    const dotClass = state.session ? 'live' : (state.sessionConnecting ? 'connecting' : '');
    micBtn.innerHTML = `<span id="session-dot" class="session-dot ${dotClass}"></span>Mic: OFF`;
    micBtn.classList.remove('mic-on');
  }
}
