package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/jamesgardner/x-ray/internal/api"
	"github.com/jamesgardner/x-ray/internal/cartographer"
	"github.com/jamesgardner/x-ray/internal/mache"
	"github.com/jamesgardner/x-ray/internal/navigator"
	"github.com/joho/godotenv"
	"google.golang.org/genai"
)

func main() {
	log.Println("Starting X-Ray Agentd")

	// Load .envrc for GOOGLE_GEMINI_API_KEY / GOOGLE_API_KEY
	if err := godotenv.Load(".envrc"); err != nil {
		log.Println("Note: No .envrc file found, using environment")
	}

	ctx := context.Background()
	client, err := genai.NewClient(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to initialize Gemini client: %v", err)
	}

	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	engine := mache.NewEngine()
	cart := cartographer.NewAgent(client, model)
	nav := navigator.NewAgent(client, model, engine)

	handler := api.NewHandler(cart, nav, engine)

	http.HandleFunc("/ws", handler.HandleWebSocket)
	http.HandleFunc("/navigate", handler.HandleNavigateHTTP)

	port := ":8080"
	log.Printf("Listening on %s", port)
	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
