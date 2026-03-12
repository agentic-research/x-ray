package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gorilla/websocket"
)

// ShellCommand is the inbound message from the terminal.
type ShellCommand struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Cwd     string   `json:"cwd"`
}

// ShellResponse is the outbound message to the terminal.
type ShellResponse struct {
	Type   string `json:"type"`
	Output string `json:"output"`
	Error  bool   `json:"error,omitempty"`
}

func (h *Handler) sendShellResponse(conn *websocket.Conn, resp ShellResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = conn.WriteMessage(websocket.TextMessage, data)
}

// handleShellCommand processes a terminal command against the active tab's mache engine.
func (h *Handler) handleShellCommand(conn *websocket.Conn, raw []byte) {
	var cmd ShellCommand
	if err := json.Unmarshal(raw, &cmd); err != nil {
		h.sendShellResponse(conn, ShellResponse{Type: "SHELL_RESPONSE", Output: "invalid command", Error: true})
		return
	}

	// Use the active voice tab's session.
	h.mu.Lock()
	tabID := h.activeVoiceTab
	h.mu.Unlock()

	if tabID == 0 {
		h.sendShellResponse(conn, ShellResponse{
			Type:   "SHELL_RESPONSE",
			Output: "\x1b[38;5;196mNo active tab. Navigate to a page first.\x1b[0m",
			Error:  true,
		})
		return
	}

	sess := h.getSession(tabID)
	engine := sess.GetEngine()

	if !engine.HasSchema() {
		h.sendShellResponse(conn, ShellResponse{
			Type:   "SHELL_RESPONSE",
			Output: "\x1b[38;5;196mNo schema yet. Wait for page capture to complete.\x1b[0m",
			Error:  true,
		})
		return
	}

	var resp ShellResponse
	resp.Type = "SHELL_RESPONSE"

	switch cmd.Command {
	case "ls":
		resp = h.shellLS(engine, cmd)
	case "cat":
		resp = h.shellCat(engine, cmd)
	case "tree":
		resp = h.shellTree(engine, cmd)
	default:
		resp.Output = fmt.Sprintf("command not found: %s", cmd.Command)
		resp.Error = true
	}

	h.sendShellResponse(conn, resp)
}

func (h *Handler) shellLS(engine EngineReader, cmd ShellCommand) ShellResponse {
	target := cmd.Cwd
	if len(cmd.Args) > 0 {
		target = cmd.Args[0]
	}

	entries, err := engine.ListDir(target)
	if err != nil {
		return ShellResponse{Type: "SHELL_RESPONSE", Output: fmt.Sprintf("ls: %v", err), Error: true}
	}

	if len(entries) == 0 {
		return ShellResponse{Type: "SHELL_RESPONSE", Output: "\x1b[38;5;245m(empty)\x1b[0m"}
	}

	// Color directories vs files
	var lines []string
	for _, entry := range entries {
		if strings.HasSuffix(entry, "/") {
			lines = append(lines, fmt.Sprintf("\x1b[38;5;75m%s\x1b[0m", entry))
		} else {
			lines = append(lines, entry)
		}
	}

	return ShellResponse{Type: "SHELL_RESPONSE", Output: strings.Join(lines, "  ")}
}

func (h *Handler) shellCat(engine EngineReader, cmd ShellCommand) ShellResponse {
	if len(cmd.Args) == 0 {
		return ShellResponse{Type: "SHELL_RESPONSE", Output: "usage: cat <path>", Error: true}
	}

	target := cmd.Args[0]
	content, err := engine.ReadFile(target)
	if err != nil {
		return ShellResponse{Type: "SHELL_RESPONSE", Output: fmt.Sprintf("cat: %v", err), Error: true}
	}

	return ShellResponse{Type: "SHELL_RESPONSE", Output: content}
}

func (h *Handler) shellTree(engine EngineReader, cmd ShellCommand) ShellResponse {
	target := cmd.Cwd
	if len(cmd.Args) > 0 {
		target = cmd.Args[0]
	}

	var sb strings.Builder
	h.treeWalk(engine, target, "", &sb, 0, 3) // max depth 3

	output := sb.String()
	if output == "" {
		return ShellResponse{Type: "SHELL_RESPONSE", Output: "\x1b[38;5;245m(empty)\x1b[0m"}
	}

	return ShellResponse{Type: "SHELL_RESPONSE", Output: strings.TrimRight(output, "\r\n")}
}

func (h *Handler) treeWalk(engine EngineReader, dirPath, prefix string, sb *strings.Builder, depth, maxDepth int) {
	if depth >= maxDepth {
		return
	}

	entries, err := engine.ListDir(dirPath)
	if err != nil {
		return
	}

	for i, entry := range entries {
		isLast := i == len(entries)-1
		connector := "\u251c\u2500\u2500 " // ├──
		if isLast {
			connector = "\u2514\u2500\u2500 " // └──
		}

		isDir := strings.HasSuffix(entry, "/")
		name := entry
		if isDir {
			name = fmt.Sprintf("\x1b[38;5;75m%s\x1b[0m", entry)
		}

		sb.WriteString(prefix + connector + name + "\r\n")

		if isDir {
			childPrefix := prefix + "\u2502   " // │
			if isLast {
				childPrefix = prefix + "    "
			}
			childPath := dirPath
			if childPath == "/" {
				childPath = "/" + strings.TrimSuffix(entry, "/")
			} else {
				childPath = dirPath + "/" + strings.TrimSuffix(entry, "/")
			}
			h.treeWalk(engine, childPath, childPrefix, sb, depth+1, maxDepth)
		}
	}
}

// EngineReader is the subset of mache.Engine methods needed by the shell.
type EngineReader interface {
	HasSchema() bool
	ListDir(dirPath string) ([]string, error)
	ReadFile(filePath string) (string, error)
}

// --- Agent terminal broadcast ---

// AgentShellResponse is sent to the sidebar terminal when the agent executes tool calls.
// It reuses the SHELL_RESPONSE type but with Agent=true so the terminal renders it differently.
type AgentShellResponse struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"` // formatted shell command (before execution)
	Output  string `json:"output,omitempty"`  // tool result (after execution)
	Agent   bool   `json:"agent"`             // true = agent-originated, render with agent prompt
}

// broadcastAgentTerminal sends a tool call or result to the sidebar terminal via WebSocket.
func (h *Handler) broadcastAgentTerminal(tabID int, command, output string) {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return
	}

	msg := AgentShellResponse{
		Type:    "AGENT_SHELL",
		Command: command,
		Output:  output,
		Agent:   true,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = conn.WriteMessage(websocket.TextMessage, data)
}

// formatToolAsShell renders a Navigator tool call as a shell-like command string.
func formatToolAsShell(toolName string, args map[string]any) string {
	switch toolName {
	case "ls":
		p, _ := args["path"].(string)
		if p == "" {
			p = "/"
		}
		return fmt.Sprintf("ls %s", p)
	case "cat":
		p, _ := args["path"].(string)
		return fmt.Sprintf("cat %s", p)
	case "stat":
		p, _ := args["path"].(string)
		return fmt.Sprintf("stat %s", p)
	case "grep":
		pattern, _ := args["pattern"].(string)
		p, _ := args["path"].(string)
		if p != "" {
			return fmt.Sprintf("grep %q %s", pattern, p)
		}
		return fmt.Sprintf("grep %q", pattern)
	case "act":
		p, _ := args["path"].(string)
		action, _ := args["action"].(string)
		value, _ := args["value"].(string)
		if value != "" {
			return fmt.Sprintf("act %s %s %q", action, p, value)
		}
		return fmt.Sprintf("act %s %s", action, p)
	case "scroll":
		dir, _ := args["direction"].(string)
		return fmt.Sprintf("scroll %s", dir)
	case "goto":
		url, _ := args["url"].(string)
		return fmt.Sprintf("goto %s", url)
	case "rescan":
		target, _ := args["mache_id"].(string)
		if target != "" {
			return fmt.Sprintf("rescan --zoom %s", target)
		}
		return "rescan"
	case "list_tabs":
		return "tabs"
	case "switch_tab":
		id, _ := args["tab_id"].(float64)
		return fmt.Sprintf("switch_tab %d", int(id))
	case "new_tab":
		url, _ := args["url"].(string)
		return fmt.Sprintf("new_tab %s", url)
	case "new_window":
		url, _ := args["url"].(string)
		return fmt.Sprintf("new_window %s", url)
	default:
		return toolName
	}
}
