package devrel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DevelopersGlobal/ironclavebot/internal/provider"
	"github.com/DevelopersGlobal/ironclavebot/internal/sandbox"
)

// IssueSolver coordinates autonomous Git repository inspection, triage, reproduction, and patching
type IssueSolver struct {
	box      *sandbox.Sandbox
	provider provider.Provider
	model    string

	mu      sync.Mutex
	created []string // files newly created by ApplyPatch, for GenerateDiff
}

// NewIssueSolver creates an issue solver instance
func NewIssueSolver(box *sandbox.Sandbox, p provider.Provider, model string) *IssueSolver {
	return &IssueSolver{
		box:      box,
		provider: p,
		model:    model,
	}
}

// InspectRepository gathers git metadata and project structure
func (s *IssueSolver) InspectRepository(ctx context.Context) (*RepoInspection, error) {
	inspection := &RepoInspection{
		Workdir:     s.box.Workdir(),
		InspectedAt: time.Now(),
	}

	// 1. Current Branch
	branchOut, err := s.box.ExecuteCommand(ctx, "git rev-parse --abbrev-ref HEAD")
	if err == nil {
		inspection.CurrentBranch = strings.TrimSpace(branchOut)
	} else {
		inspection.CurrentBranch = "main"
	}

	// 2. Last Commit
	logOut, err := s.box.ExecuteCommand(ctx, "git log -1 --oneline")
	if err == nil {
		inspection.LastCommit = strings.TrimSpace(logOut)
	}

	// 3. Git Status
	statusOut, _ := s.box.ExecuteCommand(ctx, "git status --short")
	inspection.Status = strings.TrimSpace(statusOut)

	// 4. Detect Project Type
	workdir := s.box.Workdir()
	if _, err := os.Stat(filepath.Join(workdir, "go.mod")); err == nil {
		inspection.ProjectType = "go"
	} else if _, err := os.Stat(filepath.Join(workdir, "package.json")); err == nil {
		inspection.ProjectType = "nodejs"
	} else if _, err := os.Stat(filepath.Join(workdir, "Cargo.toml")); err == nil {
		inspection.ProjectType = "rust"
	} else if _, err := os.Stat(filepath.Join(workdir, "pyproject.toml")); err == nil ||
		fileExists(filepath.Join(workdir, "requirements.txt")) {
		inspection.ProjectType = "python"
	} else {
		inspection.ProjectType = "generic"
	}

	// 5. List key repository files
	var files []string
	const maxInspectFiles = 100
	_ = filepath.Walk(workdir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if len(files) >= maxInspectFiles {
			return filepath.SkipAll // stop walking once the budget is full
		}
		if info.IsDir() {
			// Skip hidden directories below the root; the root itself may
			// legitimately have a name such as ".workspace".
			if p != workdir && strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(workdir, p)
		if !strings.HasPrefix(rel, ".") {
			files = append(files, rel)
		}
		return nil
	})
	inspection.Files = files

	return inspection, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TriageIssue analyzes the issue request against repo context
func (s *IssueSolver) TriageIssue(ctx context.Context, issue IssueRequest) (*TriageResult, error) {
	inspection, _ := s.InspectRepository(ctx)

	// If an LLM provider is available, use it for deep triage
	if s.provider != nil {
		prompt := fmt.Sprintf(`You are an autonomous Git DevRel Issue Solver for IronClaveBot.
Repository Type: %s
Branch: %s
Key Files: %s

Issue Title: %s
Issue Description:
%s

Analyze this issue and output a JSON object with:
- "summary": concise explanation of the problem
- "suspected_cause": probable root cause
- "candidate_files": list of relative file paths to inspect
- "repro_strategy": test or command to reproduce the issue
- "suggested_fix": conceptual patch plan
Only return valid JSON.`, inspection.ProjectType, inspection.CurrentBranch, strings.Join(inspection.Files, ", "), issue.Title, issue.Description)

		req := &provider.CompletionRequest{
			Model: s.model,
			Messages: []provider.ChatMessage{
				{Role: "system", Content: "You are an automated software triage system. Respond with JSON."},
				{Role: "user", Content: prompt},
			},
			Temperature: 0.1,
		}

		resp, err := s.provider.Chat(ctx, req)
		if err == nil && resp.Message.Content != "" {
			var triage TriageResult
			cleanJSON := strings.TrimSpace(resp.Message.Content)
			cleanJSON = strings.TrimPrefix(cleanJSON, "```json")
			cleanJSON = strings.TrimPrefix(cleanJSON, "```")
			cleanJSON = strings.TrimSuffix(cleanJSON, "```")
			if err := json.Unmarshal([]byte(cleanJSON), &triage); err == nil && triage.Summary != "" {
				triage.IssueTitle = issue.Title
				return &triage, nil
			}
		}
	}

	// Heuristic Fallback Triage
	candidateFiles := make([]string, 0)
	if issue.TargetFile != "" {
		candidateFiles = append(candidateFiles, issue.TargetFile)
	}
	for _, f := range inspection.Files {
		if strings.Contains(strings.ToLower(issue.Description), strings.ToLower(f)) ||
			strings.Contains(strings.ToLower(issue.Title), strings.ToLower(f)) {
			candidateFiles = append(candidateFiles, f)
		}
	}

	return &TriageResult{
		IssueTitle:     issue.Title,
		Summary:        fmt.Sprintf("Issue in %s: %s", inspection.ProjectType, issue.Title),
		SuspectedCause: "Detected discrepancy reported in issue description",
		CandidateFiles: candidateFiles,
		ReproStrategy:  issue.ReproCommand,
		SuggestedFix:   "Inspect candidate files, apply targeted logic correction, and run verification suite.",
	}, nil
}

// RunReproduction runs the reproduction command in the sandbox
func (s *IssueSolver) RunReproduction(ctx context.Context, reproCommand string) (*ExecutionResult, error) {
	if reproCommand == "" {
		return &ExecutionResult{
			Command: "(no reproduction command provided)",
			Success: true,
			Skipped: true,
			Output:  "Skipped explicit reproduction run",
		}, nil
	}

	start := time.Now()
	out, err := s.box.ExecuteCommand(ctx, reproCommand)
	duration := time.Since(start)

	return &ExecutionResult{
		Command:  reproCommand,
		Success:  err == nil,
		Output:   out,
		Duration: duration,
	}, nil
}

// ApplyPatch writes the patched content to the target file inside the sandbox
func (s *IssueSolver) ApplyPatch(ctx context.Context, patches []FilePatch) error {
	for _, p := range patches {
		if _, err := s.box.ValidatePath(p.Path); err != nil {
			return fmt.Errorf("patch safety violation: %w", err)
		}
		existed := s.box.Exists(p.Path)

		// Confined write: symlinks cannot redirect the patch outside the workdir.
		if err := s.box.WriteFile(p.Path, []byte(p.NewContent)); err != nil {
			return fmt.Errorf("failed to write patch to %s: %w", p.Path, err)
		}
		if !existed {
			s.mu.Lock()
			s.created = append(s.created, p.Path)
			s.mu.Unlock()
		}
	}
	return nil
}

// VerifyFix runs verification tests in the sandbox
func (s *IssueSolver) VerifyFix(ctx context.Context, verifyCommand string) (*ExecutionResult, error) {
	if verifyCommand == "" {
		// Nothing was verified, so this must not read as a passing test.
		return &ExecutionResult{
			Command: "(no verify command provided)",
			Success: false,
			Skipped: true,
			Output:  "No automated verification command specified; the fix is unverified",
		}, nil
	}

	start := time.Now()
	out, err := s.box.ExecuteCommand(ctx, verifyCommand)
	duration := time.Since(start)

	return &ExecutionResult{
		Command:  verifyCommand,
		Success:  err == nil,
		Output:   out,
		Duration: duration,
	}, nil
}

// GenerateDiff executes git diff to produce a clean patch diff
func (s *IssueSolver) GenerateDiff(ctx context.Context) (string, error) {
	// Plain "git diff" omits untracked files. Mark only the files this solver
	// created as intent-to-add so they appear in the diff; other untracked
	// user files are left alone.
	s.mu.Lock()
	created := append([]string(nil), s.created...)
	s.mu.Unlock()
	if len(created) > 0 {
		quoted := make([]string, 0, len(created))
		for _, f := range created {
			quoted = append(quoted, sandbox.ShellQuote(f))
		}
		if _, err := s.box.ExecuteCommand(ctx, "git add -N -- "+strings.Join(quoted, " ")); err != nil {
			return "", fmt.Errorf("failed to include new files in diff: %w", err)
		}
	}

	diff, err := s.box.ExecuteCommand(ctx, "git diff")
	if err != nil {
		return "", fmt.Errorf("failed to generate git diff: %w", err)
	}
	// Returned unchanged: trimming would drop trailing whitespace and the
	// final newline, so the patch would no longer apply cleanly.
	return diff, nil
}

// Solve executes the end-to-end autonomous DevRel routine:
// 1. Inspect repo -> 2. Triage issue -> 3. Run reproduction -> 4. Patch code -> 5. Verify fix -> 6. Output clean Git diff
func (s *IssueSolver) Solve(ctx context.Context, issue IssueRequest) (*ResolutionReport, error) {
	// Step 1 & 2: Triage
	triage, err := s.TriageIssue(ctx, issue)
	if err != nil {
		return nil, fmt.Errorf("triage failed: %w", err)
	}

	// Step 3: Run reproduction test
	reproResult, _ := s.RunReproduction(ctx, issue.ReproCommand)

	// Step 4: Apply patch
	if issue.TargetFile != "" && issue.ProposedPatch != "" {
		patchErr := s.ApplyPatch(ctx, []FilePatch{
			{Path: issue.TargetFile, NewContent: issue.ProposedPatch},
		})
		if patchErr != nil {
			return nil, fmt.Errorf("failed to apply patch: %w", patchErr)
		}
	}

	// Step 5: Verify fix
	verifyResult, _ := s.VerifyFix(ctx, issue.VerifyCommand)

	// Step 6: Generate clean git diff
	diff, _ := s.GenerateDiff(ctx)

	commitMsg := fmt.Sprintf("fix: resolve %s\n\nRoot cause: %s", issue.Title, triage.SuspectedCause)

	return &ResolutionReport{
		// Success requires a verification command that actually ran and passed.
		Success:          verifyResult != nil && verifyResult.Success && !verifyResult.Skipped,
		Verified:         verifyResult != nil && verifyResult.Success && !verifyResult.Skipped,
		IssueTitle:       issue.Title,
		Triage:           triage,
		ReproductionTest: reproResult,
		VerificationTest: verifyResult,
		GitDiff:          diff,
		CommitMessage:    commitMsg,
		CompletedAt:      time.Now(),
	}, nil
}
