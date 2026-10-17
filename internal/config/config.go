package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config represents runtime configuration for IronClaveBot
type Config struct {
	Port            int
	Host            string
	APIToken        string
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
	port, err := strconv.Atoi(getEnv("PORT", "8080"))
	if err != nil || port < 1 || port > 65535 {
		port = 8080
	}

	dbPath := getEnv("DATABASE_PATH", "./data/ironclavebot.db")
	_ = os.MkdirAll(filepath.Dir(dbPath), 0755)

	// The sandbox creates its own workdir after command-line overrides are applied.
	workdir := getEnv("SANDBOX_WORKDIR", "./workspace")

	return &Config{
		Port:            port,
		Host:            getEnv("HOST", "127.0.0.1"),
		APIToken:        getEnv("API_TOKEN", ""),
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

// LoadDotEnv reads KEY=VALUE lines from path into the process environment.
// Variables that are already set are never overridden, so real environment
// variables win over the file. A missing file is not an error.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if key == "" {
			continue
		}
		if _, set := os.LookupEnv(key); !set {
			if err := os.Setenv(key, val); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
