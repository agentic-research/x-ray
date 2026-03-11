package iterm

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/agentic-research/mache/graph"
)

// ProjectToGraph builds a mache MemoryStore representing the /iterm/ filesystem.
// The projection looks like:
//
//	windows/
//	  {window_id}/
//	    tabs/
//	      {tab_id}/
//	        sessions/
//	          {session_id}/
//	            description    "zsh — ~/code (idle)"
//	            mache_id       stable ID for act() targeting
//	            buffer         last N lines, ANSI stripped
//	            cwd            current working directory
//	            status         "idle" | "running"
//	active_session             session ID of the focused session
//	agent_sessions/            sessions spawned by the agent (safe to type into)
//	  {session_id}/
//	    description, mache_id, buffer, cwd, status
func ProjectToGraph(sessions []SessionInfo, buffers, statuses map[string]string, activeSession string, spawned map[string]bool) *graph.MemoryStore {
	store := graph.NewMemoryStore()

	// Root directories.
	windowsDir := &graph.Node{
		ID:       "windows",
		Mode:     fs.ModeDir,
		Children: []string{},
	}
	store.AddRoot(windowsDir)

	// Instead of a string file, active_session is a directory mirroring the active session.
	// We'll populate its children below if an active session is found.
	activeDir := &graph.Node{
		ID:       "active_session",
		Mode:     fs.ModeDir,
		Children: []string{},
	}
	store.AddRoot(activeDir)

	// agent_sessions/ contains only sessions spawned by the agent (safe to type into).
	agentDir := &graph.Node{
		ID:       "agent_sessions",
		Mode:     fs.ModeDir,
		Children: []string{},
	}
	store.AddRoot(agentDir)

	// Track which window/tab dirs we've already created.
	createdDirs := make(map[string]bool)
	// Collect session titles per tab dir for tab-level descriptions.
	tabTitles := make(map[string][]string)
	// Collect session titles per window dir for window-level descriptions.
	windowTitles := make(map[string][]string)

	// Map raw iTerm2 UUIDs to sequential indices for readable paths.
	windowSeq := make(map[string]string) // raw UUID → "w0", "w1", ...
	tabSeq := make(map[string]string)    // raw UUID → "t0", "t1", ...
	windowCounter := 0
	tabCounter := make(map[string]int) // per-window tab counter

	for _, s := range sessions {
		rawWID := s.WindowID
		if rawWID == "" {
			rawWID = "_default_window"
		}
		rawTID := s.TabID
		if rawTID == "" {
			rawTID = "_default_tab"
		}

		// Assign sequential window ID.
		wid, ok := windowSeq[rawWID]
		if !ok {
			wid = fmt.Sprintf("w%d", windowCounter)
			windowSeq[rawWID] = wid
			windowCounter++
		}

		// Assign sequential tab ID (per-window).
		tabKey := rawWID + "/" + rawTID
		tid, ok := tabSeq[tabKey]
		if !ok {
			tid = fmt.Sprintf("t%d", tabCounter[rawWID])
			tabSeq[tabKey] = tid
			tabCounter[rawWID]++
		}

		sid := s.SessionID

		// windows/{wid}/
		wDirID := "windows/" + wid
		if !createdDirs[wDirID] {
			wDir := &graph.Node{
				ID:       wDirID,
				Mode:     fs.ModeDir,
				Children: []string{},
			}
			store.AddNode(wDir)
			windowsDir.Children = appendUnique(windowsDir.Children, wDirID)
			createdDirs[wDirID] = true
		}

		// windows/{wid}/tabs/
		tabsID := wDirID + "/tabs"
		if !createdDirs[tabsID] {
			tabsDir := &graph.Node{
				ID:       tabsID,
				Mode:     fs.ModeDir,
				Children: []string{},
			}
			store.AddNode(tabsDir)
			if wDir, err := store.GetNode(wDirID); err == nil {
				wDir.Children = appendUnique(wDir.Children, tabsID)
			}
			createdDirs[tabsID] = true
		}

		// windows/{wid}/tabs/{tid}/
		tDirID := tabsID + "/" + tid
		if !createdDirs[tDirID] {
			tDir := &graph.Node{
				ID:       tDirID,
				Mode:     fs.ModeDir,
				Children: []string{},
			}
			store.AddNode(tDir)
			if tabsDir, err := store.GetNode(tabsID); err == nil {
				tabsDir.Children = appendUnique(tabsDir.Children, tDirID)
			}
			createdDirs[tDirID] = true
		}

		// windows/{wid}/tabs/{tid}/sessions/
		sessionsID := tDirID + "/sessions"
		if !createdDirs[sessionsID] {
			sessionsDir := &graph.Node{
				ID:       sessionsID,
				Mode:     fs.ModeDir,
				Children: []string{},
			}
			store.AddNode(sessionsDir)
			if tDir, err := store.GetNode(tDirID); err == nil {
				tDir.Children = appendUnique(tDir.Children, sessionsID)
			}
			createdDirs[sessionsID] = true
		}

		// windows/{wid}/tabs/{tid}/sessions/{sid}/
		sDirID := sessionsID + "/" + sid
		sDir := &graph.Node{
			ID:   sDirID,
			Mode: fs.ModeDir,
			Children: []string{
				sDirID + "/description",
				sDirID + "/mache_id",
				sDirID + "/buffer",
				sDirID + "/cwd",
				sDirID + "/status",
			},
			Properties: map[string][]byte{
				"mache_id": []byte("iterm:" + sid),
			},
		}
		store.AddNode(sDir)
		if sessionsDir, err := store.GetNode(sessionsID); err == nil {
			sessionsDir.Children = appendUnique(sessionsDir.Children, sDirID)
		}

		// Leaf files.
		status := "idle"
		if v, ok := statuses[sid]; ok {
			status = v
		}
		cwd := s.CWD
		if cwd == "" {
			cwd = "(unknown)"
		}

		desc := buildDescription(s.Title, cwd, status)
		buf := ""
		if v, ok := buffers[sid]; ok {
			buf = v
		}

		store.AddNode(&graph.Node{ID: sDirID + "/description", Data: []byte(desc)})
		store.AddNode(&graph.Node{ID: sDirID + "/mache_id", Data: []byte("iterm:" + sid)})
		store.AddNode(&graph.Node{ID: sDirID + "/buffer", Data: []byte(buf)})
		store.AddNode(&graph.Node{ID: sDirID + "/cwd", Data: []byte(cwd)})
		store.AddNode(&graph.Node{ID: sDirID + "/status", Data: []byte(status)})

		// Track session title for tab and window level descriptions.
		tabTitles[tDirID] = append(tabTitles[tDirID], buildTabLabel(s.Title, cwd))
		windowTitles[wDirID] = append(windowTitles[wDirID], buildTabLabel(s.Title, cwd))

		// If this is the active session, alias its children into /active_session/
		if sid == activeSession {
			activeDir.Children = []string{
				"active_session/description",
				"active_session/mache_id",
				"active_session/buffer",
				"active_session/cwd",
				"active_session/status",
			}
			activeDir.Properties = map[string][]byte{
				"mache_id": []byte("iterm:" + sid),
			}
			store.AddNode(&graph.Node{ID: "active_session/description", Data: []byte(desc)})
			store.AddNode(&graph.Node{ID: "active_session/mache_id", Data: []byte("iterm:" + sid)})
			store.AddNode(&graph.Node{ID: "active_session/buffer", Data: []byte(buf)})
			store.AddNode(&graph.Node{ID: "active_session/cwd", Data: []byte(cwd)})
			store.AddNode(&graph.Node{ID: "active_session/status", Data: []byte(status)})
		}

		// If this is an agent-spawned session, mirror into /agent_sessions/{sid}/
		if spawned[sid] {
			aDirID := "agent_sessions/" + sid
			aDir := &graph.Node{
				ID:   aDirID,
				Mode: fs.ModeDir,
				Children: []string{
					aDirID + "/description",
					aDirID + "/mache_id",
					aDirID + "/buffer",
					aDirID + "/cwd",
					aDirID + "/status",
				},
				Properties: map[string][]byte{
					"mache_id": []byte("iterm:" + sid),
				},
			}
			store.AddNode(aDir)
			agentDir.Children = appendUnique(agentDir.Children, aDirID)
			store.AddNode(&graph.Node{ID: aDirID + "/description", Data: []byte(desc)})
			store.AddNode(&graph.Node{ID: aDirID + "/mache_id", Data: []byte("iterm:" + sid)})
			store.AddNode(&graph.Node{ID: aDirID + "/buffer", Data: []byte(buf)})
			store.AddNode(&graph.Node{ID: aDirID + "/cwd", Data: []byte(cwd)})
			store.AddNode(&graph.Node{ID: aDirID + "/status", Data: []byte(status)})
		}
	}

	// Second pass: add description files to tab and window directories.
	for tDirID, titles := range tabTitles {
		descID := tDirID + "/description"
		desc := strings.Join(titles, ", ")
		store.AddNode(&graph.Node{ID: descID, Data: []byte(desc)})
		if tDir, err := store.GetNode(tDirID); err == nil {
			tDir.Children = appendUnique(tDir.Children, descID)
		}
	}
	for wDirID, titles := range windowTitles {
		descID := wDirID + "/description"
		desc := strings.Join(titles, ", ")
		if len(desc) > 80 {
			desc = desc[:77] + "..."
		}
		store.AddNode(&graph.Node{ID: descID, Data: []byte(desc)})
		if wDir, err := store.GetNode(wDirID); err == nil {
			wDir.Children = appendUnique(wDir.Children, descID)
		}
	}

	return store
}

// buildTabLabel creates a short label for a session within a tab.
// Strips common iTerm2 title prefixes ("Default: ") and truncates.
func buildTabLabel(title, cwd string) string {
	title = strings.TrimPrefix(title, "Default: ")
	// Strip emoji prefixes (common in iTerm2 profile titles)
	title = strings.TrimLeft(title, " \t")
	if title != "" && cwd != "" && cwd != "(unknown)" {
		label := title + " — " + cwd
		if len(label) > 50 {
			label = label[:47] + "..."
		}
		return label
	}
	if title != "" {
		if len(title) > 50 {
			return title[:47] + "..."
		}
		return title
	}
	if cwd != "" && cwd != "(unknown)" {
		return cwd
	}
	return "(unnamed)"
}

func buildDescription(title, cwd, status string) string {
	parts := []string{}
	if title != "" {
		parts = append(parts, title)
	}
	if cwd != "" && cwd != "(unknown)" {
		parts = append(parts, cwd)
	}
	desc := strings.Join(parts, " — ")
	return fmt.Sprintf("%s (%s)", desc, status)
}

func appendUnique(slice []string, val string) []string {
	for _, v := range slice {
		if v == val {
			return slice
		}
	}
	return append(slice, val)
}
