package devrel

import "time"

// IssueRequest represents an incoming issue or bug report
type IssueRequest struct {
	Title         string `json:"title"`
	Description   string `json:"description"`
	ReproCommand  string `json:"repro_command,omitempty"`
	VerifyCommand string `json:"verify_command,omitempty"`
	TargetFile    string `json:"target_file,omitempty"`
	ProposedPatch string `json:"proposed_patch,omitempty"`
}

// RepoInspection holds structural and metadata information about the repository
type RepoInspection struct {
	Workdir      string    `json:"workdir"`
	CurrentBranch string   `json:"current_branch"`
	LastCommit   string    `json:"last_commit"`
	Status       string    `json:"status"`
	Files        []string  `json:"files"`
	ProjectType  string    `json:"project_type"`
	InspectedAt  time.Time `json:"inspected_at"`
}

// TriageResult contains analysis and plan for addressing an issue
type TriageResult struct {
	IssueTitle      string   `json:"issue_title"`
	Summary         string   `json:"summary"`
	SuspectedCause  string   `json:"suspected_cause"`
	CandidateFiles  []string `json:"candidate_files"`
	ReproStrategy   string   `json:"repro_strategy"`
	SuggestedFix    string   `json:"suggested_fix"`
}

// ExecutionResult captures the output of a test/reproduction run
type ExecutionResult struct {
	Command  string        `json:"command"`
	Success  bool          `json:"success"`
	Output   string        `json:"output"`
	Duration time.Duration `json:"duration"`
}

// FilePatch represents a file modification
type FilePatch struct {
	Path        string `json:"path"`
	NewContent  string `json:"new_content"`
}

// ResolutionReport summarizes the autonomous triage, reproduction, patch, and diff
type ResolutionReport struct {
	Success          bool             `json:"success"`
	IssueTitle       string           `json:"issue_title"`
	Triage           *TriageResult    `json:"triage"`
	ReproductionTest *ExecutionResult `json:"reproduction_test"`
	VerificationTest *ExecutionResult `json:"verification_test"`
	GitDiff          string           `json:"git_diff"`
	CommitMessage    string           `json:"commit_message"`
	CompletedAt      time.Time        `json:"completed_at"`
}
