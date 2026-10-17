package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Policy defines permission levels for agent operations
type Policy string

const (
	PolicyStrict      Policy = "strict"      // Prompt for any modification
	PolicyInteractive Policy = "interactive" // Auto-approve read, ask for write/exec
	PolicyPermissive  Policy = "permissive"  // Autonomous (ideal inside Docker)
)

// Sandbox defines execution constraints for tools
type Sandbox struct {
	workdir string
	policy  Policy
}

// New creates a new isolated workspace sandbox
func New(workdir string, policy Policy) *Sandbox {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		abs = workdir
	}
	return &Sandbox{
		workdir: abs,
		policy:  policy,
	}
}

// Workdir returns the root isolated directory
func (s *Sandbox) Workdir() string {
	return s.workdir
}

// ValidatePath ensures that target file paths cannot escape the sandbox boundary
func (s *Sandbox) ValidatePath(target string) (string, error) {
	var clean string
	if filepath.IsAbs(target) {
		clean = filepath.Clean(target)
	} else {
		clean = filepath.Clean(filepath.Join(s.workdir, target))
	}

	rel, err := filepath.Rel(s.workdir, clean)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("security violation: path %s attempts to escape sandbox boundary %s", target, s.workdir)
	}

	return clean, nil
}

// ExecuteCommand runs a shell command inside the sandbox workdir with timeout and safety blocks
func (s *Sandbox) ExecuteCommand(ctx context.Context, cmdStr string) (string, error) {
	// 1. Blacklist dangerous commands that threaten host stability
	lower := strings.ToLower(cmdStr)
	dangerous := []string{
		"rm -rf /", "rm -rf ~", "mkfs", ":(){ :|:& };:", "> /dev/sda",
		"dd if=", "shutdown", "reboot", "init 0",
	}
	for _, bad := range dangerous {
		if strings.Contains(lower, bad) {
			return "", fmt.Errorf("ironclave security blocked potentially destructive command: %s", bad)
		}
	}

	// 2. Set execution timeout (default 30s)
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "sh", "-c", cmdStr)
	cmd.Dir = s.workdir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if execCtx.Err() == context.DeadlineExceeded {
		return "", errors.New("command execution timed out (limit: 30s)")
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		if len(output) > 0 {
			output += "\n[stderr]: " + stderr.String()
		} else {
			output = stderr.String()
		}
	}

	if err != nil && len(output) == 0 {
		return "", fmt.Errorf("command error: %w", err)
	}

	return output, nil
}
