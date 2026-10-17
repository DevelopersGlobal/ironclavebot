package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/DevelopersGlobal/ironclavebot/internal/agent"
	"github.com/DevelopersGlobal/ironclavebot/internal/config"
	"github.com/DevelopersGlobal/ironclavebot/internal/database"
	"github.com/DevelopersGlobal/ironclavebot/internal/devrel"
	"github.com/DevelopersGlobal/ironclavebot/internal/mcp"
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
	fmt.Print(Banner)

	// Support optional "./ironclavebot serve" subcommand before flags
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	}

	port := flag.Int("port", 8080, "Web dashboard port")
	workdir := flag.String("workdir", "./workspace", "Sandbox isolated working directory")
	provName := flag.String("provider", "", "LLM provider (mock, openai, anthropic, deepseek, ollama, vllm)")
	modelName := flag.String("model", "", "Model name (e.g. gpt-4o, claude-3-5-sonnet-20241022, deepseek-chat, llama3.2)")
	cliPrompt := flag.String("chat", "", "Run single command turn directly from CLI without web UI")
	secMode := flag.String("security", "", "Security policy tier (strict, interactive, permissive)")
	mcpConfigPath := flag.String("mcp-config", "", "Path to MCP server configuration JSON file")
	solveIssueTitle := flag.String("solve-issue", "", "Run autonomous Git issue solver on repository with issue title")
	reproCmd := flag.String("repro", "", "Reproduction command for issue solver")
	verifyCmd := flag.String("verify", "", "Verification command for issue solver")
	targetFile := flag.String("target", "", "Target source file for issue solver")
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
	if *secMode != "" {
		cfg.SecurityMode = *secMode
	}

	// 1. Initialize persistent database
	store, err := database.NewStore(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// 2. Initialize isolated sandbox and granular approval engine
	policy := sandbox.Policy(cfg.SecurityMode)
	box := sandbox.New(cfg.SandboxWorkdir, policy)
	approvalEngine := sandbox.NewApprovalEngine(policy, 60*time.Second)

	// 3. Register standard built-in tools
	registry := tools.NewRegistry()
	registry.Register(tools.NewTerminalTool(box))
	registry.Register(tools.NewReadFileTool(box))
	registry.Register(tools.NewWriteFileTool(box))

	// 4. Select LLM provider
	var llmProvider provider.Provider
	switch strings.ToLower(cfg.LLMProvider) {
	case "openai":
		if cfg.OpenAIKey == "" {
			log.Println("⚠️ OPENAI_API_KEY is not set. Falling back to MockProvider.")
			llmProvider = provider.NewMockProvider()
		} else {
			llmProvider = provider.NewOpenAIProvider("https://api.openai.com/v1", cfg.OpenAIKey)
		}
	case "deepseek":
		if cfg.DeepSeekKey == "" {
			log.Println("⚠️ DEEPSEEK_API_KEY is not set. Falling back to MockProvider.")
			llmProvider = provider.NewMockProvider()
		} else {
			llmProvider = provider.NewOpenAIProvider("https://api.deepseek.com/v1", cfg.DeepSeekKey)
		}
	case "anthropic":
		if cfg.AnthropicKey == "" {
			log.Println("⚠️ ANTHROPIC_API_KEY is not set. Falling back to MockProvider.")
			llmProvider = provider.NewMockProvider()
		} else {
			llmProvider = provider.NewAnthropicProvider(cfg.AnthropicKey)
		}
	case "ollama", "vllm":
		llmProvider = provider.NewOpenAIProvider(cfg.OllamaBaseURL+"/v1", "")
	default:
		llmProvider = provider.NewMockProvider()
	}

	// 5. Initialize Git-Native Autonomous DevRel / Issue Solver & register as Agent tool
	issueSolver := devrel.NewIssueSolver(box, llmProvider, cfg.LLMModel)
	registry.Register(devrel.NewIssueSolverTool(issueSolver))

	// 6. Initialize Model Context Protocol (MCP) Manager
	mcpManager := mcp.NewManager()
	mcpManager.SetRegistry(registry)
	if *mcpConfigPath != "" {
		if data, err := os.ReadFile(*mcpConfigPath); err == nil {
			var configs []mcp.ServerConfig
			if err := json.Unmarshal(data, &configs); err == nil {
				for _, sc := range configs {
					mcpManager.RegisterServer(sc)
				}
				_ = mcpManager.ConnectAllAndRegisterTools(context.Background(), registry)
			}
		}
	}

	// 7. Initialize Agent
	botAgent := agent.NewAgent(cfg.LLMModel, llmProvider, registry, store)
	botAgent.SetApprovalEngine(approvalEngine)

	// 8. Direct CLI Issue Solver mode
	if *solveIssueTitle != "" {
		fmt.Printf("🔍 Starting Autonomous DevRel Issue Solver: %q\n", *solveIssueTitle)
		report, err := issueSolver.Solve(context.Background(), devrel.IssueRequest{
			Title:         *solveIssueTitle,
			Description:   fmt.Sprintf("Fix requested for %s", *solveIssueTitle),
			ReproCommand:  *reproCmd,
			VerifyCommand: *verifyCmd,
			TargetFile:    *targetFile,
		})
		if err != nil {
			log.Fatalf("❌ Issue solver error: %v", err)
		}

		fmt.Printf("\n📋 Issue Resolution Report:\n")
		fmt.Printf("Success: %t\n", report.Success)
		fmt.Printf("Summary: %s\n", report.Triage.Summary)
		if report.GitDiff != "" {
			fmt.Printf("\n📝 Git Patch Diff:\n%s\n", report.GitDiff)
		}
		fmt.Printf("\n💡 Suggested Commit:\n%s\n", report.CommitMessage)
		os.Exit(0)
	}

	// 9. Direct CLI chat turn mode
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

	// 10. Web Dashboard & Server mode
	srv := server.New(cfg, botAgent, store, box, approvalEngine, issueSolver, mcpManager)
	if err := srv.Start(); err != nil {
		log.Fatalf("Server stopped: %v", err)
	}
}
