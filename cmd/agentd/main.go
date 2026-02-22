package main

import (
	"log"
	"net/http"

	"github.com/jamesgardner/x-ray/internal/api"
)

func main() {
	log.Println("Starting X-Ray Agentd (Mache Two-Stage Architecture)")

	// 1. Initialize Mache Engine
	// macheEngine := mache.NewEngine()

	// 2. Initialize Agents
	// cartographerAgent := cartographer.NewAgent()
	// navigatorAgent := navigator.NewAgent()

	// 3. Set up WebSocket API for the Browser Extension
	http.HandleFunc("/ws", api.HandleWebSocketConnections)

	port := ":8080"
	log.Printf("Listening on %s", port)
	err := http.ListenAndServe(port, nil)
	if err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
