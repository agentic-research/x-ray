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
	"strings"
	"syscall"
	"time"

	"github.com/agentic-research/x-ray/internal/api"
	"github.com/agentic-research/x-ray/internal/audio"
	"github.com/agentic-research/x-ray/internal/cartographer"
	"github.com/agentic-research/x-ray/internal/cfbrowser"
	"github.com/agentic-research/x-ray/internal/config"
	"github.com/agentic-research/x-ray/internal/iterm"
	"github.com/agentic-research/x-ray/internal/navigator"
	"google.golang.org/genai"
)

func main() {
	voiceFlag := flag.Bool("voice", false, "Enable native voice mode (requires sox)")
	listLangs := flag.Bool("languages", false, "List supported voice languages and exit")
	listVoices := flag.Bool("voices", false, "List available voice names and exit")
	flag.Parse()

	if *listLangs {
		fmt.Println("Supported languages (set via VOICE_LANGUAGE env var):")
		for _, l := range config.SupportedLanguages() {
			fmt.Println("  " + l)
		}
		return
	}
	if *listVoices {
		fmt.Println("Available voices (set via VOICE_NAME env var):")
		for _, v := range config.SupportedVoices() {
			fmt.Println("  " + v)
		}
		return
	}

	log.Println("Starting X-Ray Agentd")

	cfg, cfgErr := config.LoadConfig()
	if cfgErr != nil {
		log.Printf("Config warning: %v (using defaults)", cfgErr)
	} else {
		cfgPath, _ := config.ConfigPath()
		log.Printf("Config loaded from %s", cfgPath)
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

	plannerModel := cfg.Gemini.PlannerModel
	if plannerModel == "" {
		plannerModel = cfg.Gemini.Model
	}

	// Cartographer: tropical (algebraic, no LLM) > cairn (Leech) > local VLM > Gemini.
	var cart api.SchemaGenerator
	switch cfg.Cartographer.Mode {
	case "tropical":
		cart = &cartographer.TropicalCartographer{}
		log.Println("Cartographer: TropicalCartographer (algebraic)")
	case "cairn":
		cairnCart := &cartographer.CairnCartographer{Gear: cfg.Cartographer.Gear, Scale: cfg.Cartographer.Scale}
		if os.Getenv("CAIRN_SHEAF") == "1" {
			cairnCart.SheafFolding = true
		}
		if os.Getenv("CAIRN_CURVATURE") == "1" {
			cairnCart.CurvatureDetection = true
		}
		cart = cairnCart
		log.Printf("Cartographer: CairnCartographer (gear=%d, scale=%.1f, sheaf=%v, curvature=%v)",
			cfg.Cartographer.Gear, cfg.Cartographer.Scale, cairnCart.SheafFolding, cairnCart.CurvatureDetection)
	default:
		if cfg.Cartographer.Endpoint != "" {
			cart = &cartographer.OllamaAgent{Endpoint: cfg.Cartographer.Endpoint, Model: cfg.Cartographer.Model, Ollama: cfg.Ollama}
			log.Printf("Cartographer: local VLM %s at %s", cfg.Cartographer.Model, cfg.Cartographer.Endpoint)
		} else {
			cart = cartographer.NewAgent(client, cfg.Gemini.Model)
		}
	}

	// Navigator model: default to Gemini REST, override with navigator.mode or endpoint.
	// NAVIGATOR_MODEL overrides the model for all backends (REST, Live, Ollama).
	var navGen navigator.ContentGenerator = &navigator.GeminiGenerator{Client: client}
	navModel := cfg.Gemini.Model
	if cfg.Navigator.Model != "" {
		navModel = cfg.Navigator.Model
	}

	if cfg.Navigator.Mode == "gemini-live" {
		navModel = cfg.Gemini.LiveModel
		navGen = &navigator.GeminiLiveGenerator{Client: liveClient, Model: navModel}
		log.Printf("Navigator: using Gemini Live API (model %s)", navModel)
	} else if cfg.Navigator.Endpoint != "" {
		// navModel already set above from NAVIGATOR_MODEL or default.
		if cfg.Navigator.Format == "gemma" {
			navGen = &navigator.GemmaGenerator{Endpoint: cfg.Navigator.Endpoint, Model: navModel, Ollama: cfg.Ollama, CLIMode: cfg.Navigator.CLI}
			if cfg.Navigator.CLI {
				log.Printf("Navigator: using Gemma model %s at %s (CLI mode + GBNF grammar)", navModel, cfg.Navigator.Endpoint)
			} else {
				log.Printf("Navigator: using Gemma model %s at %s (native function calling)", navModel, cfg.Navigator.Endpoint)
			}
		} else {
			navGen = &navigator.OllamaGenerator{Endpoint: cfg.Navigator.Endpoint, Model: navModel, Ollama: cfg.Ollama}
			log.Printf("Navigator: using local model %s at %s (OpenAI format)", navModel, cfg.Navigator.Endpoint)
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

	// Per-tab Engine + Navigator are created on demand inside Handler.
	handler := api.NewHandler(cart, navGen, client, liveClient, navModel, cfg.Gemini.LiveModel, plannerModel, cfg.Database.Path)
	handler.Timeouts = cfg.Timeouts
	handler.CDPTargetWidth = float64(cfg.Cartographer.TargetWidth)
	handler.CDPMaxHeight = float64(cfg.Cartographer.MaxHeight)
	handler.EnableNFSMount = cfg.EnableNFSMount
	handler.NavSpeed = cfg.Navigator.Speed
	handler.VoiceLanguage = cfg.Voice.Language
	handler.VoiceName = cfg.Voice.Voice
	if cfg.EnableNFSMount {
		if err := handler.StartNFS(); err != nil {
			log.Printf("NFS mount failed (non-fatal): %v", err)
		} else {
			defer handler.StopNFS()
		}
	}

	// Cloudflare Browser Rendering: when CF_BROWSER_URL is set, use headless
	// Chromium on CF edge instead of the Chrome extension.
	if cfg.CFBrowser.URL != "" {
		cfClient := cfbrowser.NewClient(cfg.CFBrowser.URL, cfg.CFBrowser.Token)
		handler.SetBrowserBackend(cfClient)
		log.Printf("Browser backend: Cloudflare Worker at %s", cfg.CFBrowser.URL)
	}

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
	http.HandleFunc("/doer", handler.HandleDoerHTTP)
	http.HandleFunc("/interactions", handler.HandleDoerHTTP)
	http.HandleFunc("/status", handler.HandleStatus)
	http.HandleFunc("/interactions/status", handler.HandleStatus)
	http.HandleFunc("/agent/task", handler.HandleAgentTask)
	http.HandleFunc("/agent/reset", handler.HandleAgentReset)
	http.HandleFunc("/voice", handler.HandleVoice)
	http.HandleFunc("/voice-ui", serveVoiceUI)

	port := ":" + cfg.Port

	// Write PID file so the process can be found and stopped later.
	pidFile := pidFilePath()
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644); err != nil {
		log.Printf("Warning: could not write PID file %s: %v", pidFile, err)
	} else {
		log.Printf("PID file: %s", pidFile)
	}

	// Start HTTP server in background.
	server := &http.Server{Addr: port}
	go func() {
		log.Printf("Listening on %s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Trap signals so Ctrl+C triggers clean shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received %s, shutting down...", sig)
		cancel()
		// Close stdin to unblock the PTT scanner loop in voice mode.
		_ = os.Stdin.Close()
		// Remove PID file on clean shutdown.
		_ = os.Remove(pidFile)
		// Graceful HTTP shutdown.
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP shutdown error: %v", err)
		}
	}()

	if *voiceFlag {
		if !audio.Available() {
			log.Fatal("Voice mode requires sox (brew install sox)")
		}
		runVoiceLoop(ctx, cancel, handler)
	} else {
		// No voice mode — block until context is cancelled.
		<-ctx.Done()
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

	// Clean up recorder if stdin closed while recording.
	if recording && recorder != nil {
		_ = recorder.Stop()
	}
	close(mic)
	close(speaker)
}

func serveVoiceUI(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "static/voice.html")
}

// pidFilePath returns ~/.xray/agentd.pid.
func pidFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/xray-agentd.pid"
	}
	dir := home + "/.xray"
	_ = os.MkdirAll(dir, 0o755)
	return dir + "/agentd.pid"
}
