const snapshotBtn = document.getElementById('snapshot-btn');
const exportBtn = document.getElementById('export-btn');
const intentInput = document.getElementById('intent-input');
const intentBtn = document.getElementById('intent-btn');
const micBtn = document.getElementById('mic-btn');
const killBtn = document.getElementById('kill-btn');
const sessionDot = document.getElementById('session-dot');
const statusEl = document.getElementById('status');

// Poll initial state, auto-snapshot if no schema exists for this tab.
chrome.runtime.sendMessage({ type: 'GET_VOICE_STATE' }, (state) => {
  if (chrome.runtime.lastError) return;
  updateUI(state);
  if (!state.wsConnected) {
    statusEl.textContent = 'Not connected to agentd';
    statusEl.className = 'error';
    return;
  }
  // Check if active tab already has a schema — if not, auto-snapshot.
  chrome.runtime.sendMessage({ type: 'CHECK_SCHEMA' }, (resp) => {
    if (chrome.runtime.lastError) return;
    if (resp?.hasSchema) {
      intentInput.placeholder = 'Type a command...';
      statusEl.textContent = 'Ready — type a command or use voice';
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
    statusEl.textContent = 'Ready — type a command or use voice';
    statusEl.className = 'connected';
    snapshotBtn.textContent = 'Snapshot';
    snapshotBtn.disabled = false;
  }
  if (msg.type === 'VOICE_STATE_CHANGED') {
    updateUI(msg);
    statusEl.textContent = msg.mic ? 'Mic active — speak naturally' : 'Voice session ready';
    statusEl.className = msg.mic ? 'connected' : '';
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
