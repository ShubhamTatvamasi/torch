package config

import (
	"os"
	"path/filepath"
	"testing"
)

func isolate(t *testing.T) {
	t.Helper()
	original := Paths
	t.Cleanup(func() { Paths = original })
	Paths = func() (string, string, string) { return "", "", "" }
	t.Setenv("OPENCODE_URL", "")
	t.Setenv("OPENCODE_PASSWORD", "")
	t.Setenv("OPENCODE_USERNAME", "")
	t.Setenv("TORCH_MODEL", "")
	t.Setenv("TORCH_AGENT", "")
	t.Setenv("SUPER_MODEL", "")
}

func TestLoadFromEnvironment(t *testing.T) {
	isolate(t)
	t.Setenv("OPENCODE_URL", "http://127.0.0.1:9999/")
	t.Setenv("OPENCODE_PASSWORD", "secret")
	t.Setenv("TORCH_MODEL", "gpustack/deepseek-v4.1-flash-1017")

	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.URL != "http://127.0.0.1:9999" {
		t.Errorf("url = %q", s.URL)
	}
	if s.Username != "opencode" {
		t.Errorf("username = %q", s.Username)
	}
	if s.Model != "gpustack/deepseek-v4.1-flash-1017" {
		t.Errorf("model = %q", s.Model)
	}
	if s.MaxSteps != DefaultMaxSteps {
		t.Errorf("max steps = %d", s.MaxSteps)
	}
}

func TestLoadRequiresURLAndPassword(t *testing.T) {
	isolate(t)
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("expected an error without a URL")
	}

	t.Setenv("OPENCODE_URL", "http://127.0.0.1:9999")
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("expected an error without a password")
	}
}

func TestProjectConfig(t *testing.T) {
	isolate(t)
	t.Setenv("OPENCODE_URL", "http://127.0.0.1:9999")
	t.Setenv("OPENCODE_PASSWORD", "secret")

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".torch"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"judge_model":"gpustack/x","agent":"plan","max_steps":4,"max_cost_usd":2.5,"max_minutes":30}`
	if err := os.WriteFile(filepath.Join(dir, ".torch", "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Model != "gpustack/x" || s.Agent != "plan" {
		t.Errorf("project config not applied: %+v", s)
	}
	if s.MaxSteps != 4 || s.MaxCost != 2.5 || s.MaxMinutes != 30 {
		t.Errorf("budgets = %d %.2f %.0f", s.MaxSteps, s.MaxCost, s.MaxMinutes)
	}
}

func TestEnvironmentBeatsProjectConfig(t *testing.T) {
	isolate(t)
	t.Setenv("OPENCODE_URL", "http://127.0.0.1:9999")
	t.Setenv("OPENCODE_PASSWORD", "secret")
	t.Setenv("TORCH_MODEL", "env/model")

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".torch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".torch", "config.json"),
		[]byte(`{"judge_model":"file/model"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Model != "env/model" {
		t.Errorf("model = %q, want env/model", s.Model)
	}
}

func TestGlobalModelFromOpenCodeConfig(t *testing.T) {
	isolate(t)
	t.Setenv("OPENCODE_URL", "http://127.0.0.1:9999")
	t.Setenv("OPENCODE_PASSWORD", "secret")

	dir := t.TempDir()
	cfg := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(cfg, []byte(`{"model":{"providerID":"gpustack","id":"deepseek"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	Paths = func() (string, string, string) { return "", "", cfg }

	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.Model != "gpustack/deepseek" {
		t.Errorf("model = %q", s.Model)
	}
}
