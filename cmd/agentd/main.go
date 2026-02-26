package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jamesgardner/x-ray/internal/api"
	"github.com/jamesgardner/x-ray/internal/audio"
	"github.com/jamesgardner/x-ray/internal/cartographer"
	"github.com/jamesgardner/x-ray/internal/iterm"
	"github.com/jamesgardner/x-ray/internal/navigator"
	"github.com/joho/godotenv"
	"google.golang.org/genai"
)

func main() {
	voiceFlag := flag.Bool("voice", false, "Enable native voice mode (requires sox)")
	flag.Parse()

	log.Println("Starting X-Ray Agentd")

	// Load .envrc for GOOGLE_GEMINI_API_KEY / GOOGLE_API_KEY
	if err := godotenv.Load(".envrc"); err != nil {
		log.Println("Note: No .envrc file found, using environment")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	// Cartographer: default to Gemini, override with CARTOGRAPHER_ENDPOINT for local VLM.
	var cart api.SchemaGenerator
	if ep := os.Getenv("CARTOGRAPHER_ENDPOINT"); ep != "" {
		cartModel := os.Getenv("CARTOGRAPHER_MODEL")
		if cartModel == "" {
			cartModel = "llava:13b"
		}
		cart = &cartographer.OllamaAgent{Endpoint: ep, Model: cartModel}
		log.Printf("Cartographer: using local VLM %s at %s", cartModel, ep)
	} else {
		cart = cartographer.NewAgent(client, model)
	}

	// Navigator model: default to Gemini, override with NAVIGATOR_ENDPOINT for local SLM.
	var navGen navigator.ContentGenerator = &navigator.GeminiGenerator{Client: client}
	navModel := model

	if ep := os.Getenv("NAVIGATOR_ENDPOINT"); ep != "" {
		navModel = os.Getenv("NAVIGATOR_MODEL")
		if navModel == "" {
			navModel = "functiongemma:270m"
		}
		format := os.Getenv("NAVIGATOR_FORMAT") // "gemma" or "openai" (default)
		if format == "gemma" {
			navGen = &navigator.GemmaGenerator{Endpoint: ep, Model: navModel}
			log.Printf("Navigator: using Gemma model %s at %s (native function calling)", navModel, ep)
		} else {
			navGen = &navigator.OllamaGenerator{Endpoint: ep, Model: navModel}
			log.Printf("Navigator: using local model %s at %s (OpenAI format)", navModel, ep)
		}

		// Pre-warm: send a throwaway request so Ollama loads the model into GPU
		// memory before the first real intent arrives.
		go func() {
			log.Printf("Navigator: pre-warming model %s...", navModel)
			warmCtx, warmCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer warmCancel()
			_, err := navGen.GenerateContent(warmCtx, navModel, []*genai.Content{
				{Role: "user", Parts: []*genai.Part{{Text: "hi"}}},
			}, nil)
			if err != nil {
				log.Printf("Navigator: pre-warm failed (non-fatal): %v", err)
			} else {
				log.Printf("Navigator: model %s pre-warmed and ready", navModel)
			}
		}()
	}

	// Schema cache persistence.
	dbPath := os.Getenv("XRAY_DB")
	if dbPath == "" {
		home, _ := os.UserHomeDir()
		dbPath = filepath.Join(home, ".xray", "schemas.db")
	}

	// Per-tab Engine + Navigator are created on demand inside Handler.
	handler := api.NewHandler(cart, navGen, liveClient, navModel, liveModel, dbPath)
	handler.SetOpenBrowserFunc(func(url string) {
		_ = exec.Command("open", "-a", "Google Chrome", url).Start()
		// Bring Chrome to foreground — "open -a" doesn't always focus the window.
		_ = exec.Command("osascript", "-e", `tell application "Google Chrome" to activate`).Start()
	})

	// iTerm2 bridge: connect if iTerm is running. Non-fatal if unavailable.
	bridge := iterm.NewBridge()
	if err := bridge.Start(ctx); err != nil {
		log.Printf("iTerm2 bridge: %v (terminal features disabled)", err)
	} else {
		log.Println("iTerm2 bridge: connected — terminal sessions available at /iterm/")
		handler.SetTermBridge(bridge)
	}

	http.HandleFunc("/ws", handler.HandleWebSocket)
	http.HandleFunc("/navigate", handler.HandleNavigateHTTP)
	http.HandleFunc("/voice", handler.HandleVoice)
	http.HandleFunc("/voice-ui", serveVoiceUI)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	port = ":" + port

	// Start HTTP server in background.
	server := &http.Server{Addr: port}
	go func() {
		log.Printf("Listening on %s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	if *voiceFlag {
		if !audio.Available() {
			log.Fatal("Voice mode requires sox (brew install sox)")
		}
		// Trap signals so Ctrl+C cancels the voice loop context.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-sigCh
			log.Printf("Received %s, shutting down...", sig)
			cancel()
		}()
		runVoiceLoop(ctx, cancel, handler)
	} else {
		// No voice mode — block until signal, then shut down gracefully.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("Received %s, shutting down...", sig)

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP shutdown error: %v", err)
		}
		cancel()
	}
}

// runVoiceLoop implements enter-to-talk PTT using sox for native mic/speaker.
func runVoiceLoop(ctx context.Context, cancel context.CancelFunc, handler *api.Handler) {
	mic := make(chan []byte, 64)
	speaker := make(chan []byte, 64)
	textIn := make(chan string, 8)

	// Speaker goroutine: play audio chunks via sox.
	go func() {
		player := audio.NewPlayer()
		pipe, err := player.Start()
		if err != nil {
			log.Printf("Voice: failed to start speaker: %v", err)
			return
		}
		defer func() { _ = player.Stop() }()
		for chunk := range speaker {
			if _, err := pipe.Write(chunk); err != nil {
				log.Printf("Voice: speaker write error: %v", err)
				return
			}
		}
	}()

	// Voice loop goroutine: connects to Gemini Live.
	go func() {
		if err := handler.StartVoiceLoop(ctx, mic, speaker, textIn); err != nil {
			log.Printf("Voice: loop ended: %v", err)
		}
		cancel()
	}()

	// Stdin PTT loop.
	fmt.Println()
	fmt.Println("  Agent OS Ready.")
	fmt.Println("  Press ENTER to open voice channel.")
	fmt.Println("  Type text + ENTER to send a text intent.")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	var recorder *audio.Recorder
	var recorderPipe io.ReadCloser
	recording := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line != "" {
			// Non-empty line: send as text intent.
			select {
			case textIn <- line:
				log.Printf("Voice: sent text intent: %s", line)
			default:
				log.Println("Voice: text channel full, dropping")
			}
			continue
		}

		// Empty line (just Enter): toggle recording.
		if !recording {
			// Start recording.
			recorder = audio.NewRecorder()
			var err error
			recorderPipe, err = recorder.Start()
			if err != nil {
				log.Printf("Voice: failed to start recorder: %v", err)
				continue
			}
			recording = true
			fmt.Println("  [ Listening... ]")

			// Feed mic channel from recorder pipe.
			go func(pipe io.ReadCloser) {
				buf := make([]byte, 3200) // 100ms of 16kHz 16-bit mono
				for {
					n, err := pipe.Read(buf)
					if n > 0 {
						chunk := make([]byte, n)
						copy(chunk, buf[:n])
						select {
						case mic <- chunk:
						case <-ctx.Done():
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(recorderPipe)
		} else {
			// Stop recording.
			recording = false
			if recorder != nil {
				_ = recorder.Stop()
				recorder = nil
			}
			// Send a nil chunk to signal end of stream to the voice loop
			select {
			case mic <- nil:
			default:
			}
			fmt.Println("  [ Paused. Press ENTER to resume. ]")
		}
	}
}

func serveVoiceUI(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "static/voice.html")
}
