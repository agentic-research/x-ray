// offscreen.js — Voice audio bridge (runs in offscreen document)
//
// Connects to ws://<host>/voice?tab=<tabId>, acquires mic,
// streams PCM to Gemini, plays back audio responses.
//
// Host is read from chrome.storage.local (same as background.js).
// Tab ID is passed via URL fragment: offscreen.html#tab=12345
//
// Mic starts OFF. Background sends MIC_ON/MIC_OFF to toggle streaming.
// Session stays alive independently of mic state.

// Immediately report that offscreen JS loaded — helps debug silent failures.
chrome.runtime.sendMessage(
  { type: 'VOICE_STATUS', status: 'loaded', text: 'offscreen.js executing' }
).catch(() => {});

const INPUT_RATE = 16000;
const OUTPUT_RATE = 24000;
const DEFAULT_WS_HOST = 'localhost:8080';

let wsHost = DEFAULT_WS_HOST;

// Read host from storage (background.js stores full URL like ws://host:port/ws).
chrome.storage.local.get({ wsUrl: `ws://${DEFAULT_WS_HOST}/ws` }, (items) => {
  try {
    const url = new URL(items.wsUrl);
    wsHost = url.host;
  } catch {
    wsHost = DEFAULT_WS_HOST;
  }
  console.log('Offscreen: using host', wsHost);
});

let ws = null;
let audioCtx = null;
let mediaStream = null;
let micCtx = null;
let processor = null;
let recording = false;   // Controlled by MIC_ON/MIC_OFF from background.
let sessionReady = false;

// --- Audio playback ---
let playQueue = [];
let playing = false;

function enqueueAudio(pcmBytes) {
  playQueue.push(pcmBytes);
  if (!playing) drainQueue();
}

function drainQueue() {
  if (playQueue.length === 0) { playing = false; return; }
  playing = true;
  const raw = playQueue.shift();
  const samples = new Int16Array(raw.buffer, raw.byteOffset, raw.byteLength / 2);
  const floats = new Float32Array(samples.length);
  for (let i = 0; i < samples.length; i++) floats[i] = samples[i] / 32768;

  if (!audioCtx) audioCtx = new AudioContext({ sampleRate: OUTPUT_RATE });
  const buf = audioCtx.createBuffer(1, floats.length, OUTPUT_RATE);
  buf.copyToChannel(floats, 0);
  const src = audioCtx.createBufferSource();
  src.buffer = buf;
  src.connect(audioCtx.destination);
  src.onended = drainQueue;
  src.start();
}

// --- Mic capture ---
async function acquireMic() {
  mediaStream = await navigator.mediaDevices.getUserMedia({ audio: {
    sampleRate: 48000, channelCount: 1, echoCancellation: true, noiseSuppression: true,
  }});
  micCtx = new AudioContext({ sampleRate: 48000 });
  const source = micCtx.createMediaStreamSource(mediaStream);

  processor = micCtx.createScriptProcessor(4096, 1, 1);
  processor.onaudioprocess = (e) => {
    if (!recording || !ws || ws.readyState !== WebSocket.OPEN) return;
    const input = e.inputBuffer.getChannelData(0);
    const ratio = 48000 / INPUT_RATE;
    const out = new Int16Array(Math.floor(input.length / ratio));
    for (let i = 0; i < out.length; i++) {
      const s = Math.max(-1, Math.min(1, input[Math.round(i * ratio)]));
      out[i] = s < 0 ? s * 0x8000 : s * 0x7FFF;
    }
    ws.send(out.buffer);
  };
  source.connect(processor);
  processor.connect(micCtx.destination);
}

function releaseMic() {
  if (processor) { processor.disconnect(); processor = null; }
  if (micCtx) { micCtx.close(); micCtx = null; }
  if (mediaStream) { mediaStream.getTracks().forEach(t => t.stop()); mediaStream = null; }
}

// --- Voice WebSocket session ---
function connectVoice(tabId) {
  const url = `ws://${wsHost}/voice?tab=${tabId}`;
  console.log('Offscreen: connecting to', url);
  reportStatus('connecting', 'Connecting to voice server...');

  ws = new WebSocket(url);
  ws.binaryType = 'arraybuffer';

  ws.onopen = async () => {
    console.log('Offscreen: connected');
    reportStatus('connected', 'Connected, waiting for Gemini session...');
    // Acquire mic hardware up front so it's ready when user toggles on.
    await acquireMic();
  };

  ws.onmessage = (e) => {
    if (e.data instanceof ArrayBuffer) {
      enqueueAudio(new Uint8Array(e.data));
      return;
    }
    const msg = JSON.parse(e.data);
    switch (msg.type) {
      case 'waiting':
        reportStatus('waiting', msg.text);
        break;
      case 'schema_ready':
        reportStatus('schema_ready', msg.text);
        break;
      case 'ready':
        sessionReady = true;
        reportStatus('ready', 'Voice session ready — toggle mic to talk');
        break;
      case 'input_transcription':
        reportStatus('transcription', 'You: ' + msg.text);
        break;
      case 'output_transcription':
        reportStatus('transcription', 'Navigator: ' + msg.text);
        break;
      case 'model_text':
        reportStatus('transcription', 'Navigator: ' + msg.text);
        break;
      case 'EXECUTE_ACTION':
        reportStatus('action', `Action: ${msg.action} on ${msg.mache_id}`);
        break;
      case 'interrupted':
        break;
      case 'turn_complete':
        break;
      case 'error':
        reportStatus('error', msg.text);
        break;
    }
  };

  ws.onclose = () => {
    console.log('Offscreen: disconnected');
    sessionReady = false;
    recording = false;
    releaseMic();
    reportStatus('disconnected', 'Voice disconnected');
  };

  ws.onerror = (err) => {
    console.error('Offscreen: WS error', err);
    reportStatus('error', 'Connection failed');
  };
}

function disconnectVoice() {
  recording = false;
  sessionReady = false;
  releaseMic();
  if (ws) { ws.close(); ws = null; }
  playQueue = [];
  playing = false;
  if (audioCtx) { audioCtx.close(); audioCtx = null; }
}

function reportStatus(status, text) {
  chrome.runtime.sendMessage({ type: 'VOICE_STATUS', status, text }).catch(() => {});
}

// --- Listen for commands from service worker ---
chrome.runtime.onMessage.addListener((msg) => {
  switch (msg.type) {
    case 'VOICE_START':
      // Tab ID sent via messaging — URL fragments are stripped in MV3 offscreen docs.
      if (msg.tabId != null) {
        connectVoice(msg.tabId);
      }
      break;
    case 'MIC_ON':
      if (sessionReady) {
        recording = true;
        console.log('Offscreen: mic ON');
      }
      break;
    case 'MIC_OFF':
      recording = false;
      // Signal server to send AudioStreamEnd to Gemini so it processes the utterance.
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'mic_stop' }));
      }
      console.log('Offscreen: mic OFF');
      break;
    case 'VOICE_STOP':
      disconnectVoice();
      break;
  }
});
