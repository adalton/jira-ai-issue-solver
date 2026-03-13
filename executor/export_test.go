package executor

import (
	"jira-ai-issue-solver/models"
	"jira-ai-issue-solver/repoconfig"
)

// MergeImports exposes mergeImports for testing.
func MergeImports(
	settings *models.ProjectSettings,
	repoCfg *repoconfig.Config,
) []ImportEntry {
	return mergeImports(settings, repoCfg)
}

// ImportEntry is the exported version of importEntry for tests.
type ImportEntry = importEntry

// ExcludeImportPaths exposes excludeImportPaths for testing.
func ExcludeImportPaths(wsPath string, imports []ImportEntry) error {
	return excludeImportPaths(wsPath, imports)
}
