package api

import (
	"log"
	"net/http"
	// "github.com/gorilla/websocket" // To be added later
)

// HandleWebSocketConnections handles incoming WS requests from the browser extension
func HandleWebSocketConnections(w http.ResponseWriter, r *http.Request) {
	log.Println("New WebSocket connection attempt")
	// TODO: Upgrade connection
	// TODO: Listen for messages:
	//   - MessageType: "DOM_SNAPSHOT" (Contains Screenshot + Sanitized HTML with data-mache-id)
	//     -> Route to internal/cartographer
	//     -> Apply Schema via internal/mache
	//     -> Trigger internal/navigator if there's a pending user intent
	// TODO: Send messages:
	//   - MessageType: "EXECUTE_ACTION" (Contains data-mache-id and action type like 'click')
}
