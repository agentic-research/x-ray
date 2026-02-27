package iterm

import (
	"testing"
)

func TestProjectToGraph_BasicStructure(t *testing.T) {
	sessions := []SessionInfo{
		{SessionID: "abc-123", WindowID: "w0", TabID: "t0", Title: "zsh", CWD: "~/code"},
	}
	buffers := map[string]string{"abc-123": "$ git status\nOn branch main\n$ "}
	statuses := map[string]string{"abc-123": "idle"}
	active := "abc-123"

	store := ProjectToGraph(sessions, buffers, statuses, active)

	// Root should have "windows" and "active_session".
	roots, err := store.ListChildren("")
	if err != nil {
		t.Fatalf("ListChildren root: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("expected 2 roots, got %d: %v", len(roots), roots)
	}

	// active_session directory.
	node, err := store.GetNode("active_session")
	if err != nil {
		t.Fatalf("GetNode active_session: %v", err)
	}
	if !node.Mode.IsDir() {
		t.Error("active_session should be a directory")
	}

	// active_session should have children aliased to the active session.
	children, _ := store.ListChildren("active_session")
	if len(children) != 5 {
		t.Errorf("expected 5 children in active_session, got %d", len(children))
	}

	// Verify buffer file inside active_session.
	bufNode, err := store.GetNode("active_session/buffer")
	if err != nil {
		t.Fatalf("GetNode active_session/buffer: %v", err)
	}
	if string(bufNode.Data) != "$ git status\nOn branch main\n$ " {
		t.Errorf("active_session/buffer = %q", bufNode.Data)
	}

	// Session directory should exist.
	sessDir, err := store.GetNode("windows/w0/tabs/t0/sessions/abc-123")
	if err != nil {
		t.Fatalf("GetNode session dir: %v", err)
	}
	if !sessDir.Mode.IsDir() {
		t.Error("session dir should be a directory")
	}

	// Check leaf files.
	for _, leaf := range []struct {
		name, want string
	}{
		{"buffer", "$ git status\nOn branch main\n$ "},
		{"cwd", "~/code"},
		{"status", "idle"},
		{"mache_id", "iterm:abc-123"},
	} {
		node, err := store.GetNode("windows/w0/tabs/t0/sessions/abc-123/" + leaf.name)
		if err != nil {
			t.Errorf("GetNode %s: %v", leaf.name, err)
			continue
		}
		if string(node.Data) != leaf.want {
			t.Errorf("%s = %q, want %q", leaf.name, node.Data, leaf.want)
		}
	}
}

func TestProjectToGraph_MultipleSessions(t *testing.T) {
	sessions := []SessionInfo{
		{SessionID: "s1", WindowID: "w0", TabID: "t0", Title: "vim"},
		{SessionID: "s2", WindowID: "w0", TabID: "t1", Title: "shell"},
		{SessionID: "s3", WindowID: "w1", TabID: "t2", Title: "htop"},
	}
	buffers := map[string]string{}
	statuses := map[string]string{}

	store := ProjectToGraph(sessions, buffers, statuses, "s1")

	// Should have two window dirs.
	wDir, err := store.GetNode("windows")
	if err != nil {
		t.Fatalf("GetNode windows: %v", err)
	}
	if len(wDir.Children) != 2 {
		t.Errorf("expected 2 windows, got %d", len(wDir.Children))
	}

	// w0 should have 2 tabs.
	tabsDir, err := store.GetNode("windows/w0/tabs")
	if err != nil {
		t.Fatalf("GetNode w0/tabs: %v", err)
	}
	if len(tabsDir.Children) != 2 {
		t.Errorf("expected 2 tabs in w0, got %d", len(tabsDir.Children))
	}
}

func TestProjectToGraph_EmptyCWD(t *testing.T) {
	sessions := []SessionInfo{
		{SessionID: "s1", WindowID: "w0", TabID: "t0", Title: "zsh"},
	}
	store := ProjectToGraph(sessions, nil, nil, "")

	node, err := store.GetNode("windows/w0/tabs/t0/sessions/s1/cwd")
	if err != nil {
		t.Fatalf("GetNode cwd: %v", err)
	}
	if string(node.Data) != "(unknown)" {
		t.Errorf("cwd = %q, want %q", node.Data, "(unknown)")
	}
}

func TestBuildDescription(t *testing.T) {
	tests := []struct {
		title, cwd, status, want string
	}{
		{"zsh", "~/code", "idle", "zsh — ~/code (idle)"},
		{"vim", "", "running", "vim (running)"},
		{"", "~/code", "idle", "~/code (idle)"},
		{"zsh", "(unknown)", "idle", "zsh (idle)"},
	}
	for _, tt := range tests {
		got := buildDescription(tt.title, tt.cwd, tt.status)
		if got != tt.want {
			t.Errorf("buildDescription(%q,%q,%q) = %q, want %q", tt.title, tt.cwd, tt.status, got, tt.want)
		}
	}
}

func TestResolveSessionID(t *testing.T) {
	tests := []struct {
		path, want string
	}{
		{"windows/w0/tabs/t0/sessions/abc-123/buffer", "abc-123"},
		{"windows/w0/tabs/t0/sessions/abc-123", "abc-123"},
		{"iterm:abc-123", "abc-123"},
		{"windows", ""},
		{"", ""},
	}
	b := &Bridge{active: "active-123"}
	for _, tt := range tests {
		got := b.resolveSessionID(tt.path)
		if got != tt.want {
			t.Errorf("resolveSessionID(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestSpecialKeyText(t *testing.T) {
	tests := []struct {
		key, want string
	}{
		{"", "\r"},
		{"enter", "\r"},
		{"ctrl-c", "\x03"},
		{"ctrl-d", "\x04"},
		{"tab", "\t"},
		{"up", "\x1b[A"},
		{"arbitrary", "arbitrary"},
	}
	for _, tt := range tests {
		got := specialKeyText(tt.key)
		if got != tt.want {
			t.Errorf("specialKeyText(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}
