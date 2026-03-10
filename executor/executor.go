// Package executor implements the job execution pipeline that
// processes work items from the job manager.
//
// The executor handles plumbing: workspace preparation, container
// lifecycle, task file creation, committing, PR management, and
// ticket status transitions. The AI agent handles thinking: code
// generation, validation, and fixing.
//
// # Consumer-defined interfaces
//
// The executor defines narrow interfaces for its dependencies
// ([GitService], [ProjectResolver]) rather than importing a shared
// interface package. This follows the Go convention of consumer-
// defined interfaces. Each consumer declares only the methods it
// requires; the underlying implementation satisfies all consumers.
// See docs/architecture-redesign.md for rationale.
//
// # Integration with JobManager
//
// The [Pipeline.Execute] method matches the [jobmanager.ExecuteFunc]
// signature, allowing direct injection into the Coordinator:
//
//	pipeline, _ := executor.NewPipeline(cfg, ...)
//	coordinator, _ := jobmanager.NewCoordinator(jmCfg, pipeline.Execute, logger)
//
// # Pipeline steps (new ticket)
//
//  1. Fetch work item details from the issue tracker
//  2. Resolve project-specific settings (repo, statuses, etc.)
//  3. Transition ticket to "in progress"
//  4. Prepare workspace (clone or reuse)
//  5. Create or switch to ticket branch
//  6. Write task file for the AI agent
//  7. Write provider-specific wrapper script
//  8. Load repo-level configuration hints
//  9. Resolve and start dev container (with fallback on failure)
//  10. Execute AI agent inside container
//  11. Check for changes; fail if none
//  12. Commit changes via GitHub API
//  13. Sync workspace with remote
//  14. Create pull request (draft if validation failed)
//  15. Update ticket with PR URL and transition status
//  16. Stop container (workspace retained for future jobs)
//
// Test doubles are provided in the [executortest] subpackage.
package executor

import (
	"context"
	"time"

	"jira-ai-issue-solver/jobmanager"
	"jira-ai-issue-solver/models"
)

// Executor runs jobs to completion. The Execute method matches
// [jobmanager.ExecuteFunc] for direct injection into the Coordinator.
type Executor interface {
	Execute(ctx context.Context, job *jobmanager.Job) (jobmanager.JobResult, error)
}

// GitService defines the git and GitHub API operations needed by the
// new-ticket execution pipeline. This is a consumer-defined interface
// containing only the methods this pipeline requires.
//
// The feedback pipeline (Task 8) and other consumers define their own
// interface slices. The underlying implementation (e.g.,
// services.GitHubServiceImpl) satisfies all of them.
type GitService interface {
	// CreateBranch creates a new git branch in the workspace and
	// switches to it.
	CreateBranch(dir, name string) error

	// SwitchBranch switches to an existing branch. Used when a
	// workspace is reused on retry.
	SwitchBranch(dir, name string) error

	// HasChanges reports whether the workspace has uncommitted
	// changes (modified, added, or deleted tracked files).
	HasChanges(dir string) (bool, error)

	// CommitChanges creates a verified commit via the GitHub API
	// from local workspace changes. Returns the commit SHA, or
	// empty string if there are no changes to commit.
	CommitChanges(owner, repo, branch, message, dir string,
		coAuthor *models.Author) (string, error)

	// SyncWithRemote reconciles the local workspace with the remote
	// branch after an API-created commit.
	SyncWithRemote(dir, branch string) error

	// CreatePR creates a pull request.
	CreatePR(params models.PRParams) (*models.PR, error)
}

// ProjectResolver maps work items to their project-specific settings.
// The implementation bridges between the bot's configuration model
// and the executor's needs.
type ProjectResolver interface {
	// ResolveProject returns the project-specific settings for the
	// given work item. Returns an error if the work item cannot be
	// mapped to a known project or repository.
	ResolveProject(workItem models.WorkItem) (*ProjectSettings, error)
}

// ProjectSettings contains the resolved per-project settings needed
// to execute a job for a specific work item.
type ProjectSettings struct {
	// Owner is the GitHub repository owner (e.g., "my-org").
	Owner string

	// Repo is the GitHub repository name (e.g., "backend").
	Repo string

	// CloneURL is the full clone URL for the repository.
	CloneURL string

	// BaseBranch is the target branch for pull requests (e.g., "main").
	BaseBranch string

	// InProgressStatus is the tracker status name for "in progress".
	InProgressStatus string

	// InReviewStatus is the tracker status name for "in review".
	InReviewStatus string

	// TodoStatus is the tracker status name to revert to on failure.
	TodoStatus string

	// PRURLFieldName is the custom field for storing the PR URL.
	// Empty means PR URL is posted as a structured comment instead.
	PRURLFieldName string

	// DisableErrorComments prevents posting error details as tracker
	// comments on job failure. Errors are still logged.
	DisableErrorComments bool

	// AIProvider overrides the default AI provider for this project.
	// Empty means use the pipeline's default provider.
	AIProvider string
}

// Config holds construction parameters for [Pipeline].
type Config struct {
	// BotUsername is used for branch naming
	// ("{bot-username}/{ticket-key}").
	BotUsername string

	// DefaultProvider is the AI provider used when the project
	// doesn't specify one (e.g., "claude", "gemini").
	DefaultProvider string

	// FallbackImage is the container image to try when the
	// project's resolved container image fails to start. Empty
	// disables fallback (start failure is immediately fatal).
	FallbackImage string

	// AIAPIKeys maps provider names to API key values injected
	// into the container environment (e.g., {"claude": "sk-..."}).
	AIAPIKeys map[string]string

	// SessionTimeout is the maximum duration for an AI session
	// inside the container. Zero means no explicit timeout (only
	// the parent context controls cancellation).
	SessionTimeout time.Duration
}
