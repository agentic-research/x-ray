// voice-sidebar.js — Gemini Live voice client embedded in the X-Ray side panel.
// Connects to /voice WebSocket, captures mic via AudioWorklet, plays back audio.
// Transcriptions are rendered as log entries in the LOG panel.

const micBtn = document.getElementById('mic-btn');
const voiceStatusEl = document.getElementById('voice-status');

const INPUT_RATE = 16000;
const OUTPUT_RATE = 24000;

let voiceWs = null;
let audioCtx = null;
let mediaStream = null;
let micCtx = null;
let processor = null;
let voiceConnected = false;
let sessionReady = false;
let recording = false;

// --- Audio playback ---
let playQueue = [];
let playing = false;
let currentSource = null;

function enqueueAudio(pcmBytes) {
  playQueue.push(pcmBytes);
  if (!playing) drainQueue();
}

function drainQueue() {
  if (playQueue.length === 0) { playing = false; currentSource = null; return; }
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
  currentSource = src;
  src.start();
}

function flushPlayback() {
  playQueue.length = 0;
  if (currentSource) {
    try { currentSource.stop(); } catch (_) {}
    currentSource = null;
  }
  playing = false;
}

// --- Mic capture (AudioWorklet + 16kHz) ---
async function acquireMic() {
  mediaStream = await navigator.mediaDevices.getUserMedia({ audio: {
    channelCount: 1, echoCancellation: true, noiseSuppression: true,
  }});
  micCtx = new AudioContext({ sampleRate: INPUT_RATE });
  const source = micCtx.createMediaStreamSource(mediaStream);

  const workletBlob = new Blob([`
    class PcmProcessor extends AudioWorkletProcessor {
      constructor() {
        super();
        this.buf = new Float32Array(512);
        this.pos = 0;
      }
      process(inputs) {
        const ch = inputs[0]?.[0];
        if (!ch) return true;
        for (let i = 0; i < ch.length; i++) {
          this.buf[this.pos++] = ch[i];
          if (this.pos >= 512) {
            this.port.postMessage(this.buf.slice());
            this.pos = 0;
          }
        }
        return true;
      }
    }
    registerProcessor('pcm-processor', PcmProcessor);
  `], { type: 'application/javascript' });

  await micCtx.audioWorklet.addModule(URL.createObjectURL(workletBlob));
  processor = new AudioWorkletNode(micCtx, 'pcm-processor');
  processor.port.onmessage = (e) => {
    if (!recording || !voiceWs || voiceWs.readyState !== WebSocket.OPEN) return;
    const floats = e.data;
    const pcm = new Int16Array(floats.length);
    for (let i = 0; i < floats.length; i++) {
      const s = Math.max(-1, Math.min(1, floats[i]));
      pcm[i] = s < 0 ? s * 0x8000 : s * 0x7FFF;
    }
    voiceWs.send(pcm.buffer);
  };
  source.connect(processor);
}

function releaseMic() {
  if (processor) { processor.disconnect(); processor = null; }
  if (micCtx) { micCtx.close(); micCtx = null; }
  if (mediaStream) { mediaStream.getTracks().forEach(t => t.stop()); mediaStream = null; }
}

// --- Voice log entries (reuse sidepanel.js addEntry if available) ---
function voiceLog(icon, type, text) {
  // addEntry is defined in sidepanel.js which loads before us.
  if (typeof addEntry === 'function') {
    addEntry({ icon, text, ts: Date.now(), _type: type });
  }
}

// --- WebSocket ---
function connectVoice() {
  // Get active tab ID to bind voice to the right schema.
  chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
    const tabId = tabs[0]?.id || 0;
    const wsUrl = `ws://localhost:8080/voice?tab=${tabId}`;
    voiceStatusEl.textContent = 'connecting...';
    voiceWs = new WebSocket(wsUrl);
    voiceWs.binaryType = 'arraybuffer';

    voiceWs.onopen = async () => {
      voiceConnected = true;
      voiceStatusEl.textContent = 'waiting...';
      try {
        await acquireMic();
      } catch (e) {
        voiceStatusEl.textContent = 'mic denied';
        console.error('Mic access denied:', e);
      }
    };

    voiceWs.onmessage = (e) => {
      if (e.data instanceof ArrayBuffer) {
        enqueueAudio(new Uint8Array(e.data));
        return;
      }
      const msg = JSON.parse(e.data);
      switch (msg.type) {
        case 'ready':
          sessionReady = true;
          micBtn.disabled = false;
          voiceStatusEl.textContent = 'ready';
          voiceLog('V', 'VOICE', 'Voice session ready');
          break;
        case 'input_transcription':
          voiceLog('U', 'VOICE', 'You: ' + msg.text);
          break;
        case 'output_transcription':
          voiceLog('T', 'MODEL', 'Talker: ' + msg.text);
          break;
        case 'model_text':
          voiceLog('T', 'MODEL', 'Talker: ' + msg.text);
          break;
        case 'EXECUTE_ACTION':
          voiceLog('A', 'EXECUTE', `${msg.action} ${msg.mache_id}`);
          break;
        case 'interrupted':
          flushPlayback();
          break;
        case 'generation_complete':
        case 'turn_complete':
          if (sessionReady && !recording) voiceStatusEl.textContent = 'ready';
          break;
        case 'error':
          voiceLog('!', 'ERROR', msg.text);
          break;
      }
    };

    voiceWs.onclose = () => {
      voiceConnected = false;
      sessionReady = false;
      recording = false;
      micBtn.disabled = true;
      micBtn.classList.remove('recording');
      voiceStatusEl.textContent = '';
      releaseMic();
      // Auto-reconnect after 3s.
      setTimeout(connectVoice, 3000);
    };

    voiceWs.onerror = () => {
      voiceStatusEl.textContent = 'no server';
    };
  });
}

// --- Push-to-talk ---
function startRecording() {
  if (!sessionReady) return;
  recording = true;
  micBtn.classList.add('recording');
  voiceStatusEl.textContent = 'listening...';
}

function stopRecording() {
  if (!recording) return;
  recording = false;
  micBtn.classList.remove('recording');
  voiceStatusEl.textContent = 'processing...';
  if (voiceWs && voiceWs.readyState === WebSocket.OPEN) {
    voiceWs.send(JSON.stringify({ type: 'mic_stop' }));
  }
}

// Mouse
micBtn.addEventListener('mousedown', startRecording);
micBtn.addEventListener('mouseup', stopRecording);
micBtn.addEventListener('mouseleave', stopRecording);

// Touch
micBtn.addEventListener('touchstart', (e) => { e.preventDefault(); startRecording(); });
micBtn.addEventListener('touchend', (e) => { e.preventDefault(); stopRecording(); });
micBtn.addEventListener('touchcancel', stopRecording);

// Spacebar PTT (only when not focused on an input)
document.addEventListener('keydown', (e) => {
  if (e.code === 'Space' && !e.repeat && sessionReady && !['INPUT', 'TEXTAREA'].includes(document.activeElement?.tagName)) {
    e.preventDefault();
    startRecording();
  }
});
document.addEventListener('keyup', (e) => {
  if (e.code === 'Space' && !['INPUT', 'TEXTAREA'].includes(document.activeElement?.tagName)) {
    e.preventDefault();
    stopRecording();
  }
});

// --- Auto-connect ---
connectVoice();
