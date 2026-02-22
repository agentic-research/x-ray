package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/jamesgardner/x-ray/internal/cartographer"
	"github.com/jamesgardner/x-ray/internal/mache"
	"github.com/jamesgardner/x-ray/internal/navigator"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Handler holds the dependencies for the WebSocket handler.
type Handler struct {
	Cartographer *cartographer.Agent
	Navigator    *navigator.Agent
	Engine       *mache.Engine

	mu   sync.Mutex
	conn *websocket.Conn
}

func NewHandler(cart *cartographer.Agent, nav *navigator.Agent, engine *mache.Engine) *Handler {
	return &Handler{
		Cartographer: cart,
		Navigator:    nav,
		Engine:       engine,
	}
}

// HandleWebSocket upgrades the HTTP connection and processes messages.
func (h *Handler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer func() { _ = conn.Close() }()

	h.mu.Lock()
	h.conn = conn
	h.mu.Unlock()

	log.Println("WebSocket: Client connected")

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			log.Printf("WebSocket read error: %v", err)
			break
		}

		var msg InboundMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			log.Printf("WebSocket: invalid JSON: %v", err)
			continue
		}

		switch msg.Type {
		case MsgDOMSnapshot:
			go h.handleDOMSnapshot(conn, msg)
		case MsgNavigate:
			go h.handleNavigate(conn, msg)
		default:
			log.Printf("WebSocket: unknown message type: %s", msg.Type)
		}
	}
}

func (h *Handler) handleDOMSnapshot(conn *websocket.Conn, msg InboundMessage) {
	ctx := context.Background()

	h.sendMessage(conn, OutboundMessage{
		Type: MsgStatus, Message: "Generating semantic schema...", Stage: "cartographer",
	})

	// Decode screenshot from base64
	var screenshotBytes []byte
	if msg.Screenshot != "" {
		var err error
		screenshotBytes, err = base64.StdEncoding.DecodeString(msg.Screenshot)
		if err != nil {
			log.Printf("Failed to decode screenshot: %v", err)
		}
	}

	mimeType := "image/png"

	schemaJSON, err := h.Cartographer.GenerateSchema(ctx, screenshotBytes, mimeType, msg.Summary)
	if err != nil {
		log.Printf("Cartographer failed: %v", err)
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, Message: "Schema generation failed: " + err.Error(), Stage: "error",
		})
		return
	}

	log.Printf("Cartographer generated schema: %s", schemaJSON)

	if err := h.Engine.ApplySchema(schemaJSON); err != nil {
		log.Printf("Engine apply failed: %v", err)
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, Message: "Engine failed: " + err.Error(), Stage: "error",
		})
		return
	}

	h.Navigator.SetEngine(h.Engine)

	var schema any
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		log.Printf("Failed to parse schema JSON for response: %v", err)
	}
	h.sendMessage(conn, OutboundMessage{Type: MsgSchemaReady, Schema: schema})
}

func (h *Handler) handleNavigate(conn *websocket.Conn, msg InboundMessage) {
	ctx := context.Background()

	h.sendMessage(conn, OutboundMessage{
		Type: MsgStatus, Message: "Navigating: " + msg.Intent, Stage: "navigator",
	})

	action, textResponse, err := h.Navigator.HandleIntent(ctx, msg.Intent)
	if err != nil {
		log.Printf("Navigator failed: %v", err)
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, Message: "Navigation failed: " + err.Error(), Stage: "error",
		})
		return
	}

	if action != nil {
		h.sendMessage(conn, OutboundMessage{
			Type:    MsgExecuteAction,
			MacheID: action.MacheID,
			Action:  action.Action,
		})
	} else if textResponse != "" {
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, Message: textResponse, Stage: "navigator",
		})
	}
}

func (h *Handler) sendMessage(conn *websocket.Conn, msg OutboundMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal message: %v", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("Failed to send message: %v", err)
	}
}

// HandleNavigateHTTP provides a POST /navigate endpoint for curl/UI testing.
func (h *Handler) HandleNavigateHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Intent string `json:"intent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	action, textResponse, err := h.Navigator.HandleIntent(ctx, req.Intent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// If there's an action, also send it to the browser via WebSocket
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if action != nil && conn != nil {
		h.sendMessage(conn, OutboundMessage{
			Type:    MsgExecuteAction,
			MacheID: action.MacheID,
			Action:  action.Action,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"action":   action,
		"response": textResponse,
	}); err != nil {
		log.Printf("Failed to encode navigate response: %v", err)
	}
}
