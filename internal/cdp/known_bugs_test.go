package cdp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// To see the race detector failures, run:
// go test -race -run TestBug_ ./internal/cdp/

// ---------------------------------------------------------------------------
// Bug 1: Global Event Handler Race Condition
//
// The old code used SetEventHandler/getEventHandler to save-and-restore
// a global function pointer. When two tabs ran CaptureLayerTree concurrently:
//   1. Tab A saves prevA=nil, sets handlerA
//   2. Tab B saves prevB=handlerA, sets handlerB
//   3. Tab A finishes, defer restores prevA=nil → clobbers Tab B's handler
//   4. Tab B is permanently deaf to events
//
// Fix: Replace global handler with per-tab event subscription (SubscribeEvents).
// ---------------------------------------------------------------------------

func TestBug_EventHandlerClobberedByConcurrentCapture(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	// Tab A subscribes to events for tab 1.
	chA := p.SubscribeEvents(1)
	defer p.UnsubscribeEvents(1)

	// Tab B subscribes to events for tab 2.
	chB := p.SubscribeEvents(2)
	defer p.UnsubscribeEvents(2)

	// Tab A finishes first and unsubscribes.
	p.UnsubscribeEvents(1)

	// Send event for Tab B — it MUST still receive it.
	p.HandleEvent(2, "LayerTree.layerTreeDidChange", json.RawMessage(`{"layers":[]}`))

	select {
	case ev := <-chB:
		if ev.Method != "LayerTree.layerTreeDidChange" {
			t.Errorf("expected LayerTree.layerTreeDidChange, got %s", ev.Method)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("BUG: Tab B's event subscription was clobbered when Tab A unsubscribed")
	}

	// Verify Tab A's channel is closed (unsubscribed).
	select {
	case _, ok := <-chA:
		if ok {
			t.Error("expected Tab A's channel to be closed after UnsubscribeEvents")
		}
	default:
		// Channel might be closed and drained already — fine.
	}
}

func TestBug_ConcurrentCaptureLayerTreeIsolation(t *testing.T) {
	// Full integration test: two CaptureLayerTree calls on different tabs
	// must both receive their respective events without interference.
	ms := &mockSender{}
	p := New(ms)

	macheA := map[string]int{"mache-1": 10}
	macheB := map[string]int{"mache-2": 20}

	var resultA, resultB map[string]LayerInfo
	var wg sync.WaitGroup
	wg.Add(2)

	ctx := context.Background()

	// Background responder: continuously drains messages and responds.
	responded := make(map[int64]bool)
	stopResponder := make(chan struct{})
	enableCount := 0
	go func() {
		for {
			select {
			case <-stopResponder:
				return
			case <-time.After(5 * time.Millisecond):
			}
			for _, msg := range ms.allMsgs() {
				id, ok := msg["cdp_id"].(int64)
				if !ok || responded[id] {
					continue
				}
				switch msg["cdp_method"] {
				case "LayerTree.enable":
					responded[id] = true
					p.HandleResult(id, json.RawMessage(`{}`))
					enableCount++
					// Fire layerTreeDidChange once both enables are done.
					if enableCount == 2 {
						time.Sleep(10 * time.Millisecond)
						p.HandleEvent(1, "LayerTree.layerTreeDidChange", json.RawMessage(`{
							"layers":[{"layerId":"LA","backendNodeId":10}]
						}`))
						p.HandleEvent(2, "LayerTree.layerTreeDidChange", json.RawMessage(`{
							"layers":[{"layerId":"LB","backendNodeId":20}]
						}`))
					}
				case "LayerTree.compositingReasons":
					responded[id] = true
					p.HandleResult(id, json.RawMessage(`{"compositingReasons":[]}`))
				case "LayerTree.disable":
					responded[id] = true
					p.HandleResult(id, json.RawMessage(`{}`))
				}
			}
		}
	}()

	// Launch Tab A and Tab B captures concurrently.
	go func() { defer wg.Done(); resultA = CaptureLayerTree(ctx, p, 1, macheA, 2*time.Second) }()
	go func() { defer wg.Done(); resultB = CaptureLayerTree(ctx, p, 2, macheB, 2*time.Second) }()

	// Wait for both to complete.
	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent CaptureLayerTree calls did not complete")
	}
	close(stopResponder)

	// Verify both tabs got results.
	if _, ok := resultA["mache-1"]; !ok {
		t.Error("BUG: Tab A (tab 1) did not receive its layer event — handler was clobbered")
	}
	if _, ok := resultB["mache-2"]; !ok {
		t.Error("BUG: Tab B (tab 2) did not receive its layer event — handler was clobbered")
	}
}

// ---------------------------------------------------------------------------
// Bug 2: captureMu blocks context cancellation (tested in api/known_bugs_test.go)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Bug 3: Goroutine Sprawl on Cancellation
//
// MacheBackendMap pushes into a bounded semaphore for each nodeID. If the
// context is cancelled mid-loop, the for loop continues to push 3,900 more
// items into the semaphore and spawns goroutines that instantly die.
//
// Fix: Check ctx.Done() before pushing into semaphore.
// ---------------------------------------------------------------------------

func TestBug_MacheBackendMapShortCircuitsOnCancel(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		_, _ = MacheBackendMap(ctx, p, 1, 42)
		close(done)
	}()

	// querySelectorAll — return 200 node IDs.
	waitForMsg(t, ms)
	msg := ms.lastMsg()

	var nodeIDs []int
	for i := 0; i < 200; i++ {
		nodeIDs = append(nodeIDs, 100+i)
	}
	nodeIDsJSON, _ := json.Marshal(map[string][]int{"nodeIds": nodeIDs})
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(nodeIDsJSON))

	// Let the first batch of goroutines start (semaphore is 10 wide).
	time.Sleep(20 * time.Millisecond)

	// Cancel context — the loop should stop spawning new goroutines.
	cancel()

	// Respond to any in-flight describeNode calls so they can finish.
	time.Sleep(50 * time.Millisecond)
	for _, m := range ms.allMsgs() {
		if m["cdp_method"] == "DOM.describeNode" {
			p.HandleResult(m["cdp_id"].(int64), json.RawMessage(
				`{"node":{"backendNodeId":999,"attributes":["data-mache-id","mache-x"]}}`,
			))
		}
	}

	// Function should return promptly.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("MacheBackendMap did not return after context cancellation")
	}

	// Count how many describeNode calls were actually dispatched.
	describeCount := 0
	for _, m := range ms.allMsgs() {
		if m["cdp_method"] == "DOM.describeNode" {
			describeCount++
		}
	}

	// With semaphore width 10, we expect at most ~10-20 calls (the initial
	// batch that started before cancel), NOT all 200.
	if describeCount > 50 {
		t.Errorf("BUG: %d describeNode calls dispatched despite context cancellation "+
			"(expected ≤50, ideally ≤%d)", describeCount, DescribeNodeConcurrency*2)
	}
}

// ---------------------------------------------------------------------------
// Bug 4: Unsafe type assertions in Handle* methods
//
// All Handle* methods load from sync.Map and bare-cast (v.(chan error), etc).
// If a programming error stores a wrong type, or a stale entry exists, the
// assertion panics and crashes the server. These should be safe assertions.
// ---------------------------------------------------------------------------

func TestBug_HandleAttachedWrongType_NoPanic(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	// Corrupt the map by storing a wrong type.
	p.attachM.Store(1, "not a channel")

	// Must not panic.
	p.HandleAttached(1)
	p.HandleAttachFailed(1, "err")
}

func TestBug_HandleDetachedWrongType_NoPanic(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	p.detachM.Store(1, 42)
	p.HandleDetached(1)
}

func TestBug_HandleResultWrongType_NoPanic(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	p.pending.Store(int64(1), "garbage")
	p.HandleResult(1, json.RawMessage(`{}`))
	p.HandleError(1, "some error")
}

func TestBug_UnsubscribeEventsWrongType_NoPanic(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	p.eventSubs.Store(1, "not a channel")
	p.UnsubscribeEvents(1)
}

func TestBug_HandleEventWrongType_NoPanic(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)

	p.eventSubs.Store(1, 99)
	p.HandleEvent(1, "Page.loadEventFired", json.RawMessage(`{}`))
}
