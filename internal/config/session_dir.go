package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SessionsDir returns the directory where agent session dirs are created (~/.h2/sessions/).
func SessionsDir() string {
	return filepath.Join(ConfigDir(), "sessions")
}

// SessionDir returns the session directory for a given agent name.
func SessionDir(agentName string) string {
	return filepath.Join(SessionsDir(), agentName)
}

// FindSessionDirByAgentName returns the session directory for an agent if it exists.
// Returns empty string if not found.
func FindSessionDirByAgentName(agentName string) string {
	dir := SessionDir(agentName)
	if _, err := os.Stat(dir); err != nil {
		return ""
	}
	return dir
}

// FindSessionDirByID returns the session directory whose RuntimeConfig has the
// given h2 SessionID. Empty string means not found.
func FindSessionDirByID(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	return findSessionDirMatching(func(rc *RuntimeConfig) bool {
		return rc.SessionID == sessionID
	})
}

// FindSessionDirByHarnessSessionID returns the session directory whose
// RuntimeConfig has the given HarnessSessionID — the session ID as known by the
// underlying harness (Claude Code's session UUID, Codex's conversation id).
// Empty string means not found.
func FindSessionDirByHarnessSessionID(harnessSessionID string) string {
	if harnessSessionID == "" {
		return ""
	}
	return findSessionDirMatching(func(rc *RuntimeConfig) bool {
		return rc.HarnessSessionID == harnessSessionID
	})
}

// findSessionDirMatching scans the session directories and returns the first one
// whose RuntimeConfig satisfies pred. Directories with missing or invalid
// metadata are skipped. Empty string means no match.
func findSessionDirMatching(pred func(*RuntimeConfig) bool) string {
	root := SessionsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if rc, err := ReadRuntimeConfig(dir); err == nil && pred(rc) {
			return dir
		}
	}
	return ""
}

// SessionLastActivity returns the last modification time of the events.jsonl
// file in the session directory, which represents the last activity time.
// Falls back to the directory's mod time if events.jsonl doesn't exist.
func SessionLastActivity(sessionDir string) time.Time {
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	if info, err := os.Stat(eventsPath); err == nil {
		return info.ModTime()
	}
	if info, err := os.Stat(sessionDir); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// ListSessionConfigs reads all session directories and returns their RuntimeConfigs.
// Directories with missing or invalid metadata are silently skipped.
func ListSessionConfigs() []*RuntimeConfig {
	root := SessionsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var configs []*RuntimeConfig
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		rc, err := ReadRuntimeConfig(dir)
		if err != nil {
			continue
		}
		configs = append(configs, rc)
	}
	return configs
}

// SetupSessionDir creates the session directory for an agent. Claude Code
// config (auth, hooks, settings) lives in the shared claude config dir, not
// here. Permission review config (including AI reviewer instructions) is
// stored in the RuntimeConfig written to session.metadata.json by the launcher.
func SetupSessionDir(agentName string, role *Role) (string, error) {
	sessionDir := SessionDir(agentName)

	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}

	return sessionDir, nil
}

// EnsureClaudeConfigDir creates the shared Claude config directory and writes
// the h2 standard settings.json (hooks + permissions) if it doesn't exist yet.
func EnsureClaudeConfigDir(configDir string) error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}

	// Write settings.json with h2 hooks if it doesn't exist.
	settingsPath := filepath.Join(configDir, "settings.json")
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		settings := buildH2Settings()
		settingsJSON, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal settings.json: %w", err)
		}
		if err := os.WriteFile(settingsPath, settingsJSON, 0o644); err != nil {
			return fmt.Errorf("write settings.json: %w", err)
		}
	}

	return nil
}

// EnsureGrokConfigDir creates the shared Grok config directory (GROK_HOME) and
// writes the h2 standard hook registrations to <configDir>/hooks/h2.json if it
// doesn't exist yet. Grok discovers *.json under $GROK_HOME/hooks as
// always-trusted global-scope hooks (Claude-compatible hooks, grok >= 1.0.4),
// so this puts grok on the same event-driven turn-done footing as claude:
// `h2 handle-hook` fires on Stop/StopCancelled/StopFailure/SessionStart/
// SessionEnd/Notification(idle_prompt)/UserPromptSubmit/PreToolUse and the
// harness's EventHandler translates them into AgentEvents.
//
// All registrations are passive (the handler exits 0 printing "{}"), so the
// Stop gate never blocks a grok turn. Timeout is 10s — well under Stop's 600s
// gate default, generous for a local unix-socket dial.
func EnsureGrokConfigDir(configDir string) error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("create grok config dir: %w", err)
	}

	hooksDir := filepath.Join(configDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("create grok hooks dir: %w", err)
	}

	hooksPath := filepath.Join(hooksDir, "h2.json")
	if _, err := os.Stat(hooksPath); os.IsNotExist(err) {
		payload := map[string]any{"hooks": buildGrokHooks()}
		hooksJSON, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal grok hooks: %w", err)
		}
		if err := os.WriteFile(hooksPath, hooksJSON, 0o644); err != nil {
			return fmt.Errorf("write grok hooks: %w", err)
		}
	}

	return nil
}

// hookEntry represents a single hook in the settings.json hooks array.
type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// hookMatcher represents a matcher + hooks pair in settings.json.
type hookMatcher struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

// buildH2Settings constructs the settings.json content with h2 standard hooks.
func buildH2Settings() map[string]any {
	settings := make(map[string]any)
	settings["hooks"] = buildH2Hooks()
	return settings
}

// buildH2Hooks creates the hooks section with h2 standard hooks.
// All events use the unified "h2 handle-hook" command which forwards
// events to the agent and handles PermissionRequest review.
func buildH2Hooks() map[string][]hookMatcher {
	hook := hookEntry{
		Type:    "command",
		Command: "h2 handle-hook",
		Timeout: 5,
	}

	// Standard hook events.
	standardEvents := []string{
		"PreToolUse",
		"PostToolUse",
		"PostToolUseFailure",
		"PreCompact",
		"SessionStart",
		"SessionEnd",
		"Stop",
		"UserPromptSubmit",
	}

	hooks := make(map[string][]hookMatcher)

	for _, event := range standardEvents {
		hooks[event] = []hookMatcher{{
			Matcher: "",
			Hooks:   []hookEntry{hook},
		}}
	}

	// PermissionRequest needs a longer timeout for the AI reviewer.
	permissionHook := hookEntry{
		Type:    "command",
		Command: "h2 handle-hook",
		Timeout: 60,
	}
	hooks["PermissionRequest"] = []hookMatcher{{
		Matcher: "",
		Hooks:   []hookEntry{permissionHook},
	}}

	return hooks
}

// buildGrokHooks builds the Grok hook registrations. Every event uses the
// unified "h2 handle-hook" command, which forwards the event payload to the
// agent's session over its unix socket. Event names are grok's PascalCase
// lifecycle events per the grok hooks spec (~/.grok/docs/user-guide/10-hooks.md).
func buildGrokHooks() map[string][]hookMatcher {
	hook := hookEntry{
		Type:    "command",
		Command: "h2 handle-hook",
		Timeout: 10,
	}

	events := []string{
		"SessionStart",
		"SessionEnd",
		"Stop",
		"StopCancelled",
		"StopFailure",
		"UserPromptSubmit",
		"PreToolUse",
	}

	hooks := make(map[string][]hookMatcher)
	for _, event := range events {
		hooks[event] = []hookMatcher{{
			Matcher: "",
			Hooks:   []hookEntry{hook},
		}}
	}

	// Notification only for the idle_prompt backstop: turns whose end is not
	// reported by Stop/StopCancelled/StopFailure still settle via idle_prompt.
	hooks["Notification"] = []hookMatcher{{
		Matcher: "idle_prompt",
		Hooks:   []hookEntry{hook},
	}}

	return hooks
}
