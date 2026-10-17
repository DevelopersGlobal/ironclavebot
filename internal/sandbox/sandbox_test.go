package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSymlinkEscapeBlocked(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(work, "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := os.Symlink(secret, filepath.Join(work, "file-link")); err != nil {
		t.Fatal(err)
	}
	box := New(work, PolicyPermissive)

	if _, err := box.ReadFile("link/secret.txt"); err == nil {
		t.Error("read through directory symlink escaped the sandbox")
	}
	if _, err := box.ReadFile("file-link"); err == nil {
		t.Error("read through file symlink escaped the sandbox")
	}
	if err := box.WriteFile("link/new.txt", []byte("x")); err == nil {
		t.Error("write through directory symlink escaped the sandbox")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Error("file was created outside the sandbox")
	}
	if err := box.WriteFile("file-link", []byte("overwritten")); err == nil {
		t.Error("write through file symlink escaped the sandbox")
	}
	if b, _ := os.ReadFile(secret); string(b) != "top secret" {
		t.Errorf("outside file was modified: %q", b)
	}
}

func TestReadWriteInsideAndTraversal(t *testing.T) {
	box := New(t.TempDir(), PolicyPermissive)
	if err := box.WriteFile("a/b/c.txt", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	got, err := box.ReadFile("a/b/c.txt")
	if err != nil || string(got) != "hi" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := box.ReadFile("../etc/passwd"); err == nil {
		t.Error("lexical traversal not rejected")
	}
	// A name that merely starts with ".." is a valid in-sandbox file.
	if err := box.WriteFile("..hidden", []byte("ok")); err != nil {
		t.Errorf("file named ..hidden wrongly rejected: %v", err)
	}
}

func TestPolicyEnforced(t *testing.T) {
	for _, p := range []Policy{PolicyStrict, PolicyInteractive, Policy("bogus")} {
		box := New(t.TempDir(), p)
		if _, err := box.ExecuteCommand(context.Background(), "echo hi"); err == nil {
			t.Errorf("%s: command ran without approval", p)
		}
		if err := box.WriteFile("x.txt", []byte("x")); err == nil {
			t.Errorf("%s: write allowed without approval", p)
		}
		if _, err := box.ReadFile("x.txt"); err != nil && strings.Contains(err.Error(), "not allowed") {
			t.Errorf("%s: reads must not need approval: %v", p, err)
		}
	}
	box := New(t.TempDir(), PolicyInteractive)
	box.SetApprover(func(op Operation, detail string) bool { return op == OpWrite })
	if err := box.WriteFile("ok.txt", []byte("x")); err != nil {
		t.Errorf("approved write failed: %v", err)
	}
	if _, err := box.ExecuteCommand(context.Background(), "echo hi"); err == nil {
		t.Error("unapproved execute allowed")
	}
}

func TestNonzeroExitKeepsOutputAndError(t *testing.T) {
	box := New(t.TempDir(), PolicyPermissive)
	out, err := box.ExecuteCommand(context.Background(), "echo failed; exit 1")
	if err == nil {
		t.Fatal("nonzero exit was reported as success")
	}
	if !strings.Contains(out, "failed") {
		t.Errorf("output lost: %q", out)
	}
}

func TestOutputIsCapped(t *testing.T) {
	box := New(t.TempDir(), PolicyPermissive)
	out, err := box.ExecuteCommand(context.Background(), "yes aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa | head -c 5000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) > MaxOutputBytes+200 {
		t.Errorf("output not capped: %d bytes", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Error("truncation not reported")
	}
}

func TestTimeoutKillsBackgroundChildren(t *testing.T) {
	old := CommandTimeout
	CommandTimeout = time.Second
	defer func() { CommandTimeout = old }()

	box := New(t.TempDir(), PolicyPermissive)
	start := time.Now()
	_, err := box.ExecuteCommand(context.Background(), "sleep 20 & wait")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Errorf("command ran %s, background child kept it alive", d)
	}
}

func TestEnvironmentIsScrubbed(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test-secret")
	box := New(t.TempDir(), PolicyPermissive)
	out, err := box.ExecuteCommand(context.Background(), "env")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sk-test-secret") {
		t.Error("daemon secret leaked to the command environment")
	}
}

func TestMissingWorkdirIsCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "workspace")
	box := New(dir, PolicyPermissive)
	out, err := box.ExecuteCommand(context.Background(), "pwd")
	if err != nil {
		t.Fatalf("command failed in fresh workdir: %v", err)
	}
	if !strings.Contains(out, "workspace") {
		t.Errorf("unexpected cwd %q", out)
	}
}
