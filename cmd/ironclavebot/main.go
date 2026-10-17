package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/DevelopersGlobal/ironclavebot/internal/agent"
	"github.com/DevelopersGlobal/ironclavebot/internal/config"
	"github.com/DevelopersGlobal/ironclavebot/internal/database"
	"github.com/DevelopersGlobal/ironclavebot/internal/provider"
	"github.com/DevelopersGlobal/ironclavebot/internal/sandbox"
	"github.com/DevelopersGlobal/ironclavebot/internal/server"
	"github.com/DevelopersGlobal/ironclavebot/internal/tools"
)

const Banner = `
  ___                     ____ _                  ____        _   
 |_ _|_ __ ___  _ __     / ___| | __ ___   _____ | __ )  ___ | |_ 
  | || '__/ _ \| '_ \   | |   | |/ _` + "`" + ` \ \ / / _ \|  _ \ / _ \| __|
  | || | | (_) | | | |  | |___| | (_| |\ V /  __/| |_) | (_) | |_ 
 |___|_|  \___/|_| |_|   \____|_|\__,_| \_/ \___||____/ \___/ \__|
          The Sandboxed AI Teammate • DevelopersGlobal
`

func main() {
	fmt.Println(Banner)

	port := flag.Int("port", 8080, "Web dashboard port")
	workdir := flag.String("workdir", "./workspace", "Sandbox isolated working directory")
	provName := flag.String("provider", "", "LLM provider (mock, openai, ollama)")
	modelName := flag.String("model", "", "Model name (e.g. gpt-4o, llama3.2)")
	cliPrompt := flag.String("chat", "", "Run single command turn directly from CLI without web UI")
	flag.Parse()

	cfg := config.Load()
	if *port != 8080 {
		cfg.Port = *port
	}
	if *workdir != "./workspace" {
		cfg.SandboxWorkdir = *workdir
	}
	if *provName != "" {
		cfg.LLMProvider = *provName
	}
	if *modelName != "" {
		cfg.LLMModel = *modelName
	}

	// 1. Initialize persistent database
	store, err := database.NewStore(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// 2. Initialize isolated sandbox
	box := sandbox.New(cfg.SandboxWorkdir, sandbox.Policy(cfg.SecurityMode))

	// 3. Register tools
	registry := tools.NewRegistry()
	registry.Register(tools.NewTerminalTool(box))
	registry.Register(tools.NewReadFileTool(box))
	registry.Register(tools.NewWriteFileTool(box))

	// 4. Select LLM provider
	var llmProvider provider.Provider
	switch cfg.LLMProvider {
	case "openai":
		if cfg.OpenAIKey == "" {
			log.Println("⚠️ OPENAI_API_KEY is not set. Falling back to MockProvider.")
			llmProvider = provider.NewMockProvider()
		} else {
			llmProvider = provider.NewOpenAIProvider("https://api.openai.com/v1", cfg.OpenAIKey)
		}
	case "ollama":
		llmProvider = provider.NewOpenAIProvider(cfg.OllamaBaseURL+"/v1", "")
	default:
		llmProvider = provider.NewMockProvider()
	}

	// 5. Initialize agent
	botAgent := agent.NewAgent(cfg.LLMModel, llmProvider, registry, store)

	// 6. Direct CLI run mode
	if *cliPrompt != "" {
		sess, err := store.CreateSession("CLI Turn")
		if err != nil {
			log.Fatalf("Error creating session: %v", err)
		}
		fmt.Printf("User: %s\n\n", *cliPrompt)
		reply, err := botAgent.Run(context.Background(), sess.ID, *cliPrompt)
		if err != nil {
			log.Fatalf("Agent error: %v", err)
		}
		fmt.Printf("IronClaveBot: %s\n", reply)
		os.Exit(0)
	}

	// 7. Web Dashboard Server mode
	srv := server.New(cfg, botAgent, store, box)
	if err := srv.Start(); err != nil {
		log.Fatalf("Server stopped: %v", err)
	}
}
