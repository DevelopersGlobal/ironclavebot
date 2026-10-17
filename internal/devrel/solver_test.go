package devrel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DevelopersGlobal/ironclavebot/internal/sandbox"
)

func setupTestGitRepo(t *testing.T) (*sandbox.Sandbox, string) {
	tempDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tempDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	// Configure local git user for test commits
	_ = exec.Command("git", "-C", tempDir, "config", "user.email", "test@developersglobal.org").Run()
	_ = exec.Command("git", "-C", tempDir, "config", "user.name", "IronClave Bot").Run()

	// Write an initial buggy file
	calcFile := filepath.Join(tempDir, "calc.sh")
	initialContent := "#!/bin/sh\n# Buggy calculator\necho 41\n"
	if err := os.WriteFile(calcFile, []byte(initialContent), 0755); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_ = exec.Command("git", "-C", tempDir, "add", ".").Run()
	_ = exec.Command("git", "-C", tempDir, "commit", "-m", "initial commit").Run()

	box := sandbox.New(tempDir, sandbox.PolicyPermissive)
	return box, tempDir
}

func TestIssueSolverLifecycle(t *testing.T) {
	box, _ := setupTestGitRepo(t)
	solver := NewIssueSolver(box, nil, "mock-agent")
	ctx := context.Background()

	// 1. Inspect repository
	inspection, err := solver.InspectRepository(ctx)
	if err != nil {
		t.Fatalf("InspectRepository failed: %v", err)
	}
	if len(inspection.Files) == 0 {
		t.Errorf("Expected files in repository inspection, got 0")
	}

	// 2. Triage issue
	issue := IssueRequest{
		Title:         "calc.sh returns wrong answer 41 instead of 42",
		Description:   "When executing calc.sh, expected output is 42 but received 41.",
		ReproCommand:  `test "$($(pwd)/calc.sh)" = "42"`,
		VerifyCommand: `test "$($(pwd)/calc.sh)" = "42"`,
		TargetFile:    "calc.sh",
		ProposedPatch: "#!/bin/sh\n# Fixed calculator\necho 42\n",
	}

	triage, err := solver.TriageIssue(ctx, issue)
	if err != nil {
		t.Fatalf("TriageIssue failed: %v", err)
	}
	if triage.IssueTitle != issue.Title {
		t.Errorf("Triage title mismatch: got %s", triage.IssueTitle)
	}

	// 3. Reproduction run (should fail on buggy file)
	reproResult, err := solver.RunReproduction(ctx, issue.ReproCommand)
	if err != nil {
		t.Fatalf("RunReproduction error: %v", err)
	}
	if reproResult.Success {
		t.Errorf("Expected reproduction command to fail on buggy code, but succeeded")
	}

	// 4. End-to-end Solve
	report, err := solver.Solve(ctx, issue)
	if err != nil {
		t.Fatalf("Solve failed: %v", err)
	}

	if !report.Success {
		t.Errorf("Expected resolution report success to be true")
	}

	if !strings.Contains(report.GitDiff, "+echo 42") {
		t.Errorf("Expected git diff to contain '+echo 42', got:\n%s", report.GitDiff)
	}

	if !strings.Contains(report.CommitMessage, "fix: resolve") {
		t.Errorf("Expected commit message suggestion, got: %s", report.CommitMessage)
	}
}
