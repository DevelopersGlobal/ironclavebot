package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Policy defines permission levels for agent operations
type Policy string

const (
	PolicyStrict      Policy = "strict"      // Reads only; every modification needs approval
	PolicyInteractive Policy = "interactive" // Reads allowed; writes and command execution need approval
	PolicyPermissive  Policy = "permissive"  // Autonomous (ideal inside Docker)
)

// Operation classifies what a tool wants to do so the policy can decide.
type Operation string

const (
	OpRead    Operation = "read"
	OpWrite   Operation = "write"
	OpExecute Operation = "execute"
)

// Approver decides whether an operation that needs approval may proceed.
// There is no built-in approval UI yet, so by default nothing is approved.
type Approver func(op Operation, detail string) bool

const (
	// MaxOutputBytes caps the combined stdout+stderr captured from one command.
	MaxOutputBytes = 1 << 20
	// CommandTimeoutDefault is the wall-clock limit for one command.
	CommandTimeoutDefault = 30 * time.Second
	// pipeWaitDelay bounds how long we wait for output pipes after the
	// command is killed (descendants may still hold them open).
	pipeWaitDelay = 2 * time.Second
)

// CommandTimeout is the active per-command limit (a variable so tests can shorten it).
var CommandTimeout = CommandTimeoutDefault

// Sandbox defines execution constraints for tools
type Sandbox struct {
	workdir  string
	policy   Policy
	mu       sync.RWMutex
	approver Approver
}

// New creates a workspace sandbox rooted at workdir. The directory is created
// if it does not exist.
func New(workdir string, policy Policy) *Sandbox {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		abs = workdir
	}
	_ = os.MkdirAll(abs, 0o755)
	return &Sandbox{
		workdir: abs,
		policy:  policy,
	}
}

// Workdir returns the root isolated directory
func (s *Sandbox) Workdir() string {
	return s.workdir
}

// Policy returns the active policy.
func (s *Sandbox) Policy() Policy {
	return s.policy
}

// SetApprover installs the callback used for operations the policy gates.
func (s *Sandbox) SetApprover(a Approver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approver = a
}

// Authorize enforces the sandbox policy. Unknown policy values fail closed
// (treated as strict).
func (s *Sandbox) Authorize(op Operation, detail string) error {
	if op == OpRead || s.policy == PolicyPermissive {
		return nil
	}
	s.mu.RLock()
	approve := s.approver
	s.mu.RUnlock()
	if approve != nil && approve(op, detail) {
		return nil
	}
	return fmt.Errorf("operation %q is not allowed under %q security mode and no approval mechanism is configured; set SECURITY_MODE=permissive only inside a container or VM", op, s.policy)
}

// relPath converts target to a clean path relative to the workdir, rejecting
// anything that lexically escapes it.
func (s *Sandbox) relPath(target string) (string, error) {
	var clean string
	if filepath.IsAbs(target) {
		clean = filepath.Clean(target)
	} else {
		clean = filepath.Clean(filepath.Join(s.workdir, target))
	}
	rel, err := filepath.Rel(s.workdir, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("security violation: path %s attempts to escape sandbox boundary %s", target, s.workdir)
	}
	return rel, nil
}

// ValidatePath checks that target is lexically inside the sandbox and returns
// the cleaned absolute path. It does NOT resolve symlinks, so never open the
// returned path directly; use ReadFile / WriteFile, which are confined at open
// time.
func (s *Sandbox) ValidatePath(target string) (string, error) {
	rel, err := s.relPath(target)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.workdir, rel), nil
}

// ReadFile reads a file inside the sandbox. Symlinks that point outside the
// workdir are refused by the OS-level root confinement.
func (s *Sandbox) ReadFile(target string) ([]byte, error) {
	if err := s.Authorize(OpRead, target); err != nil {
		return nil, err
	}
	rel, err := s.relPath(target)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.workdir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// WriteFile writes a file inside the sandbox, creating parent directories.
// Every path component is resolved inside the workdir, including symlinks.
func (s *Sandbox) WriteFile(target string, data []byte) error {
	if err := s.Authorize(OpWrite, target); err != nil {
		return err
	}
	return s.writeFile(target, data)
}

func (s *Sandbox) writeFile(target string, data []byte) error {
	rel, err := s.relPath(target)
	if err != nil {
		return err
	}
	if rel == "." {
		return fmt.Errorf("cannot write to the sandbox root")
	}
	root, err := os.OpenRoot(s.workdir)
	if err != nil {
		return err
	}
	defer root.Close()

	// Create parent directories one component at a time through the root.
	dir := filepath.Dir(rel)
	if dir != "." {
		cur := ""
		for _, part := range strings.Split(dir, string(filepath.Separator)) {
			cur = filepath.Join(cur, part)
			if err := root.Mkdir(cur, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		}
	}

	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Exists reports whether a path inside the sandbox exists (without following
// links out of the workdir).
func (s *Sandbox) Exists(target string) bool {
	rel, err := s.relPath(target)
	if err != nil {
		return false
	}
	root, err := os.OpenRoot(s.workdir)
	if err != nil {
		return false
	}
	defer root.Close()
	_, err = root.Lstat(rel)
	return err == nil
}

// ShellQuote quotes a string for safe use as one POSIX shell word.
func ShellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// limitedBuffer keeps at most a shared byte budget and silently drops the
// rest while still reporting a full write, so the child never blocks on a
// full pipe.
type limitedBuffer struct {
	buf    bytes.Buffer
	budget *outputBudget
}

type outputBudget struct {
	mu        sync.Mutex
	remaining int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.budget.mu.Lock()
	defer b.budget.mu.Unlock()
	n := len(p)
	if n > b.budget.remaining {
		p = p[:b.budget.remaining]
		b.budget.truncated = true
	}
	b.budget.remaining -= len(p)
	b.buf.Write(p)
	return n, nil
}

// ExecuteCommand runs a shell command inside the sandbox workdir.
//
// It returns the captured output even when the command fails. A nonzero exit
// status is always reported as an error, alongside whatever output the command
// produced.
//
// NOTE: this is NOT a security boundary. The command runs as the daemon's user
// with full filesystem and network access; only the working directory, a
// scrubbed environment, a timeout, an output cap and a coarse blacklist are
// applied. Run the daemon inside a container or VM for real isolation.
func (s *Sandbox) ExecuteCommand(ctx context.Context, cmdStr string) (string, error) {
	if err := s.Authorize(OpExecute, cmdStr); err != nil {
		return "", err
	}

	// Coarse blacklist of obviously destructive commands. Trivially bypassed;
	// kept only as a guard against accidents.
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

	if st, err := os.Stat(s.workdir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("sandbox workdir %s is not available", s.workdir)
	}

	execCtx, cancel := context.WithTimeout(ctx, CommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "sh", "-c", cmdStr)
	cmd.Dir = s.workdir
	cmd.Env = scrubbedEnv(s.workdir)
	prepareProcessGroup(cmd)
	cmd.WaitDelay = pipeWaitDelay

	budget := &outputBudget{remaining: MaxOutputBytes}
	stdout := &limitedBuffer{budget: budget}
	stderr := &limitedBuffer{budget: budget}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()

	output := stdout.buf.String()
	if stderr.buf.Len() > 0 {
		if len(output) > 0 {
			output += "\n[stderr]: " + stderr.buf.String()
		} else {
			output = stderr.buf.String()
		}
	}
	if budget.truncated {
		output += fmt.Sprintf("\n[output truncated at %d bytes]", MaxOutputBytes)
	}

	if execCtx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("command execution timed out (limit: %s)", CommandTimeout)
	}
	if runErr != nil {
		return output, fmt.Errorf("command failed: %w", runErr)
	}
	return output, nil
}

// scrubbedEnv returns a minimal environment so API keys and other secrets in
// the daemon's environment are not handed to agent-run commands.
func scrubbedEnv(workdir string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + workdir,
		"LANG=C.UTF-8",
		"TERM=dumb",
	}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		env = append(env, "TMPDIR="+tmp)
	}
	return env
}
