package navigator

import (
	"log"
)

// Agent represents Stage 2: The Navigator
// A Gemini Live/Voice agent that explores the projected Mache filesystem and takes actions.
type Agent struct {
	// Gemini Live/Voice client goes here
	// Mache FS client goes here
}

func NewAgent() *Agent {
	return &Agent{}
}

// HandleIntent processes a user intent string by navigating the virtual FS.
func (a *Agent) HandleIntent(intent string) error {
	log.Printf("Navigator: Handling intent: %s", intent)

	// TODO:
	// 1. Agent is provided with tools: ls(path), read(path), act(path, actionType)
	// 2. Agent interacts with the internal/mache virtual filesystem to find the target.
	// 3. Agent calls act(), which resolves to a data-mache-id.
	// 4. Action is sent via WebSocket to the Browser Extension to execute.

	return nil
}
