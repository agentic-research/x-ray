package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Config holds all agentd configuration.
type Config struct {
	Port           string             `yaml:"port"`
	Gemini         GeminiConfig       `yaml:"gemini"`
	Cartographer   CartographerConfig `yaml:"cartographer"`
	Navigator      NavigatorConfig    `yaml:"navigator"`
	Ollama         OllamaConfig       `yaml:"ollama"`
	Timeouts       TimeoutsConfig     `yaml:"timeouts"`
	Voice          VoiceConfig        `yaml:"voice"` // Live API voice settings
	Database       DatabaseConfig     `yaml:"database"`
	EnableNFSMount bool               `yaml:"nfs_mount"`  // Mount page graph as NFS via mache
	CFBrowser      CFBrowserConfig    `yaml:"cf_browser"` // Cloudflare Browser Rendering
}

// CFBrowserConfig holds Cloudflare Browser Rendering settings.
type CFBrowserConfig struct {
	URL   string `yaml:"url"`   // CF Worker URL (e.g., https://xray-browser.your-account.workers.dev)
	Token string `yaml:"token"` // Bearer token for worker auth
}

// GeminiConfig holds Gemini API settings.
type GeminiConfig struct {
	Model        string `yaml:"model"`
	LiveModel    string `yaml:"live_model"`
	PlannerModel string `yaml:"planner_model"`
}

// VoiceConfig holds Live API voice session settings.
type VoiceConfig struct {
	Language string `yaml:"language"` // BCP-47 language code (e.g., "en-US", "ja-JP")
	Voice    string `yaml:"voice"`    // Prebuilt voice name (e.g., "Aoede", "Charon", "Kore")
}

// SupportedLanguages returns the list of languages supported by Gemini Live API.
func SupportedLanguages() []string {
	return []string{
		"af-ZA", "am-ET", "ar-XA", "bg-BG", "bn-IN", "ca-ES", "cmn-CN", "cmn-TW",
		"cs-CZ", "da-DK", "de-DE", "el-GR", "en-AU", "en-GB", "en-IN", "en-US",
		"es-ES", "es-US", "eu-ES", "fi-FI", "fil-PH", "fr-CA", "fr-FR", "gl-ES",
		"gu-IN", "he-IL", "hi-IN", "hu-HU", "id-ID", "is-IS", "it-IT", "ja-JP",
		"kn-IN", "ko-KR", "lt-LT", "lv-LV", "ml-IN", "mr-IN", "ms-MY", "nb-NO",
		"nl-NL", "pl-PL", "pt-BR", "pt-PT", "ro-RO", "ru-RU", "sk-SK", "sl-SI",
		"sr-RS", "sv-SE", "sw-KE", "ta-IN", "te-IN", "th-TH", "tr-TR", "uk-UA",
		"ur-PK", "vi-VN", "yue-HK", "zu-ZA",
	}
}

// SupportedVoices returns the prebuilt voice names for Gemini Live API.
func SupportedVoices() []string {
	return []string{
		"Aoede", "Charon", "Fenrir", "Kore", "Leda",
		"Orus", "Puck", "Zephyr",
	}
}

// CartographerConfig holds schema generation settings.
// CartographerConfig.Mode: "tropical", "cairn", "progressive", or "" (Gemini VLM).
type CartographerConfig struct {
	Mode        string  `yaml:"mode"`
	Gear        int     `yaml:"gear"`
	Scale       float64 `yaml:"scale"`
	Endpoint    string  `yaml:"endpoint"`
	Model       string  `yaml:"model"`
	TargetWidth int     `yaml:"target_width"` // CDP screenshot width (default: 800)
	MaxHeight   int     `yaml:"max_height"`   // CDP max page height cap (default: 16384)
}

// NavigatorConfig holds navigation/action generation settings.
type NavigatorConfig struct {
	Endpoint string `yaml:"endpoint"`
	Model    string `yaml:"model"`
	Format   string `yaml:"format"`
	CLI      bool   `yaml:"cli"`
	Mode     string `yaml:"mode"`  // "gemini-live" to use Live API WebSocket; default is REST
	Speed    string `yaml:"speed"` // "fast" (zero-shot, no continuation) or "safe" (default)
}

// IsFast returns true when the navigator is configured for low-latency voice mode.
func (n NavigatorConfig) IsFast() bool {
	return n.Speed == "fast"
}

// OllamaConfig holds Ollama-specific request defaults shared by
// both Cartographer and Navigator when talking to a local model.
type OllamaConfig struct {
	KeepAlive int `yaml:"keep_alive"` // -1 = keep model loaded indefinitely
	NumGPU    int `yaml:"num_gpu"`    // 99 = offload all layers to GPU
	NumCtx    int `yaml:"num_ctx"`    // context window size
}

// Apply adds Ollama-specific fields to a request body map.
func (o OllamaConfig) Apply(reqBody map[string]any) {
	reqBody["keep_alive"] = o.KeepAlive
	reqBody["options"] = map[string]any{
		"num_gpu": o.NumGPU,
		"num_ctx": o.NumCtx,
	}
}

// TimeoutsConfig holds orchestration timeout durations (in seconds).
// Users running heavy local models may need to bump SchemaWait and Capture
// to 60–90s to prevent the Doer from giving up prematurely.
type TimeoutsConfig struct {
	SchemaWait int `yaml:"schema_wait"` // Wait for schema before navigation (default: 30)
	ScrollWait int `yaml:"scroll_wait"` // Wait for DOM update after scroll (default: 10)
	Summary    int `yaml:"summary"`     // Wait for SUMMARY_RESPONSE (default: 10)
	Overlay    int `yaml:"overlay"`     // Wait for OVERLAY_DRAWN (default: 10)
	Capture    int `yaml:"capture"`     // Overall captureGo deadline (default: 30)
	LayerTree  int `yaml:"layer_tree"`  // Wait for LayerTree event (default: 2)
}

// Dur converts an int-seconds field to time.Duration.
func Dur(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}

// DatabaseConfig holds persistence settings.
type DatabaseConfig struct {
	Path string `yaml:"path"`
}

func defaults() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Port: "8080",
		Gemini: GeminiConfig{
			Model:     "gemini-2.5-flash",
			LiveModel: "gemini-2.5-flash-native-audio-preview-12-2025",
		},
		Cartographer: CartographerConfig{
			Mode:        "tropical",
			Gear:        5,
			Scale:       10.0,
			Model:       "llava:13b",
			TargetWidth: 800,
			MaxHeight:   16384,
		},
		Navigator: NavigatorConfig{
			Model:  "functiongemma:270m",
			Format: "openai",
		},
		Ollama: OllamaConfig{
			KeepAlive: -1,
			NumGPU:    99,
			NumCtx:    32768,
		},
		Timeouts: TimeoutsConfig{
			SchemaWait: 30,
			ScrollWait: 10,
			Summary:    10,
			Overlay:    10,
			Capture:    30,
			LayerTree:  2,
		},
		Database: DatabaseConfig{
			Path: filepath.Join(home, ".xray", "schemas.db"),
		},
	}
}

// ConfigPath returns the path to the config file.
// Respects XRAY_CONFIG_FILE env var override.
func ConfigPath() (string, error) {
	if p := os.Getenv("XRAY_CONFIG_FILE"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".agentic-research", "x-ray", "config.yaml"), nil
}

// LoadEnv loads environment variables from .envrc if present.
// Called automatically by LoadConfig; also usable standalone by binaries
// that need env vars but not the full YAML config.
func LoadEnv() {
	_ = godotenv.Load(".envrc")
}

// LoadConfig loads configuration with precedence: defaults < YAML < env vars.
// Never returns nil -- on error returns defaults alongside the error.
func LoadConfig() (*Config, error) {
	LoadEnv()
	cfg := defaults()

	path, err := ConfigPath()
	if err != nil {
		return cfg, err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if writeErr := writeDefaultConfig(path, cfg); writeErr != nil {
			return cfg, fmt.Errorf("config: create %s: %w", path, writeErr)
		}
	} else if err != nil {
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	} else {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return cfg, fmt.Errorf("config: parse %s: %w", path, err)
		}
		cfg.Database.Path = expandHome(cfg.Database.Path)
	}

	applyEnvOverrides(cfg)
	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("PORT"); v != "" {
		cfg.Port = v
	}
	if v := os.Getenv("GEMINI_MODEL"); v != "" {
		cfg.Gemini.Model = v
	}
	if v := os.Getenv("GEMINI_LIVE_MODEL"); v != "" {
		cfg.Gemini.LiveModel = v
	}
	if v := os.Getenv("PLANNER_MODEL"); v != "" {
		cfg.Gemini.PlannerModel = v
	}
	if v := os.Getenv("CARTOGRAPHER_MODE"); v != "" {
		cfg.Cartographer.Mode = v
	}
	if v := os.Getenv("CARTOGRAPHER_GEAR"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Cartographer.Gear = n
		}
	}
	if v := os.Getenv("CARTOGRAPHER_SCALE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Cartographer.Scale = f
		}
	}
	if v := os.Getenv("CARTOGRAPHER_ENDPOINT"); v != "" {
		cfg.Cartographer.Endpoint = v
	}
	if v := os.Getenv("CARTOGRAPHER_MODEL"); v != "" {
		cfg.Cartographer.Model = v
	}
	if v := os.Getenv("NAVIGATOR_ENDPOINT"); v != "" {
		cfg.Navigator.Endpoint = v
	}
	if v := os.Getenv("NAVIGATOR_MODEL"); v != "" {
		cfg.Navigator.Model = v
	}
	if v := os.Getenv("NAVIGATOR_FORMAT"); v != "" {
		cfg.Navigator.Format = v
	}
	if v := os.Getenv("NAVIGATOR_CLI"); v != "" {
		cfg.Navigator.CLI = v == "1"
	}
	if v := os.Getenv("NAVIGATOR_MODE"); v != "" {
		cfg.Navigator.Mode = v
	}
	if v := os.Getenv("NAV_SPEED"); v != "" {
		cfg.Navigator.Speed = v
	}
	if v := os.Getenv("OLLAMA_KEEP_ALIVE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Ollama.KeepAlive = n
		}
	}
	if v := os.Getenv("OLLAMA_NUM_GPU"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Ollama.NumGPU = n
		}
	}
	if v := os.Getenv("OLLAMA_NUM_CTX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Ollama.NumCtx = n
		}
	}
	if v := os.Getenv("CARTOGRAPHER_TARGET_WIDTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Cartographer.TargetWidth = n
		}
	}
	if v := os.Getenv("CARTOGRAPHER_MAX_HEIGHT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Cartographer.MaxHeight = n
		}
	}
	if v := os.Getenv("TIMEOUT_SCHEMA_WAIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Timeouts.SchemaWait = n
		}
	}
	if v := os.Getenv("TIMEOUT_CAPTURE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Timeouts.Capture = n
		}
	}
	if v := os.Getenv("XRAY_DB"); v != "" {
		cfg.Database.Path = expandHome(v)
	}
	if os.Getenv("XRAY_NFS_MOUNT") == "true" || os.Getenv("XRAY_NFS_MOUNT") == "1" {
		cfg.EnableNFSMount = true
	}
	if v := os.Getenv("VOICE_LANGUAGE"); v != "" {
		cfg.Voice.Language = v
	}
	if v := os.Getenv("VOICE_NAME"); v != "" {
		cfg.Voice.Voice = v
	}
	if v := os.Getenv("CF_BROWSER_URL"); v != "" {
		cfg.CFBrowser.URL = v
	}
	if v := os.Getenv("CF_BROWSER_TOKEN"); v != "" {
		cfg.CFBrowser.Token = v
	}
}

func expandHome(p string) string {
	if len(p) < 2 || p[:2] != "~/" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

func writeDefaultConfig(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	template := `# X-Ray Agentd Configuration
# Generated on first run. Edit freely -- env vars override these values.

# HTTP port for the agentd server.
port: "` + cfg.Port + `"

gemini:
  # Primary Gemini model for schema generation and planning.
  model: "` + cfg.Gemini.Model + `"
  # Gemini Live model for real-time voice sessions.
  live_model: "` + cfg.Gemini.LiveModel + `"
  # Planner model override. Leave empty to inherit gemini.model.
  planner_model: ""

cartographer:
  # Schema generation mode: "tropical" (algebraic), "cairn" (Leech), "progressive" (staged), or "" (Gemini VLM).
  mode: "` + cfg.Cartographer.Mode + `"
  # CairnCartographer gear level (1-8). Only used when mode=cairn.
  gear: ` + strconv.Itoa(cfg.Cartographer.Gear) + `
  # CairnCartographer spatial scale. Only used when mode=cairn.
  scale: ` + strconv.FormatFloat(cfg.Cartographer.Scale, 'f', 1, 64) + `
  # Local VLM endpoint (Ollama). Set to enable local cartographer.
  endpoint: ""
  # Local VLM model name.
  model: "` + cfg.Cartographer.Model + `"
  # CDP screenshot width. Lower = faster screenshots, less detail for VLM.
  target_width: ` + strconv.Itoa(cfg.Cartographer.TargetWidth) + `
  # CDP max page height cap (infinite-scroll guard).
  max_height: ` + strconv.Itoa(cfg.Cartographer.MaxHeight) + `

navigator:
  # Mode: "" (Gemini REST, default), "gemini-live" (Live API WebSocket).
  mode: ""
  # Local LLM endpoint (Ollama / llama.cpp). Set to enable local navigator.
  endpoint: ""
  # Local LLM model name.
  model: "` + cfg.Navigator.Model + `"
  # Format: "openai" or "gemma".
  format: "` + cfg.Navigator.Format + `"
  # CLI mode for Gemma (space-delimited commands, GBNF grammar).
  cli: false

# Ollama-specific request defaults (shared by cartographer + navigator).
ollama:
  # -1 = keep model loaded indefinitely (avoids reload latency).
  keep_alive: ` + strconv.Itoa(cfg.Ollama.KeepAlive) + `
  # 99 = offload all layers to GPU.
  num_gpu: ` + strconv.Itoa(cfg.Ollama.NumGPU) + `
  # Context window size.
  num_ctx: ` + strconv.Itoa(cfg.Ollama.NumCtx) + `

timeouts:
  # Seconds to wait for schema before navigation. Bump to 60-90 for heavy local models.
  schema_wait: ` + strconv.Itoa(cfg.Timeouts.SchemaWait) + `
  # Seconds to wait for DOM update after scroll.
  scroll_wait: ` + strconv.Itoa(cfg.Timeouts.ScrollWait) + `
  # Seconds to wait for SUMMARY_RESPONSE from content script.
  summary: ` + strconv.Itoa(cfg.Timeouts.Summary) + `
  # Seconds to wait for overlay draw confirmation.
  overlay: ` + strconv.Itoa(cfg.Timeouts.Overlay) + `
  # Overall captureGo deadline. Bump to 60-90 for heavy local models.
  capture: ` + strconv.Itoa(cfg.Timeouts.Capture) + `
  # Seconds to wait for LayerTree.layerTreeDidChange event.
  layer_tree: ` + strconv.Itoa(cfg.Timeouts.LayerTree) + `

database:
  # SQLite path for schema cache. Supports ~ for home directory.
  path: "~/.xray/schemas.db"
`
	return os.WriteFile(path, []byte(template), 0o644)
}
