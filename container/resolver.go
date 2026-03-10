package container

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

const (
	// DefaultFallbackImage is the built-in fallback container image
	// used when no other configuration source provides an image.
	// This is a minimal image; teams should provide their own image
	// with their toolchain and AI CLI installed.
	DefaultFallbackImage = "ubuntu:latest"

	// botConfigPath is the path (relative to repo root) for the
	// bot-specific container configuration file.
	botConfigPath = ".ai-bot/container.json"

	// devcontainerConfigPath is the path (relative to repo root) for
	// the standard devcontainer configuration file.
	devcontainerConfigPath = ".devcontainer/devcontainer.json"
)

// Resolver resolves container configuration for a repository by
// checking multiple sources in priority order and merging with defaults.
//
// The resolution chain (highest to lowest priority):
//  1. .ai-bot/container.json in the repository
//  2. .devcontainer/devcontainer.json in the repository (practical subset)
//  3. Bot-level defaults (defaultImage, defaultLimits)
//  4. Built-in minimal fallback ([DefaultFallbackImage])
//
// Only the highest-priority repo-level source is used: sources 1 and 2
// do not stack. Within the selected source, any field left unset falls
// through to bot-level defaults, then to the built-in fallback.
type Resolver struct {
	defaultImage  string
	defaultLimits ResourceLimits
	logger        *zap.Logger
}

// NewResolver creates a Resolver with the given bot-level defaults.
// The defaultImage and defaultLimits fill in gaps when a repository's
// config does not specify those values. Pass empty values to rely
// entirely on repository config or the built-in fallback.
func NewResolver(defaultImage string, defaultLimits ResourceLimits, logger *zap.Logger) (*Resolver, error) {
	if logger == nil {
		return nil, errors.New("logger must not be nil")
	}
	return &Resolver{
		defaultImage:  defaultImage,
		defaultLimits: defaultLimits,
		logger:        logger,
	}, nil
}

// Resolve determines the container configuration for the repository at
// repoDir. See [Resolver] for the resolution chain and merging rules.
func (r *Resolver) Resolve(repoDir string) (*Config, error) {
	// Try repo-level configs in priority order.
	repoCfg, err := r.tryBotConfig(repoDir)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", botConfigPath, err)
	}

	if repoCfg == nil {
		repoCfg, err = r.tryDevcontainer(repoDir)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", devcontainerConfigPath, err)
		}
	}

	// Build the resolved config: start with built-in fallback, layer
	// bot-level defaults, then overlay the repo-level config.
	resolved := &Config{
		Image:  DefaultFallbackImage,
		Source: "built-in fallback",
	}

	r.applyDefaults(resolved)

	if repoCfg != nil {
		overlay(resolved, repoCfg)
	}

	if resolved.Env == nil {
		resolved.Env = make(map[string]string)
	}

	return resolved, nil
}

// applyDefaults overlays bot-level defaults onto base. Non-empty
// default values override the corresponding base values.
func (r *Resolver) applyDefaults(base *Config) {
	if r.defaultImage != "" {
		base.Image = r.defaultImage
		base.Source = "bot default"
	}
	if r.defaultLimits.Memory != "" {
		base.ResourceLimits.Memory = r.defaultLimits.Memory
	}
	if r.defaultLimits.CPUs != "" {
		base.ResourceLimits.CPUs = r.defaultLimits.CPUs
	}
}

// overlay applies non-zero fields from src onto dst. Empty/zero fields
// in src are treated as "not set" and do not override dst.
func overlay(dst, src *Config) {
	if src.Image != "" {
		dst.Image = src.Image
	}
	if src.PostCreateCommand != "" {
		dst.PostCreateCommand = src.PostCreateCommand
	}
	if src.ResourceLimits.Memory != "" {
		dst.ResourceLimits.Memory = src.ResourceLimits.Memory
	}
	if src.ResourceLimits.CPUs != "" {
		dst.ResourceLimits.CPUs = src.ResourceLimits.CPUs
	}
	if len(src.Env) > 0 {
		if dst.Env == nil {
			dst.Env = make(map[string]string)
		}
		maps.Copy(dst.Env, src.Env)
	}
	if src.Source != "" {
		dst.Source = src.Source
	}
}

// --- .ai-bot/container.json ---

// tryBotConfig reads and parses .ai-bot/container.json from the repo.
// Returns (nil, nil) if the file does not exist.
func (r *Resolver) tryBotConfig(repoDir string) (*Config, error) {
	path := filepath.Join(repoDir, botConfigPath)

	data, err := os.ReadFile(path) // #nosec G304 -- path is repo dir + constant
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	r.logger.Debug("Found bot container config", zap.String("path", path))
	return parseBotConfig(data)
}

// botConfigJSON is the deserialization target for .ai-bot/container.json.
type botConfigJSON struct {
	Image             string            `json:"image"`
	PostCreateCommand string            `json:"postCreateCommand"`
	Env               map[string]string `json:"env"`
	ResourceLimits    *struct {
		Memory string `json:"memory"`
		CPUs   string `json:"cpus"`
	} `json:"resourceLimits"`
}

func parseBotConfig(data []byte) (*Config, error) {
	var raw botConfigJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	cfg := &Config{
		Image:             raw.Image,
		PostCreateCommand: raw.PostCreateCommand,
		Env:               raw.Env,
		Source:            botConfigPath,
	}
	if raw.ResourceLimits != nil {
		cfg.ResourceLimits.Memory = raw.ResourceLimits.Memory
		cfg.ResourceLimits.CPUs = raw.ResourceLimits.CPUs
	}
	return cfg, nil
}

// --- .devcontainer/devcontainer.json ---

// tryDevcontainer reads and parses .devcontainer/devcontainer.json from
// the repo. Returns (nil, nil) if the file does not exist.
func (r *Resolver) tryDevcontainer(repoDir string) (*Config, error) {
	path := filepath.Join(repoDir, devcontainerConfigPath)

	data, err := os.ReadFile(path) // #nosec G304 -- path is repo dir + constant
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	r.logger.Debug("Found devcontainer config", zap.String("path", path))
	return r.parseDevcontainer(data)
}

// devcontainerJSON is the deserialization target for devcontainer.json.
// Only a practical subset of the devcontainer spec is supported:
// image, postCreateCommand, and containerEnv.
type devcontainerJSON struct {
	Image             string            `json:"image"`
	PostCreateCommand json.RawMessage   `json:"postCreateCommand"`
	ContainerEnv      map[string]string `json:"containerEnv"`
}

// devcontainerUnsupportedFields lists devcontainer.json fields that we
// detect but do not support, so we can warn operators.
var devcontainerUnsupportedFields = []string{
	"build",
	"features",
	"forwardPorts",
	"customizations",
	"mounts",
	"runArgs",
	"remoteUser",
	"remoteEnv",
}

func (r *Resolver) parseDevcontainer(data []byte) (*Config, error) {
	cleaned := stripJSONComments(data)

	var raw devcontainerJSON
	if err := json.Unmarshal(cleaned, &raw); err != nil {
		return nil, err
	}

	r.warnUnsupportedFields(cleaned)

	return &Config{
		Image:             raw.Image,
		PostCreateCommand: r.parsePostCreateCommand(raw.PostCreateCommand),
		Env:               raw.ContainerEnv,
		Source:            devcontainerConfigPath,
	}, nil
}

func (r *Resolver) warnUnsupportedFields(data []byte) {
	var rawMap map[string]json.RawMessage
	if json.Unmarshal(data, &rawMap) != nil {
		return
	}
	for _, field := range devcontainerUnsupportedFields {
		if _, ok := rawMap[field]; ok {
			r.logger.Warn("Unsupported devcontainer.json field ignored",
				zap.String("field", field))
		}
	}
}

// parsePostCreateCommand handles the devcontainer spec's
// postCreateCommand, which can be a string or an array of strings.
// Unrecognized formats (e.g., object form) are logged and ignored.
func (r *Resolver) parsePostCreateCommand(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return strings.Join(arr, " && ")
	}

	r.logger.Warn("Unsupported postCreateCommand format ignored (expected string or string array)",
		zap.String("raw", string(raw)))
	return ""
}

// --- JSONC support ---

// stripJSONComments removes single-line (//) and multi-line (/* */)
// comments from JSONC data, then removes trailing commas before } and ].
// String contents are preserved: comment and comma patterns inside
// quoted strings are not modified.
func stripJSONComments(data []byte) []byte {
	withoutComments := removeComments(data)
	return removeTrailingCommas(withoutComments)
}

// removeComments strips // and /* */ comments from JSONC, preserving
// string contents.
func removeComments(data []byte) []byte {
	result := make([]byte, 0, len(data))
	inString := false

	for i := 0; i < len(data); {
		ch := data[i]

		if ch == '"' && !isEscaped(data, i) {
			inString = !inString
			result = append(result, ch)
			i++
			continue
		}

		if inString {
			result = append(result, ch)
			i++
			continue
		}

		// Single-line comment.
		if i+1 < len(data) && ch == '/' && data[i+1] == '/' {
			i += 2
			for i < len(data) && data[i] != '\n' {
				i++
			}
			continue
		}

		// Multi-line comment.
		if i+1 < len(data) && ch == '/' && data[i+1] == '*' {
			i += 2
			for i+1 < len(data) && (data[i] != '*' || data[i+1] != '/') {
				i++
			}
			if i+1 < len(data) {
				i += 2
			}
			continue
		}

		result = append(result, ch)
		i++
	}

	return result
}

// removeTrailingCommas removes commas that appear immediately before
// } or ] (with only whitespace between), preserving string contents.
func removeTrailingCommas(data []byte) []byte {
	result := make([]byte, 0, len(data))
	inString := false

	for i := range len(data) {
		ch := data[i]

		if ch == '"' && !isEscaped(data, i) {
			inString = !inString
			result = append(result, ch)
			continue
		}

		if inString {
			result = append(result, ch)
			continue
		}

		if ch == ',' {
			// Look ahead past whitespace for a closing bracket.
			j := i + 1
			for j < len(data) && isJSONWhitespace(data[j]) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue // skip trailing comma
			}
		}

		result = append(result, ch)
	}

	return result
}

// isEscaped reports whether the byte at position i is preceded by an
// odd number of backslashes (meaning it is escaped).
func isEscaped(data []byte, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && data[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

func isJSONWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
