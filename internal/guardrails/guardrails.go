// Package guardrails provides deterministic checks that enforce mechanical
// rules the LLM frequently ignores: deduplication, pagination tracking,
// reference validation, completeness checking, and output normalization.
//
// All guardrails are gated behind XRAY_GUARDRAILS=1 and logged via XRAY_DEBUG.
package guardrails

import (
	"log"
	"os"
	"sync"
)

// GoalState tracks per-goal guardrail state. Created fresh for each
// executeGoal() call in the Doer and passed to subsystems that need it.
type GoalState struct {
	mu            sync.RWMutex
	Enabled       bool
	debug         bool
	TabID         int
	VisitedPages  map[string]int // url+scroll → visit count
	FoundItems    []string       // deduplicated findings
	ActionHistory []ActionRecord // tool call log
	PageItemCount int            // detected item count (-1 = unknown)
}

// ActionRecord logs a single tool invocation for heuristic lookback.
type ActionRecord struct {
	Step   int
	Tool   string
	Path   string
	Result string
}

// New creates a GoalState for the given tab. Reads XRAY_GUARDRAILS and
// XRAY_DEBUG env vars once at creation time.
func New(tabID int) *GoalState {
	return &GoalState{
		Enabled:       os.Getenv("XRAY_GUARDRAILS") == "1",
		debug:         os.Getenv("XRAY_DEBUG") == "1",
		TabID:         tabID,
		VisitedPages:  make(map[string]int),
		PageItemCount: -1,
	}
}

// RecordAction appends a tool invocation to the action history.
func (gs *GoalState) RecordAction(step int, tool, path, result string) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	gs.ActionHistory = append(gs.ActionHistory, ActionRecord{
		Step:   step,
		Tool:   tool,
		Path:   path,
		Result: result,
	})
}

// LastActions returns the most recent n actions (or fewer if history is short).
func (gs *GoalState) LastActions(n int) []ActionRecord {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	if n > len(gs.ActionHistory) {
		n = len(gs.ActionHistory)
	}
	out := make([]ActionRecord, n)
	copy(out, gs.ActionHistory[len(gs.ActionHistory)-n:])
	return out
}

// Log emits a guardrail debug line if XRAY_DEBUG is enabled.
func (gs *GoalState) Log(subsystem, msg string) {
	if gs.debug {
		log.Printf("Guardrail [%s]: %s (tab %d)", subsystem, msg, gs.TabID)
	}
}
