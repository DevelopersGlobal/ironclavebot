package devrel

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/DevelopersGlobal/ironclavebot/internal/tools"
)

// IssueSolverTool exposes the autonomous Git-Native DevRel Issue Solver as an agent tool
type IssueSolverTool struct {
	solver *IssueSolver
}

// NewIssueSolverTool creates a tool wrapper around IssueSolver
func NewIssueSolverTool(solver *IssueSolver) *IssueSolverTool {
	return &IssueSolverTool{solver: solver}
}

func (t *IssueSolverTool) Name() string {
	return "solve_git_issue"
}

func (t *IssueSolverTool) Description() string {
	return "Autonomously inspects the Git repository, triages an issue description, runs reproducing tests in the sandbox, applies a patch if provided, verifies the fix, and outputs a clean unified Git diff."
}

func (t *IssueSolverTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Short title of the bug or issue to solve",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Detailed description, stack trace, or failure symptom",
			},
			"repro_command": map[string]interface{}{
				"type":        "string",
				"description": "Shell command to reproduce the bug in the sandbox (e.g. 'go test -v ./...')",
			},
			"verify_command": map[string]interface{}{
				"type":        "string",
				"description": "Shell command to verify the fix after patching",
			},
			"target_file": map[string]interface{}{
				"type":        "string",
				"description": "Relative path to the file to patch inside the sandbox",
			},
			"proposed_patch": map[string]interface{}{
				"type":        "string",
				"description": "Full updated file content to apply as the patch",
			},
		},
		"required": []string{"title"},
	}
}

func (t *IssueSolverTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var req IssueRequest
	if err := tools.ParseArgs(argsJSON, &req); err != nil {
		return "", err
	}
	if req.Title == "" {
		return "", fmt.Errorf("issue title is required")
	}

	report, err := t.solver.Solve(ctx, req)
	if err != nil {
		return "", err
	}

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}
