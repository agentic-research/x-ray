package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config holds all agentd configuration.
type Config struct {
	Port         string             `yaml:"port"`
	Gemini       GeminiConfig       `yaml:"gemini"`
	Cartographer CartographerConfig `yaml:"cartographer"`
	Navigator    NavigatorConfig    `yaml:"navigator"`
	Ollama       OllamaConfig       `yaml:"ollama"`
	Database     DatabaseConfig     `yaml:"database"`
}

// GeminiConfig holds Gemini API settings.
type GeminiConfig struct {
	Model        string `yaml:"model"`
	LiveModel    string `yaml:"live_model"`
	PlannerModel string `yaml:"planner_model"`
}

// CartographerConfig holds schema generation settings.
type CartographerConfig struct {
	Mode     string  `yaml:"mode"`
	Gear     int     `yaml:"gear"`
	Scale    float64 `yaml:"scale"`
	Endpoint string  `yaml:"endpoint"`
	Model    string  `yaml:"model"`
}

// NavigatorConfig holds navigation/action generation settings.
type NavigatorConfig struct {
	Endpoint string `yaml:"endpoint"`
	Model    string `yaml:"model"`
	Format   string `yaml:"format"`
	CLI      bool   `yaml:"cli"`
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
			Mode:  "tropical",
			Gear:  5,
			Scale: 10.0,
			Model: "llava:13b",
		},
		Navigator: NavigatorConfig{
			Model:  "functiongemma:270m",
			Format: "openai",
		},
		Ollama: OllamaConfig{
			KeepAlive: -1,
			NumGPU:    99,
			NumCtx:    8192,
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

// LoadConfig loads configuration with precedence: defaults < YAML < env vars.
// Never returns nil -- on error returns defaults alongside the error.
func LoadConfig() (*Config, error) {
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
	if v := os.Getenv("XRAY_DB"); v != "" {
		cfg.Database.Path = expandHome(v)
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
  # Schema generation mode: "tropical" (algebraic), "cairn" (Leech), or "" (Gemini VLM).
  mode: "` + cfg.Cartographer.Mode + `"
  # CairnCartographer gear level (1-8). Only used when mode=cairn.
  gear: ` + strconv.Itoa(cfg.Cartographer.Gear) + `
  # CairnCartographer spatial scale. Only used when mode=cairn.
  scale: ` + strconv.FormatFloat(cfg.Cartographer.Scale, 'f', 1, 64) + `
  # Local VLM endpoint (Ollama). Set to enable local cartographer.
  endpoint: ""
  # Local VLM model name.
  model: "` + cfg.Cartographer.Model + `"

navigator:
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

database:
  # SQLite path for schema cache. Supports ~ for home directory.
  path: "~/.xray/schemas.db"
`
	return os.WriteFile(path, []byte(template), 0o644)
}
