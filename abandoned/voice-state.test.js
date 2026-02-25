import { describe, it, expect } from 'vitest';

// Load the module — sets up globalThis exports and module.exports.
const { initialState, voiceReducer, getComputedState } = require('./voice-state.js');

// Helper: fresh copy of initial state.
const fresh = () => ({ ...initialState });

describe('voiceReducer', () => {
  // --- TOGGLE_MIC ---

  describe('TOGGLE_MIC', () => {
    it('starts a session when no session exists', () => {
      const { state, effects } = voiceReducer(fresh(), { type: 'TOGGLE_MIC', tabId: 42 });

      expect(state.sessionTabId).toBe(42);
      expect(state.sessionReady).toBe(false);
      expect(state.pendingAutoMic).toBe(true);
      expect(state.micActive).toBe(false);

      expect(effects).toEqual([
        { type: 'LOG', message: 'TOGGLE_MIC: starting session for tab 42' },
        { type: 'CREATE_OFFSCREEN', tabId: 42 },
        { type: 'UPDATE_BADGE' },
      ]);
    });

    it('is a no-op while session is connecting', () => {
      const before = { ...fresh(), sessionTabId: 42, sessionReady: false, pendingAutoMic: true };
      const { state, effects } = voiceReducer(before, { type: 'TOGGLE_MIC', tabId: 42 });

      // State unchanged.
      expect(state).toEqual(before);
      expect(effects).toEqual([{ type: 'LOG', message: 'TOGGLE_MIC: session not ready' }]);
    });

    it('toggles mic ON when session is ready and mic is off', () => {
      const before = { ...fresh(), sessionTabId: 42, sessionReady: true, micActive: false };
      const { state, effects } = voiceReducer(before, { type: 'TOGGLE_MIC', tabId: 42 });

      expect(state.micActive).toBe(true);
      expect(effects).toContainEqual({ type: 'SET_MIC', on: true });
      expect(effects).toContainEqual({ type: 'UPDATE_BADGE' });
    });

    it('toggles mic OFF when session is ready and mic is on', () => {
      const before = { ...fresh(), sessionTabId: 42, sessionReady: true, micActive: true };
      const { state, effects } = voiceReducer(before, { type: 'TOGGLE_MIC', tabId: 42 });

      expect(state.micActive).toBe(false);
      expect(effects).toContainEqual({ type: 'SET_MIC', on: false });
    });

    it('double TOGGLE_MIC — second is no-op while connecting', () => {
      const r1 = voiceReducer(fresh(), { type: 'TOGGLE_MIC', tabId: 42 });
      const r2 = voiceReducer(r1.state, { type: 'TOGGLE_MIC', tabId: 42 });

      // Second toggle should not change state.
      expect(r2.state).toEqual(r1.state);
      expect(r2.effects).toEqual([{ type: 'LOG', message: 'TOGGLE_MIC: session not ready' }]);
    });
  });

  // --- VOICE_READY ---

  describe('VOICE_READY', () => {
    it('marks session ready and auto-enables mic with pendingAutoMic', () => {
      const before = { ...fresh(), sessionTabId: 42, pendingAutoMic: true };
      const { state, effects } = voiceReducer(before, { type: 'VOICE_READY' });

      expect(state.sessionReady).toBe(true);
      expect(state.micActive).toBe(true);
      expect(state.pendingAutoMic).toBe(false);

      expect(effects).toContainEqual({ type: 'SET_MIC', on: true });
      expect(effects).toContainEqual({ type: 'UPDATE_BADGE' });
    });

    it('marks session ready without enabling mic when no pendingAutoMic', () => {
      const before = { ...fresh(), sessionTabId: 42, pendingAutoMic: false };
      const { state, effects } = voiceReducer(before, { type: 'VOICE_READY' });

      expect(state.sessionReady).toBe(true);
      expect(state.micActive).toBe(false);
      expect(state.pendingAutoMic).toBe(false);

      // No SET_MIC effect.
      expect(effects.find(e => e.type === 'SET_MIC')).toBeUndefined();
      expect(effects).toContainEqual({ type: 'UPDATE_BADGE' });
    });
  });

  // --- VOICE_DISCONNECTED ---

  describe('VOICE_DISCONNECTED', () => {
    it('resets all state to initial', () => {
      const before = { sessionTabId: 42, sessionReady: true, micActive: true, pendingAutoMic: false };
      const { state, effects } = voiceReducer(before, { type: 'VOICE_DISCONNECTED' });

      expect(state).toEqual(initialState);
      expect(effects).toEqual([{ type: 'UPDATE_BADGE' }]);
    });
  });

  // --- VOICE_ERROR ---

  describe('VOICE_ERROR', () => {
    it('resets all state to initial', () => {
      const before = { sessionTabId: 42, sessionReady: true, micActive: true, pendingAutoMic: false };
      const { state, effects } = voiceReducer(before, { type: 'VOICE_ERROR', text: 'connection failed' });

      expect(state).toEqual(initialState);
      expect(effects).toEqual([{ type: 'UPDATE_BADGE' }]);
    });
  });

  // --- KILL_SESSION ---

  describe('KILL_SESSION', () => {
    it('resets state and emits DESTROY_OFFSCREEN', () => {
      const before = { sessionTabId: 42, sessionReady: true, micActive: true, pendingAutoMic: false };
      const { state, effects } = voiceReducer(before, { type: 'KILL_SESSION' });

      expect(state).toEqual(initialState);
      expect(effects).toEqual([
        { type: 'DESTROY_OFFSCREEN' },
        { type: 'UPDATE_BADGE' },
      ]);
    });

    it('is safe to call with no active session', () => {
      const { state, effects } = voiceReducer(fresh(), { type: 'KILL_SESSION' });

      expect(state).toEqual(initialState);
      expect(effects).toContainEqual({ type: 'DESTROY_OFFSCREEN' });
    });
  });

  // --- MIC_GRANTED ---

  describe('MIC_GRANTED', () => {
    it('restarts session when sessionTabId is set', () => {
      const before = { ...fresh(), sessionTabId: 42, pendingAutoMic: true };
      const { state, effects } = voiceReducer(before, { type: 'MIC_GRANTED' });

      // sessionTabId stays set — reducer doesn't null it.
      expect(state.sessionTabId).toBe(42);
      expect(state.pendingAutoMic).toBe(true);
      expect(effects).toContainEqual({ type: 'CREATE_OFFSCREEN', tabId: 42 });
    });

    it('is a no-op when no session exists', () => {
      const { state, effects } = voiceReducer(fresh(), { type: 'MIC_GRANTED' });

      expect(state).toEqual(initialState);
      expect(effects).toEqual([]);
    });
  });

  // --- Unknown event ---

  describe('unknown event', () => {
    it('returns state unchanged with no effects', () => {
      const before = { ...fresh(), sessionTabId: 42 };
      const { state, effects } = voiceReducer(before, { type: 'UNKNOWN_EVENT' });

      expect(state).toEqual(before);
      expect(effects).toEqual([]);
    });
  });
});

// --- getComputedState ---

describe('getComputedState', () => {
  it('reports no session when sessionTabId is null', () => {
    const computed = getComputedState(fresh());

    expect(computed.session).toBe(false);
    expect(computed.sessionConnecting).toBe(false);
    expect(computed.sessionTabId).toBeNull();
    expect(computed.mic).toBe(false);
  });

  it('reports session connecting when not ready', () => {
    const computed = getComputedState({ ...fresh(), sessionTabId: 42 });

    expect(computed.session).toBe(true);
    expect(computed.sessionConnecting).toBe(true);
  });

  it('reports session active when ready', () => {
    const computed = getComputedState({ ...fresh(), sessionTabId: 42, sessionReady: true });

    expect(computed.session).toBe(true);
    expect(computed.sessionConnecting).toBe(false);
  });

  it('reports mic state', () => {
    const computed = getComputedState({ ...fresh(), sessionTabId: 42, sessionReady: true, micActive: true });

    expect(computed.mic).toBe(true);
  });
});

// --- Full lifecycle: click mic → session ready → mic on → disconnect ---

describe('full lifecycle', () => {
  it('click → ready → mic on → disconnect', () => {
    // 1. User clicks mic — no session.
    let { state, effects } = voiceReducer(fresh(), { type: 'TOGGLE_MIC', tabId: 10 });
    expect(state.sessionTabId).toBe(10);
    expect(state.pendingAutoMic).toBe(true);
    expect(effects.find(e => e.type === 'CREATE_OFFSCREEN')).toBeTruthy();

    // 2. Session becomes ready — auto-enable mic.
    ({ state, effects } = voiceReducer(state, { type: 'VOICE_READY' }));
    expect(state.sessionReady).toBe(true);
    expect(state.micActive).toBe(true);
    expect(state.pendingAutoMic).toBe(false);
    expect(effects.find(e => e.type === 'SET_MIC' && e.on === true)).toBeTruthy();

    // 3. User toggles mic off.
    ({ state, effects } = voiceReducer(state, { type: 'TOGGLE_MIC', tabId: 10 }));
    expect(state.micActive).toBe(false);
    expect(effects.find(e => e.type === 'SET_MIC' && e.on === false)).toBeTruthy();

    // 4. User toggles mic back on.
    ({ state, effects } = voiceReducer(state, { type: 'TOGGLE_MIC', tabId: 10 }));
    expect(state.micActive).toBe(true);

    // 5. Connection drops.
    ({ state, effects } = voiceReducer(state, { type: 'VOICE_DISCONNECTED' }));
    expect(state).toEqual(initialState);
  });

  it('click → ready → kill session', () => {
    let { state } = voiceReducer(fresh(), { type: 'TOGGLE_MIC', tabId: 10 });
    ({ state } = voiceReducer(state, { type: 'VOICE_READY' }));
    expect(state.micActive).toBe(true);

    const result = voiceReducer(state, { type: 'KILL_SESSION' });
    expect(result.state).toEqual(initialState);
    expect(result.effects.find(e => e.type === 'DESTROY_OFFSCREEN')).toBeTruthy();
  });
});
