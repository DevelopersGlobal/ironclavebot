package tools

import (
	"context"
	"fmt"

	"github.com/DevelopersGlobal/ironclavebot/internal/sandbox"
)

// TerminalTool allows the agent to run commands safely inside the sandbox
type TerminalTool struct {
	box *sandbox.Sandbox
}

// NewTerminalTool initializes the terminal capability
func NewTerminalTool(box *sandbox.Sandbox) *TerminalTool {
	return &TerminalTool{box: box}
}

func (t *TerminalTool) Name() string {
	return "run_command"
}

func (t *TerminalTool) Description() string {
	return "Executes a shell command inside the isolated sandbox workspace. Destructive commands are strictly blocked."
}

func (t *TerminalTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The shell command to execute, e.g. 'ls -la', 'npm test', 'git status'",
			},
		},
		"required": []string{"command"},
	}
}

type TerminalArgs struct {
	Command string `json:"command"`
}

func (t *TerminalTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args TerminalArgs
	if err := ParseArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if args.Command == "" {
		return "", fmt.Errorf("command parameter cannot be empty")
	}

	return t.box.ExecuteCommand(ctx, args.Command)
}
