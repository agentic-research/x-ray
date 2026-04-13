package navigator

import (
	"context"
	"testing"

	"google.golang.org/genai"
)

// stubTool is a minimal Tool implementation for unit tests.
type stubTool struct {
	name string
}

func (s *stubTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: s.name, Description: s.name + " stub"}
}

func (s *stubTool) Execute(_ context.Context, _ map[string]any) (string, *ActionResult) {
	return s.name + " executed", nil
}

func TestExecuteBlocksTool(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&stubTool{name: "act"})
	reg.Register(&stubTool{name: "grep"})

	reg.SetBlocked("act")

	// act should be rejected.
	msg, ar := reg.Execute(context.Background(), &genai.FunctionCall{
		Name: "act",
		Args: map[string]any{},
	})
	if ar != nil {
		t.Fatal("expected nil ActionResult for blocked tool")
	}
	if msg == "act executed" {
		t.Fatal("blocked tool should not execute")
	}
	if msg == "" {
		t.Fatal("expected an error message for blocked tool")
	}
	t.Logf("blocked message: %s", msg)

	// grep should still work.
	msg, _ = reg.Execute(context.Background(), &genai.FunctionCall{
		Name: "grep",
		Args: map[string]any{},
	})
	if msg != "grep executed" {
		t.Fatalf("expected 'grep executed', got %q", msg)
	}
}

func TestClearBlocked(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&stubTool{name: "act"})

	reg.SetBlocked("act")

	// Blocked: should reject.
	msg, _ := reg.Execute(context.Background(), &genai.FunctionCall{
		Name: "act",
		Args: map[string]any{},
	})
	if msg == "act executed" {
		t.Fatal("blocked tool should not execute before ClearBlocked")
	}

	reg.ClearBlocked()

	// Unblocked: should work.
	msg, _ = reg.Execute(context.Background(), &genai.FunctionCall{
		Name: "act",
		Args: map[string]any{},
	})
	if msg != "act executed" {
		t.Fatalf("after ClearBlocked, expected 'act executed', got %q", msg)
	}
}
