package mocks

import (
	"time"

	"jira-ai-issue-solver/workspace"
)

// Compile-time check that MockWorkspaceManager implements workspace.Manager.
var _ workspace.Manager = (*MockWorkspaceManager)(nil)

// MockWorkspaceManager is a test double for workspace.Manager.
// Set the corresponding Func field to control each method's behavior.
// When a Func field is nil, the method returns zero values.
type MockWorkspaceManager struct {
	CreateFunc          func(ticketKey, repoURL string) (string, error)
	FindFunc            func(ticketKey string) (string, bool)
	FindOrCreateFunc    func(ticketKey, repoURL string) (string, bool, error)
	CleanupFunc         func(ticketKey string) error
	CleanupStaleFunc    func(maxAge time.Duration) (int, error)
	CleanupByFilterFunc func(shouldRemove func(ticketKey string) bool) (int, error)
	ListFunc            func() ([]workspace.Info, error)
}

func (m *MockWorkspaceManager) Create(ticketKey, repoURL string) (string, error) {
	if m.CreateFunc != nil {
		return m.CreateFunc(ticketKey, repoURL)
	}
	return "", nil
}

func (m *MockWorkspaceManager) Find(ticketKey string) (string, bool) {
	if m.FindFunc != nil {
		return m.FindFunc(ticketKey)
	}
	return "", false
}

func (m *MockWorkspaceManager) FindOrCreate(ticketKey, repoURL string) (string, bool, error) {
	if m.FindOrCreateFunc != nil {
		return m.FindOrCreateFunc(ticketKey, repoURL)
	}
	return "", false, nil
}

func (m *MockWorkspaceManager) Cleanup(ticketKey string) error {
	if m.CleanupFunc != nil {
		return m.CleanupFunc(ticketKey)
	}
	return nil
}

func (m *MockWorkspaceManager) CleanupStale(maxAge time.Duration) (int, error) {
	if m.CleanupStaleFunc != nil {
		return m.CleanupStaleFunc(maxAge)
	}
	return 0, nil
}

func (m *MockWorkspaceManager) CleanupByFilter(shouldRemove func(ticketKey string) bool) (int, error) {
	if m.CleanupByFilterFunc != nil {
		return m.CleanupByFilterFunc(shouldRemove)
	}
	return 0, nil
}

func (m *MockWorkspaceManager) List() ([]workspace.Info, error) {
	if m.ListFunc != nil {
		return m.ListFunc()
	}
	return []workspace.Info{}, nil
}
