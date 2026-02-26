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
func ProjectToGraph(sessions []SessionInfo, buffers, statuses map[string]string, activeSession string) *graph.MemoryStore {
	store := graph.NewMemoryStore()

	// Root directories.
	windowsDir := &graph.Node{
		ID:       "windows",
		Mode:     fs.ModeDir,
		Children: []string{},
	}
	store.AddRoot(windowsDir)

	// active_session file at root.
	activeFile := &graph.Node{ID: "active_session", Data: []byte(activeSession)}
	store.AddRoot(activeFile)

	// Track which window/tab dirs we've already created.
	createdDirs := make(map[string]bool)

	for _, s := range sessions {
		wid := s.WindowID
		if wid == "" {
			wid = "w0"
		}
		tid := s.TabID
		if tid == "" {
			tid = "t0"
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
	}

	return store
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
