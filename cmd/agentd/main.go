package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/jamesgardner/x-ray/internal/api"
	"github.com/jamesgardner/x-ray/internal/cartographer"
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

	// Live API requires APIVersion to be set. Create a dedicated client.
	liveClient, err := genai.NewClient(ctx, &genai.ClientConfig{
		HTTPOptions: genai.HTTPOptions{APIVersion: "v1alpha"},
	})
	if err != nil {
		log.Fatalf("Failed to initialize Gemini Live client: %v", err)
	}

	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	liveModel := os.Getenv("GEMINI_LIVE_MODEL")
	if liveModel == "" {
		liveModel = "gemini-2.5-flash-native-audio-preview-12-2025"
	}

	// Cartographer is stateless — shared across all tabs.
	cart := cartographer.NewAgent(client, model)

	// Navigator model: default to Gemini, override with NAVIGATOR_ENDPOINT for local SLM.
	var navGen navigator.ContentGenerator = &navigator.GeminiGenerator{Client: client}
	navModel := model

	if ep := os.Getenv("NAVIGATOR_ENDPOINT"); ep != "" {
		navModel = os.Getenv("NAVIGATOR_MODEL")
		if navModel == "" {
			navModel = "llama3.2"
		}
		navGen = &navigator.OllamaGenerator{Endpoint: ep, Model: navModel}
		log.Printf("Navigator: using local model %s at %s", navModel, ep)
	}

	// Per-tab Engine + Navigator are created on demand inside Handler.
	handler := api.NewHandler(cart, navGen, liveClient, navModel, liveModel)

	http.HandleFunc("/ws", handler.HandleWebSocket)
	http.HandleFunc("/navigate", handler.HandleNavigateHTTP)
	http.HandleFunc("/voice", handler.HandleVoice)
	http.HandleFunc("/voice-ui", serveVoiceUI)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	port = ":" + port
	log.Printf("Listening on %s", port)
	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func serveVoiceUI(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "static/voice.html")
}
