package config

import (
	"os"
	"path/filepath"
	"strconv"
)

// Config represents runtime configuration for IronClaveBot
type Config struct {
	Port            int
	Env             string
	DatabasePath    string
	LLMProvider     string
	LLMModel        string
	OpenAIKey       string
	AnthropicKey    string
	DeepSeekKey     string
	OllamaBaseURL   string
	SecurityMode    string
	SandboxProvider string
	SandboxWorkdir  string
}

// Load reads settings from environment variables with sensible defaults
func Load() *Config {
	port, _ := strconv.Atoi(getEnv("PORT", "8080"))

	dbPath := getEnv("DATABASE_PATH", "./data/ironclavebot.db")
	_ = os.MkdirAll(filepath.Dir(dbPath), 0755)

	workdir := getEnv("SANDBOX_WORKDIR", "./workspace")
	_ = os.MkdirAll(workdir, 0755)

	return &Config{
		Port:            port,
		Env:             getEnv("ENV", "development"),
		DatabasePath:    dbPath,
		LLMProvider:     getEnv("LLM_PROVIDER", "mock"),
		LLMModel:        getEnv("LLM_MODEL", "mock-agent"),
		OpenAIKey:       getEnv("OPENAI_API_KEY", ""),
		AnthropicKey:    getEnv("ANTHROPIC_API_KEY", ""),
		DeepSeekKey:     getEnv("DEEPSEEK_API_KEY", ""),
		OllamaBaseURL:   getEnv("OLLAMA_BASE_URL", "http://localhost:11434"),
		SecurityMode:    getEnv("SECURITY_MODE", "interactive"),
		SandboxProvider: getEnv("SANDBOX_PROVIDER", "local"),
		SandboxWorkdir:  workdir,
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
