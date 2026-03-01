package api

import (
	"context"
	"sync"

	"github.com/agentic-research/mache/graph"
	"github.com/agentic-research/x-ray/internal/mache"
)

// pendingAction is an action queued for dispatch when the extension reconnects.
type pendingAction struct {
	TabID   int
	MacheID string
	Action  string
}

// DOMUpdate carries the post-scroll summary and any browser-resolved primary items.
type DOMUpdate struct {
	Summary       string
	ResolvedItems map[string][]string
}

// TabSession holds per-tab state: its own Engine and Navigator.
type TabSession struct {
	TabID             int
	Engine            *mache.Engine
	Composite         *graph.CompositeGraph // multiplexes browser + iterm
	Navigator         IntentHandler
	SchemaReady       chan struct{}            // closed when schema is applied
	DOMUpdateCh       chan DOMUpdate           // receives summary + resolved items after scroll
	DOMMutatedCh      chan struct{}            // signals in-page DOM mutation (from MutationObserver)
	SelectorsResolved chan map[string][]string // receives resolved items from RESOLVE_SELECTORS round-trip
	RescanPath        string                   // set by voice handler for targeted rescan, consumed by handleDOMSnapshot
	CurrentURL        string                   // URL of the page currently loaded or loading (prevents redundant goto)
	Doer              *Doer                    // background execution agent (created lazily on first voice session)
	doerCancel        context.CancelFunc       // cancels the Doer's Run goroutine (not just the current goal)
	TabsListedCh      chan []TabInfo           // receives tab list from LIST_TABS round-trip
	CVRegions         []EdgeRegion             // canvas regions detected via edge analysis, used for CDP pixel-click

	schemaMu     sync.Mutex // protects SchemaReady close + schemaGen
	schemaClosed bool
	schemaGen    uint64       // monotonically increasing; only the latest generation applies
	engineMu     sync.RWMutex // protects Engine pointer swaps
}

// SignalSchemaReady safely closes SchemaReady. No-op if already closed.
func (s *TabSession) SignalSchemaReady() {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if !s.schemaClosed {
		close(s.SchemaReady)
		s.schemaClosed = true
	}
}

// ResetSchema creates a fresh SchemaReady channel (used by goto navigation).
func (s *TabSession) ResetSchema() {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	s.SchemaReady = make(chan struct{})
	s.schemaClosed = false
}

// GetSchemaReady returns the SchemaReady channel under the lock, preventing
// a data race between select-reading the channel and ResetSchema replacing it.
func (s *TabSession) GetSchemaReady() <-chan struct{} {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	return s.SchemaReady
}

// GetEngine returns the Engine pointer under a read lock.
func (s *TabSession) GetEngine() *mache.Engine {
	s.engineMu.RLock()
	defer s.engineMu.RUnlock()
	return s.Engine
}

// SwapEngine atomically replaces the Engine pointer.
func (s *TabSession) SwapEngine(engine *mache.Engine) {
	s.engineMu.Lock()
	defer s.engineMu.Unlock()
	s.Engine = engine
}

// GetCurrentURL returns the URL currently associated with this session.
func (s *TabSession) GetCurrentURL() string {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	return s.CurrentURL
}

// SetCurrentURL updates the URL for this session.
func (s *TabSession) SetCurrentURL(url string) {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	s.CurrentURL = url
}
