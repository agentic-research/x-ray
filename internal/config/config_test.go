package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := defaults()
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want %q", cfg.Port, "8080")
	}
	if cfg.Gemini.Model != "gemini-2.5-flash" {
		t.Errorf("Gemini.Model = %q, want %q", cfg.Gemini.Model, "gemini-2.5-flash")
	}
	if cfg.Gemini.LiveModel != "gemini-2.5-flash-native-audio-preview-12-2025" {
		t.Errorf("Gemini.LiveModel = %q, want %q", cfg.Gemini.LiveModel, "gemini-2.5-flash-native-audio-preview-12-2025")
	}
	if cfg.Gemini.PlannerModel != "" {
		t.Errorf("Gemini.PlannerModel = %q, want empty", cfg.Gemini.PlannerModel)
	}
	if cfg.Cartographer.Mode != "tropical" {
		t.Errorf("Cartographer.Mode = %q, want %q", cfg.Cartographer.Mode, "tropical")
	}
	if cfg.Cartographer.Gear != 5 {
		t.Errorf("Cartographer.Gear = %d, want 5", cfg.Cartographer.Gear)
	}
	if cfg.Cartographer.Scale != 10.0 {
		t.Errorf("Cartographer.Scale = %f, want 10.0", cfg.Cartographer.Scale)
	}
	if cfg.Cartographer.Model != "llava:13b" {
		t.Errorf("Cartographer.Model = %q, want %q", cfg.Cartographer.Model, "llava:13b")
	}
	if cfg.Navigator.Model != "functiongemma:270m" {
		t.Errorf("Navigator.Model = %q, want %q", cfg.Navigator.Model, "functiongemma:270m")
	}
	if cfg.Navigator.Format != "openai" {
		t.Errorf("Navigator.Format = %q, want %q", cfg.Navigator.Format, "openai")
	}
	if cfg.Navigator.CLI {
		t.Error("Navigator.CLI should default to false")
	}
	if !strings.HasSuffix(cfg.Database.Path, filepath.Join(".xray", "schemas.db")) {
		t.Errorf("Database.Path = %q, should end with .xray/schemas.db", cfg.Database.Path)
	}
}

func TestLoadConfig_AutoCreate(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "subdir", "config.yaml")

	t.Setenv("XRAY_CONFIG_FILE", cfgPath)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig() returned nil config")
	}

	// File should have been created.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("Config file not created: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "# X-Ray Agentd Configuration") {
		t.Error("Config file missing header comment")
	}
	if !strings.Contains(content, "gemini:") {
		t.Error("Config file missing gemini section")
	}
	if !strings.Contains(content, "cartographer:") {
		t.Error("Config file missing cartographer section")
	}
	if !strings.Contains(content, "navigator:") {
		t.Error("Config file missing navigator section")
	}
	if !strings.Contains(content, "database:") {
		t.Error("Config file missing database section")
	}
}

func TestLoadConfig_ParseYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	yamlContent := `
port: "9090"
gemini:
  model: "gemini-pro"
  live_model: "gemini-live-pro"
  planner_model: "gemini-planner"
cartographer:
  mode: "cairn"
  gear: 3
  scale: 5.5
  endpoint: "http://localhost:11434"
  model: "llava:7b"
navigator:
  endpoint: "http://localhost:11435"
  model: "codellama:7b"
  format: "gemma"
  cli: true
database:
  path: "/tmp/test.db"
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("XRAY_CONFIG_FILE", cfgPath)
	// Clear env overrides so YAML values are used.
	t.Setenv("PORT", "")
	t.Setenv("GEMINI_MODEL", "")
	t.Setenv("GEMINI_LIVE_MODEL", "")
	t.Setenv("PLANNER_MODEL", "")
	t.Setenv("CARTOGRAPHER_MODE", "")
	t.Setenv("CARTOGRAPHER_GEAR", "")
	t.Setenv("CARTOGRAPHER_SCALE", "")
	t.Setenv("CARTOGRAPHER_ENDPOINT", "")
	t.Setenv("CARTOGRAPHER_MODEL", "")
	t.Setenv("NAVIGATOR_ENDPOINT", "")
	t.Setenv("NAVIGATOR_MODEL", "")
	t.Setenv("NAVIGATOR_FORMAT", "")
	t.Setenv("NAVIGATOR_CLI", "")
	t.Setenv("XRAY_DB", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.Port != "9090" {
		t.Errorf("Port = %q, want %q", cfg.Port, "9090")
	}
	if cfg.Gemini.Model != "gemini-pro" {
		t.Errorf("Gemini.Model = %q, want %q", cfg.Gemini.Model, "gemini-pro")
	}
	if cfg.Gemini.LiveModel != "gemini-live-pro" {
		t.Errorf("Gemini.LiveModel = %q, want %q", cfg.Gemini.LiveModel, "gemini-live-pro")
	}
	if cfg.Gemini.PlannerModel != "gemini-planner" {
		t.Errorf("Gemini.PlannerModel = %q, want %q", cfg.Gemini.PlannerModel, "gemini-planner")
	}
	if cfg.Cartographer.Mode != "cairn" {
		t.Errorf("Cartographer.Mode = %q, want %q", cfg.Cartographer.Mode, "cairn")
	}
	if cfg.Cartographer.Gear != 3 {
		t.Errorf("Cartographer.Gear = %d, want 3", cfg.Cartographer.Gear)
	}
	if cfg.Cartographer.Scale != 5.5 {
		t.Errorf("Cartographer.Scale = %f, want 5.5", cfg.Cartographer.Scale)
	}
	if cfg.Cartographer.Endpoint != "http://localhost:11434" {
		t.Errorf("Cartographer.Endpoint = %q, want %q", cfg.Cartographer.Endpoint, "http://localhost:11434")
	}
	if cfg.Cartographer.Model != "llava:7b" {
		t.Errorf("Cartographer.Model = %q, want %q", cfg.Cartographer.Model, "llava:7b")
	}
	if cfg.Navigator.Endpoint != "http://localhost:11435" {
		t.Errorf("Navigator.Endpoint = %q, want %q", cfg.Navigator.Endpoint, "http://localhost:11435")
	}
	if cfg.Navigator.Model != "codellama:7b" {
		t.Errorf("Navigator.Model = %q, want %q", cfg.Navigator.Model, "codellama:7b")
	}
	if cfg.Navigator.Format != "gemma" {
		t.Errorf("Navigator.Format = %q, want %q", cfg.Navigator.Format, "gemma")
	}
	if !cfg.Navigator.CLI {
		t.Error("Navigator.CLI should be true")
	}
	if cfg.Database.Path != "/tmp/test.db" {
		t.Errorf("Database.Path = %q, want %q", cfg.Database.Path, "/tmp/test.db")
	}
}

func TestEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Write a minimal YAML with base values.
	yamlContent := `
port: "8080"
gemini:
  model: "base-model"
cartographer:
  mode: "tropical"
  gear: 5
  scale: 10.0
navigator:
  format: "openai"
database:
  path: "/tmp/base.db"
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("XRAY_CONFIG_FILE", cfgPath)
	t.Setenv("PORT", "3000")
	t.Setenv("GEMINI_MODEL", "env-model")
	t.Setenv("GEMINI_LIVE_MODEL", "env-live")
	t.Setenv("PLANNER_MODEL", "env-planner")
	t.Setenv("CARTOGRAPHER_MODE", "cairn")
	t.Setenv("CARTOGRAPHER_GEAR", "7")
	t.Setenv("CARTOGRAPHER_SCALE", "2.5")
	t.Setenv("CARTOGRAPHER_ENDPOINT", "http://env:11434")
	t.Setenv("CARTOGRAPHER_MODEL", "env-vlm")
	t.Setenv("NAVIGATOR_ENDPOINT", "http://env:11435")
	t.Setenv("NAVIGATOR_MODEL", "env-nav")
	t.Setenv("NAVIGATOR_FORMAT", "gemma")
	t.Setenv("NAVIGATOR_CLI", "1")
	t.Setenv("XRAY_DB", "/tmp/env.db")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.Port != "3000" {
		t.Errorf("Port = %q, want %q (env override)", cfg.Port, "3000")
	}
	if cfg.Gemini.Model != "env-model" {
		t.Errorf("Gemini.Model = %q, want %q", cfg.Gemini.Model, "env-model")
	}
	if cfg.Gemini.LiveModel != "env-live" {
		t.Errorf("Gemini.LiveModel = %q, want %q", cfg.Gemini.LiveModel, "env-live")
	}
	if cfg.Gemini.PlannerModel != "env-planner" {
		t.Errorf("Gemini.PlannerModel = %q, want %q", cfg.Gemini.PlannerModel, "env-planner")
	}
	if cfg.Cartographer.Mode != "cairn" {
		t.Errorf("Cartographer.Mode = %q, want %q", cfg.Cartographer.Mode, "cairn")
	}
	if cfg.Cartographer.Gear != 7 {
		t.Errorf("Cartographer.Gear = %d, want 7", cfg.Cartographer.Gear)
	}
	if cfg.Cartographer.Scale != 2.5 {
		t.Errorf("Cartographer.Scale = %f, want 2.5", cfg.Cartographer.Scale)
	}
	if cfg.Cartographer.Endpoint != "http://env:11434" {
		t.Errorf("Cartographer.Endpoint = %q, want %q", cfg.Cartographer.Endpoint, "http://env:11434")
	}
	if cfg.Cartographer.Model != "env-vlm" {
		t.Errorf("Cartographer.Model = %q, want %q", cfg.Cartographer.Model, "env-vlm")
	}
	if cfg.Navigator.Endpoint != "http://env:11435" {
		t.Errorf("Navigator.Endpoint = %q, want %q", cfg.Navigator.Endpoint, "http://env:11435")
	}
	if cfg.Navigator.Model != "env-nav" {
		t.Errorf("Navigator.Model = %q, want %q", cfg.Navigator.Model, "env-nav")
	}
	if cfg.Navigator.Format != "gemma" {
		t.Errorf("Navigator.Format = %q, want %q", cfg.Navigator.Format, "gemma")
	}
	if !cfg.Navigator.CLI {
		t.Error("Navigator.CLI should be true from env")
	}
	if cfg.Database.Path != "/tmp/env.db" {
		t.Errorf("Database.Path = %q, want %q", cfg.Database.Path, "/tmp/env.db")
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	tests := []struct {
		input string
		want  string
	}{
		{"~/foo/bar", filepath.Join(home, "foo", "bar")},
		{"~/.xray/schemas.db", filepath.Join(home, ".xray", "schemas.db")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"~", "~"},         // too short, no slash
		{"", ""},           // empty
		{"~nope", "~nope"}, // not ~/ prefix
	}

	for _, tt := range tests {
		got := expandHome(tt.input)
		if got != tt.want {
			t.Errorf("expandHome(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
