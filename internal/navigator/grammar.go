package navigator

import (
	"fmt"
	"strings"
)

// BuildGBNF generates a GBNF grammar that constrains model output to valid
// CLI commands with paths drawn from the current filesystem state.
//
// When excludeAct is true the act command is omitted (read-only mode).
// When paths is empty the grammar allows any path (unconstrained fallback).
//
// The grammar is designed for llama.cpp / Ollama grammar-constrained decoding.
func BuildGBNF(paths []string, excludeAct bool) string {
	var sb strings.Builder

	sb.WriteString("root ::= tool-call\n\n")

	// Build tool-call alternatives.
	var tools []string
	tools = append(tools, "ls-cmd", "cat-cmd")
	if !excludeAct {
		tools = append(tools, "act-cmd")
	}
	tools = append(tools,
		"scroll-cmd", "goto-cmd", "rescan-cmd",
		"list-tabs-cmd", "switch-tab-cmd",
		"new-window-cmd", "new-tab-cmd",
	)
	fmt.Fprintf(&sb, "tool-call ::= %s\n\n", strings.Join(tools, " | "))

	// Commands with path arguments.
	sb.WriteString("ls-cmd ::= \"ls \" valid-path\n")
	sb.WriteString("cat-cmd ::= \"cat \" valid-path\n")
	if !excludeAct {
		sb.WriteString("act-cmd ::= \"act \" action \" \" valid-path payload?\n")
	}

	// Commands with fixed arguments.
	sb.WriteString("scroll-cmd ::= \"browser.scroll \" (\"up\" | \"down\")\n")
	sb.WriteString("goto-cmd ::= \"browser.goto \" url\n")
	sb.WriteString("rescan-cmd ::= \"browser.rescan\" (\" \" valid-path)?\n")
	sb.WriteString("list-tabs-cmd ::= \"browser.list_tabs\"\n")
	sb.WriteString("switch-tab-cmd ::= \"browser.switch_tab \" digits\n")
	sb.WriteString("new-window-cmd ::= \"iterm.new_window\"\n")
	sb.WriteString("new-tab-cmd ::= \"iterm.new_tab\" (\" \" valid-path)?\n\n")

	// Leaf rules.
	sb.WriteString("action ::= \"►\" | \"⊙\" | \"✎\" | \"⏎\"\n")
	sb.WriteString("payload ::= \" \\\"\" [^\"\\n]* \"\\\"\"\n")
	sb.WriteString("url ::= [a-zA-Z] [^ \\n]*\n")
	sb.WriteString("digits ::= [0-9]+\n\n")

	// Dynamic path rule — the core of constrained decoding.
	if len(paths) > 0 {
		alts := make([]string, len(paths))
		for i, p := range paths {
			alts[i] = fmt.Sprintf("%q", p) // Go %q gives GBNF-compatible quoting
		}
		fmt.Fprintf(&sb, "valid-path ::= %s\n", strings.Join(alts, " | "))
	} else {
		// Unconstrained fallback: any path-like string.
		sb.WriteString("valid-path ::= \"/\" [a-zA-Z0-9_/.]*\n")
	}

	return sb.String()
}

// EnumeratePaths walks the NavFS recursively and returns all valid paths.
// Includes both directories and files, since different tools operate on both.
func EnumeratePaths(fs *NavFS) []string {
	paths := []string{"/"}

	rootEntries, err := fs.ListDir("/")
	if err != nil || len(rootEntries) == 0 {
		return paths
	}

	for _, entry := range rootEntries {
		name := strings.TrimSuffix(entry, "/")
		walkPaths(fs, &paths, "/"+name, strings.HasSuffix(entry, "/"), 0)
	}
	return paths
}

const maxGrammarDepth = 8

func walkPaths(fs *NavFS, paths *[]string, fullPath string, isDir bool, depth int) {
	*paths = append(*paths, fullPath)

	if !isDir || depth >= maxGrammarDepth {
		return
	}

	entries, err := fs.ListDir(fullPath)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry, "/")
		child := fullPath + "/" + name
		walkPaths(fs, paths, child, strings.HasSuffix(entry, "/"), depth+1)
	}
}
