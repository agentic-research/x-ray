const snapshotBtn = document.getElementById('snapshot-btn');
const intentInput = document.getElementById('intent-input');
const intentBtn = document.getElementById('intent-btn');
const micBtn = document.getElementById('mic-btn');
const killBtn = document.getElementById('kill-btn');
const sessionDot = document.getElementById('session-dot');
const statusEl = document.getElementById('status');

let schemaReady = false;
let schemaPollTimer = null;

function setIntentEnabled(enabled) {
  intentInput.disabled = !enabled;
  intentBtn.disabled = !enabled;
  if (enabled) {
    intentInput.placeholder = 'Type a command...';
    intentInput.focus();
  } else {
    intentInput.placeholder = 'Waiting for schema...';
  }
}

function pollForSchema() {
  if (schemaPollTimer) return;
  schemaPollTimer = setInterval(() => {
    chrome.runtime.sendMessage({ type: 'CHECK_SCHEMA' }, (resp) => {
      if (chrome.runtime.lastError) return;
      if (resp?.hasSchema) {
        clearInterval(schemaPollTimer);
        schemaPollTimer = null;
        schemaReady = true;
        setIntentEnabled(true);
        statusEl.textContent = 'Ready — type a command or use voice';
        statusEl.className = 'connected';
      }
    });
  }, 1000);
}

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
      schemaReady = true;
      setIntentEnabled(true);
      statusEl.textContent = 'Ready — type a command or use voice';
      statusEl.className = 'connected';
    } else {
      // Disable input until schema arrives.
      setIntentEnabled(false);
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
          pollForSchema();
        }
        snapshotBtn.textContent = 'Snapshot';
        snapshotBtn.disabled = false;
      });
    }
  });
});

snapshotBtn.addEventListener('click', () => {
  snapshotBtn.textContent = 'Capturing...';
  snapshotBtn.disabled = true;
  schemaReady = false;
  setIntentEnabled(false);
  chrome.runtime.sendMessage({ type: 'TRIGGER_SNAPSHOT' }, (resp) => {
    if (chrome.runtime.lastError || !resp?.ok) {
      statusEl.textContent = resp?.error || 'Snapshot failed';
      statusEl.className = 'error';
    } else {
      statusEl.textContent = 'Generating schema...';
      statusEl.className = '';
      pollForSchema();
    }
    snapshotBtn.textContent = 'Snapshot';
    snapshotBtn.disabled = false;
  });
});

function sendIntent() {
  const intent = intentInput.value.trim();
  if (!intent) return;
  intentBtn.disabled = true;
  intentInput.disabled = true;
  statusEl.textContent = 'Navigating...';
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
      statusEl.className = 'connected';
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
