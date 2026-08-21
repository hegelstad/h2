package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// EnsureGrokConfigDir creates the shared Grok config directory (GROK_HOME) and
// writes the h2 standard hook registrations to <configDir>/hooks/h2.json if it
// doesn't exist yet. Grok Build discovers *.json under $GROK_HOME/hooks as
// always-trusted global-scope hooks, so this puts Grok on the same
// event-driven turn-completion footing as the Claude harness.
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

// buildGrokHooks builds the Grok hook registrations. All events use the unified
// "h2 handle-hook" command, which forwards the event to the agent's session over
// its unix socket. Every registration is passive (exits 0 with "{}"), so the
// Stop hook never blocks a Grok turn.
//
// The set follows Grok's documented complete busy/idle indicator:
//   - UserPromptSubmit marks the session busy;
//   - Stop / StopFailure / StopCancelled settle it however the turn ended;
//   - Notification(idle_prompt) is the backstop for turns that report none of
//     the three;
//   - SessionEnd settles a session that exits before the idle ping.
func buildGrokHooks() map[string][]hookMatcher {
	// Passive observe hooks: keep the timeout short since the handler only
	// dials a local socket and returns.
	hook := hookEntry{
		Type:    "command",
		Command: "h2 handle-hook",
		Timeout: 10,
	}

	settleEvents := []string{
		"UserPromptSubmit",
		"Stop",
		"StopFailure",
		"StopCancelled",
		"SessionEnd",
	}

	hooks := make(map[string][]hookMatcher)
	for _, event := range settleEvents {
		hooks[event] = []hookMatcher{{
			Matcher: "",
			Hooks:   []hookEntry{hook},
		}}
	}

	// Notification is registered only for the idle_prompt backstop.
	hooks["Notification"] = []hookMatcher{{
		Matcher: "idle_prompt",
		Hooks:   []hookEntry{hook},
	}}

	return hooks
}
