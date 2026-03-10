package mocks

import (
	"context"

	"jira-ai-issue-solver/container"
)

// Compile-time check that MockContainerManager implements container.Manager.
var _ container.Manager = (*MockContainerManager)(nil)

// MockContainerManager is a test double for container.Manager.
// Set the corresponding Func field to control each method's behavior.
// When a Func field is nil, the method returns zero values.
type MockContainerManager struct {
	ResolveConfigFunc  func(repoDir string) (*container.Config, error)
	StartFunc          func(ctx context.Context, cfg *container.Config, workspaceDir string, env map[string]string) (*container.Container, error)
	ExecFunc           func(ctx context.Context, ctr *container.Container, cmd []string) (string, int, error)
	StopFunc           func(ctx context.Context, ctr *container.Container) error
	CleanupOrphansFunc func(ctx context.Context, prefix string) error
}

func (m *MockContainerManager) ResolveConfig(repoDir string) (*container.Config, error) {
	if m.ResolveConfigFunc != nil {
		return m.ResolveConfigFunc(repoDir)
	}
	return &container.Config{}, nil
}

func (m *MockContainerManager) Start(ctx context.Context, cfg *container.Config, workspaceDir string, env map[string]string) (*container.Container, error) {
	if m.StartFunc != nil {
		return m.StartFunc(ctx, cfg, workspaceDir, env)
	}
	return &container.Container{}, nil
}

func (m *MockContainerManager) Exec(ctx context.Context, ctr *container.Container, cmd []string) (string, int, error) {
	if m.ExecFunc != nil {
		return m.ExecFunc(ctx, ctr, cmd)
	}
	return "", 0, nil
}

func (m *MockContainerManager) Stop(ctx context.Context, ctr *container.Container) error {
	if m.StopFunc != nil {
		return m.StopFunc(ctx, ctr)
	}
	return nil
}

func (m *MockContainerManager) CleanupOrphans(ctx context.Context, prefix string) error {
	if m.CleanupOrphansFunc != nil {
		return m.CleanupOrphansFunc(ctx, prefix)
	}
	return nil
}
