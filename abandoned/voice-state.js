// voice-state.js — Pure voice session state machine (reducer pattern).
//
// State transitions are pure functions: (state, event) → { state, effects[] }
// Side effects are returned as descriptors — background.js executes them.
// This separation makes every transition unit-testable without Chrome APIs.

const initialState = {
  sessionTabId: null,
  sessionReady: false,
  micActive: false,
  pendingAutoMic: false,
};

function voiceReducer(state, event) {
  switch (event.type) {
    case 'TOGGLE_MIC': {
      if (state.sessionTabId === null) {
        // No session — start one. Mic auto-enables once ready.
        return {
          state: {
            ...state,
            sessionTabId: event.tabId,
            sessionReady: false,
            pendingAutoMic: true,
          },
          effects: [
            { type: 'LOG', message: `TOGGLE_MIC: starting session for tab ${event.tabId}` },
            { type: 'CREATE_OFFSCREEN', tabId: event.tabId },
            { type: 'UPDATE_BADGE' },
          ],
        };
      }
      if (!state.sessionReady) {
        // Session is connecting — ignore toggle.
        return {
          state,
          effects: [{ type: 'LOG', message: 'TOGGLE_MIC: session not ready' }],
        };
      }
      // Session is ready — toggle mic.
      const newMic = !state.micActive;
      return {
        state: { ...state, micActive: newMic },
        effects: [
          { type: 'LOG', message: `TOGGLE_MIC: mic ${state.micActive} → ${newMic}` },
          { type: 'SET_MIC', on: newMic },
          { type: 'UPDATE_BADGE' },
        ],
      };
    }

    case 'VOICE_READY': {
      const effects = [{ type: 'UPDATE_BADGE' }];
      const newState = { ...state, sessionReady: true };
      if (state.pendingAutoMic) {
        newState.pendingAutoMic = false;
        newState.micActive = true;
        effects.unshift(
          { type: 'LOG', message: 'pendingAutoMic → enabling mic' },
          { type: 'SET_MIC', on: true },
        );
      }
      return { state: newState, effects };
    }

    case 'VOICE_DISCONNECTED':
    case 'VOICE_ERROR': {
      return {
        state: { ...initialState },
        effects: [{ type: 'UPDATE_BADGE' }],
      };
    }

    case 'KILL_SESSION': {
      return {
        state: { ...initialState },
        effects: [
          { type: 'DESTROY_OFFSCREEN' },
          { type: 'UPDATE_BADGE' },
        ],
      };
    }

    case 'MIC_GRANTED': {
      // mic-setup.html reports permission granted — retry offscreen creation.
      // sessionTabId stays set (was set by the original TOGGLE_MIC).
      if (state.sessionTabId !== null) {
        return {
          state,
          effects: [
            { type: 'LOG', message: 'mic permission granted, restarting session' },
            { type: 'CREATE_OFFSCREEN', tabId: state.sessionTabId },
          ],
        };
      }
      return { state, effects: [] };
    }

    default:
      return { state, effects: [] };
  }
}

// Derived state — what popup.js and badge logic need.
function getComputedState(state) {
  return {
    session: state.sessionTabId !== null,
    sessionConnecting: state.sessionTabId !== null && !state.sessionReady,
    sessionTabId: state.sessionTabId,
    mic: state.micActive,
  };
}

// Export for both importScripts (service worker) and CommonJS/ESM (tests).
if (typeof globalThis !== 'undefined') {
  globalThis.voiceInitialState = initialState;
  globalThis.voiceReducer = voiceReducer;
  globalThis.getComputedState = getComputedState;
}
if (typeof module !== 'undefined' && module.exports) {
  module.exports = { initialState, voiceReducer, getComputedState };
}
