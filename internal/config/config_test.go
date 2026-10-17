package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvDoesNotOverrideEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\nLLM_PROVIDER=openai\nexport LLM_MODEL=\"gpt-4o\"\nPORT=9999 # inline\nQUOTED='a b'\n\nBROKEN\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORT", "7000") // real environment wins
	os.Unsetenv("LLM_PROVIDER")
	os.Unsetenv("LLM_MODEL")
	os.Unsetenv("QUOTED")
	t.Cleanup(func() { os.Unsetenv("LLM_PROVIDER"); os.Unsetenv("LLM_MODEL"); os.Unsetenv("QUOTED") })

	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	cfg := Load()
	if cfg.LLMProvider != "openai" || cfg.LLMModel != "gpt-4o" {
		t.Errorf("dotenv values not loaded: %+v", cfg)
	}
	if cfg.Port != 7000 {
		t.Errorf("dotenv overrode the real environment: port=%d", cfg.Port)
	}
	if os.Getenv("QUOTED") != "a b" {
		t.Errorf("quotes not stripped: %q", os.Getenv("QUOTED"))
	}
}

func TestLoadDotEnvMissingFileIsFine(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultsBindLoopback(t *testing.T) {
	os.Unsetenv("HOST")
	if cfg := Load(); cfg.Host != "127.0.0.1" {
		t.Errorf("default host = %q", cfg.Host)
	}
}
