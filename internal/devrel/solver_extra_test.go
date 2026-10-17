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

func TestInspectWorksInHiddenRootDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".workspace")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "src", "a.txt"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(root, ".git", "config"), []byte("x"), 0o644)

	s := NewIssueSolver(sandbox.New(root, sandbox.PolicyPermissive), nil, "m")
	insp, err := s.InspectRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(insp.Files) != 1 || insp.Files[0] != filepath.Join("src", "a.txt") {
		t.Fatalf("files = %v", insp.Files)
	}
}

func TestInspectStopsAtFileLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 150; i++ {
		_ = os.WriteFile(filepath.Join(root, "f"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+".txt"), nil, 0o644)
	}
	s := NewIssueSolver(sandbox.New(root, sandbox.PolicyPermissive), nil, "m")
	insp, _ := s.InspectRepository(context.Background())
	if len(insp.Files) > 100 {
		t.Fatalf("collected %d files", len(insp.Files))
	}
}

func TestSkippedVerificationIsNotSuccess(t *testing.T) {
	box, _ := setupTestGitRepo(t)
	s := NewIssueSolver(box, nil, "m")
	report, err := s.Solve(context.Background(), IssueRequest{Title: "no tests", Description: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Success || report.Verified {
		t.Errorf("unverified resolution reported as success: %+v", report)
	}
	if report.VerificationTest == nil || !report.VerificationTest.Skipped || report.VerificationTest.Success {
		t.Errorf("skipped verification not marked: %+v", report.VerificationTest)
	}
}

func TestFailedVerificationIsNotSuccess(t *testing.T) {
	box, _ := setupTestGitRepo(t)
	s := NewIssueSolver(box, nil, "m")
	report, err := s.Solve(context.Background(), IssueRequest{Title: "t", VerifyCommand: "echo failing; exit 1"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Success {
		t.Error("failed verification reported as success")
	}
}

func TestDiffIncludesNewFilesAndKeepsTrailingNewline(t *testing.T) {
	box, dir := setupTestGitRepo(t)
	// An unrelated untracked file must not end up in the diff.
	_ = os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("mine\n"), 0o644)

	s := NewIssueSolver(box, nil, "m")
	report, err := s.Solve(context.Background(), IssueRequest{
		Title:         "add file",
		TargetFile:    "new dir/it's new.txt",
		ProposedPatch: "line with trailing space \n",
		VerifyCommand: "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report.GitDiff, "+line with trailing space \n") {
		t.Errorf("new file missing from diff or whitespace altered:\n%q", report.GitDiff)
	}
	if !strings.HasSuffix(report.GitDiff, "\n") {
		t.Error("diff lost its terminating newline")
	}
	if strings.Contains(report.GitDiff, "unrelated.txt") {
		t.Error("unrelated untracked file leaked into the diff")
	}
	// The diff must apply cleanly to a pristine checkout.
	patch := filepath.Join(t.TempDir(), "p.diff")
	_ = os.WriteFile(patch, []byte(report.GitDiff), 0o644)
	clone := filepath.Join(t.TempDir(), "clone")
	if out, err := exec.Command("git", "clone", "-q", dir, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", clone, "apply", "--check", patch).CombinedOutput(); err != nil {
		t.Errorf("diff does not apply: %v %s", err, out)
	}
}

func TestPatchCannotFollowSymlinkOutside(t *testing.T) {
	box, dir := setupTestGitRepo(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Skip("symlinks unavailable")
	}
	s := NewIssueSolver(box, nil, "m")
	if err := s.ApplyPatch(context.Background(), []FilePatch{{Path: "escape/pwn.txt", NewContent: "x"}}); err == nil {
		t.Error("patch followed a symlink out of the sandbox")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwn.txt")); err == nil {
		t.Error("file written outside the sandbox")
	}
}
