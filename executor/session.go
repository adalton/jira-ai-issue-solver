package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// sessionOutputPath is the path, relative to the workspace root,
// where the wrapper script writes AI session metadata.
const sessionOutputPath = ".ai-bot/session-output.json"

// SessionOutput holds the AI session results parsed from
// session-output.json. The wrapper script (run.sh) writes this file
// after the AI CLI exits.
type SessionOutput struct {
	// ExitCode is the AI CLI's exit code.
	ExitCode int `json:"exit_code"`

	// CostUSD is the session cost reported by the AI provider.
	CostUSD float64 `json:"cost_usd"`

	// ValidationPassed indicates whether the AI's own validation
	// succeeded. Nil means unknown (field absent or not reported).
	ValidationPassed *bool `json:"validation_passed"`

	// Summary is a brief description of what the AI did.
	Summary string `json:"summary"`
}

// readSessionOutput reads and parses the session output file from the
// workspace. Returns a zero-value SessionOutput if the file does not
// exist or cannot be parsed. Missing files are expected when the
// container is killed by timeout before the wrapper script finishes.
func readSessionOutput(dir string) SessionOutput {
	path := filepath.Join(dir, sessionOutputPath)

	data, err := os.ReadFile(path) // #nosec G304 -- path is dir + constant
	if err != nil {
		return SessionOutput{}
	}

	var output SessionOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return SessionOutput{}
	}

	return output
}
