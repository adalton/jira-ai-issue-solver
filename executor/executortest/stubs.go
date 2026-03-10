// Package executortest provides test doubles for the executor package.
package executortest

import (
	"context"

	"jira-ai-issue-solver/executor"
	"jira-ai-issue-solver/jobmanager"
	"jira-ai-issue-solver/models"
)

// Compile-time checks.
var (
	_ executor.Executor        = (*Stub)(nil)
	_ executor.GitService      = (*StubGitService)(nil)
	_ executor.ProjectResolver = (*StubProjectResolver)(nil)
)

// Stub is a test double for [executor.Executor].
// Set the corresponding Func field to control each method's behavior.
// When a Func field is nil, the method returns zero values.
type Stub struct {
	ExecuteFunc func(ctx context.Context, job *jobmanager.Job) (jobmanager.JobResult, error)
}

func (s *Stub) Execute(ctx context.Context, job *jobmanager.Job) (jobmanager.JobResult, error) {
	if s.ExecuteFunc != nil {
		return s.ExecuteFunc(ctx, job)
	}
	return jobmanager.JobResult{}, nil
}

// StubGitService is a test double for [executor.GitService].
// Set the corresponding Func field to control each method's behavior.
// When a Func field is nil, the method returns zero values.
type StubGitService struct {
	CreateBranchFunc   func(dir, name string) error
	SwitchBranchFunc   func(dir, name string) error
	HasChangesFunc     func(dir string) (bool, error)
	CommitChangesFunc  func(owner, repo, branch, message, dir string, coAuthor *models.Author) (string, error)
	SyncWithRemoteFunc func(dir, branch string) error
	CreatePRFunc       func(params models.PRParams) (*models.PR, error)
}

func (s *StubGitService) CreateBranch(dir, name string) error {
	if s.CreateBranchFunc != nil {
		return s.CreateBranchFunc(dir, name)
	}
	return nil
}

func (s *StubGitService) SwitchBranch(dir, name string) error {
	if s.SwitchBranchFunc != nil {
		return s.SwitchBranchFunc(dir, name)
	}
	return nil
}

func (s *StubGitService) HasChanges(dir string) (bool, error) {
	if s.HasChangesFunc != nil {
		return s.HasChangesFunc(dir)
	}
	return false, nil
}

func (s *StubGitService) CommitChanges(owner, repo, branch, message, dir string, coAuthor *models.Author) (string, error) {
	if s.CommitChangesFunc != nil {
		return s.CommitChangesFunc(owner, repo, branch, message, dir, coAuthor)
	}
	return "", nil
}

func (s *StubGitService) SyncWithRemote(dir, branch string) error {
	if s.SyncWithRemoteFunc != nil {
		return s.SyncWithRemoteFunc(dir, branch)
	}
	return nil
}

func (s *StubGitService) CreatePR(params models.PRParams) (*models.PR, error) {
	if s.CreatePRFunc != nil {
		return s.CreatePRFunc(params)
	}
	return &models.PR{}, nil
}

// StubProjectResolver is a test double for [executor.ProjectResolver].
// Set the corresponding Func field to control each method's behavior.
// When a Func field is nil, the method returns zero values.
type StubProjectResolver struct {
	ResolveProjectFunc func(workItem models.WorkItem) (*executor.ProjectSettings, error)
}

func (s *StubProjectResolver) ResolveProject(workItem models.WorkItem) (*executor.ProjectSettings, error) {
	if s.ResolveProjectFunc != nil {
		return s.ResolveProjectFunc(workItem)
	}
	return &executor.ProjectSettings{}, nil
}
