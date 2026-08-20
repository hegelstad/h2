package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureGrokConfigDir_WritesHooks(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "grok-config", "default")

	if err := EnsureGrokConfigDir(configDir); err != nil {
		t.Fatalf("EnsureGrokConfigDir: %v", err)
	}

	hooksPath := filepath.Join(configDir, "hooks", "h2.json")
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks file: %v", err)
	}

	var parsed struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse hooks json: %v", err)
	}

	// All five settle events plus Notification must be registered.
	for _, event := range []string{"UserPromptSubmit", "Stop", "StopFailure", "StopCancelled", "SessionEnd", "Notification"} {
		groups, ok := parsed.Hooks[event]
		if !ok || len(groups) == 0 {
			t.Fatalf("event %q not registered", event)
		}
		if len(groups[0].Hooks) == 0 {
			t.Fatalf("event %q has no handlers", event)
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" || h.Command != "h2 handle-hook" {
			t.Errorf("event %q handler = {%q,%q}, want command/h2 handle-hook", event, h.Type, h.Command)
		}
	}

	// Notification must filter to idle_prompt only.
	if got := parsed.Hooks["Notification"][0].Matcher; got != "idle_prompt" {
		t.Errorf("Notification matcher = %q, want idle_prompt", got)
	}

	// Settle events match everything (empty matcher).
	if got := parsed.Hooks["Stop"][0].Matcher; got != "" {
		t.Errorf("Stop matcher = %q, want empty", got)
	}
}

func TestEnsureGrokConfigDir_Idempotent(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "grok-config", "p1")

	if err := EnsureGrokConfigDir(configDir); err != nil {
		t.Fatalf("first EnsureGrokConfigDir: %v", err)
	}

	hooksPath := filepath.Join(configDir, "hooks", "h2.json")
	// Overwrite with a sentinel to prove the second call does not clobber it.
	sentinel := []byte(`{"hooks":{"custom":[]}}`)
	if err := os.WriteFile(hooksPath, sentinel, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureGrokConfigDir(configDir); err != nil {
		t.Fatalf("second EnsureGrokConfigDir: %v", err)
	}

	after, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(sentinel) {
		t.Errorf("existing hooks file was overwritten; got %q", string(after))
	}
}
