package grok

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"h2/internal/config"
	"h2/internal/session/agent/harness"
	"h2/internal/session/agent/monitor"
)

// Verify GrokHarness implements harness.Harness.
var _ harness.Harness = (*GrokHarness)(nil)

func testRC(mutate func(*config.RuntimeConfig)) *config.RuntimeConfig {
	rc := &config.RuntimeConfig{HarnessType: "grok", Command: "grok", AgentName: "test", CWD: "/tmp", StartedAt: "2024-01-01T00:00:00Z"}
	if mutate != nil {
		mutate(rc)
	}
	return rc
}

// --- Identity tests ---

func TestIdentity(t *testing.T) {
	h := New(testRC(nil))
	if h.Name() != "grok" {
		t.Errorf("Name() = %q, want %q", h.Name(), "grok")
	}
	if h.Command() != "grok" {
		t.Errorf("Command() = %q, want %q", h.Command(), "grok")
	}
	if h.DisplayCommand() != "grok" {
		t.Errorf("DisplayCommand() = %q, want %q", h.DisplayCommand(), "grok")
	}
	if !h.SupportsResume() {
		t.Error("SupportsResume() = false, want true")
	}
}

// --- BuildCommandArgs tests ---

func TestBuildCommandArgs_EmptyConfig_NoFlags(t *testing.T) {
	args := New(testRC(nil)).BuildCommandArgs(nil, nil)
	if len(args) != 0 {
		t.Fatalf("expected [] for empty config, got %v", args)
	}
}

func TestBuildCommandArgs_AllFields(t *testing.T) {
	h := New(testRC(func(rc *config.RuntimeConfig) {
		rc.SessionID = "11111111-2222-3333-4444-555555555555"
		rc.SystemPrompt = "You are a robot"
		rc.Instructions = "Follow the rules"
		rc.Model = "grok-4-5"
		rc.GrokPermissionMode = "dontAsk"
	}))
	got := h.BuildCommandArgs(nil, nil)
	want := []string{
		"--session-id", "11111111-2222-3333-4444-555555555555",
		"--system-prompt-override", "You are a robot",
		"--rules", "Follow the rules",
		"--model", "grok-4-5",
		"--permission-mode", "dontAsk",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildCommandArgs() = %v, want %v", got, want)
	}
}

func TestBuildCommandArgs_ResumeOnly(t *testing.T) {
	// Resume mode must emit only --resume: the resumed session already has
	// its system prompt, rules, and permission mode.
	h := New(testRC(func(rc *config.RuntimeConfig) {
		rc.ResumeSessionID = "abc-123"
		rc.SystemPrompt = "ignored"
		rc.Instructions = "ignored"
		rc.Model = "ignored"
		rc.GrokPermissionMode = "dontAsk"
	}))
	got := h.BuildCommandArgs(nil, nil)
	want := []string{"--resume", "abc-123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildCommandArgs() = %v, want %v", got, want)
	}
}

func TestBuildCommandArgs_PrependAndExtraOrder(t *testing.T) {
	h := New(testRC(func(rc *config.RuntimeConfig) { rc.Model = "grok-4-5" }))
	got := h.BuildCommandArgs([]string{"pre"}, []string{"extra"})
	want := []string{"pre", "extra", "--model", "grok-4-5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildCommandArgs() = %v, want %v", got, want)
	}
}

// --- Env / config dir tests ---

func TestBuildCommandEnvVars_SetsGrokHome(t *testing.T) {
	h := New(testRC(func(rc *config.RuntimeConfig) {
		rc.HarnessConfigPathPrefix = "/h2dir/grok-config"
		rc.Profile = "default"
	}))
	env := h.BuildCommandEnvVars("/h2dir")
	if env["GROK_HOME"] != "/h2dir/grok-config/default" {
		t.Fatalf("GROK_HOME = %q, want %q", env["GROK_HOME"], "/h2dir/grok-config/default")
	}
}

func TestBuildCommandEnvVars_NoPrefix_NoEnv(t *testing.T) {
	if env := New(testRC(nil)).BuildCommandEnvVars("/h2dir"); env != nil {
		t.Fatalf("expected nil env without config prefix, got %v", env)
	}
}

func TestEnsureConfigDir_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	h := New(testRC(func(rc *config.RuntimeConfig) {
		rc.HarnessConfigPathPrefix = dir + "/grok-config"
		rc.Profile = "p1"
	}))
	if err := h.EnsureConfigDir(dir); err != nil {
		t.Fatalf("EnsureConfigDir: %v", err)
	}
	if _, err := New(testRC(nil)).PrepareForLaunch(true); err != nil {
		t.Fatalf("PrepareForLaunch(dryRun): %v", err)
	}
}

// --- Runtime tests (ptycollector-based, mirrors generic harness) ---

func TestStartAndOutputIdleDetection(t *testing.T) {
	h := New(testRC(nil))
	if _, err := h.PrepareForLaunch(false); err != nil {
		t.Fatalf("PrepareForLaunch: %v", err)
	}
	defer h.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan monitor.AgentEvent, 16)
	go func() { _ = h.Start(ctx, events) }()

	h.HandleOutput()
	select {
	case ev := <-events:
		if ev.Type != monitor.EventStateChange {
			t.Fatalf("event type = %v, want %v", ev.Type, monitor.EventStateChange)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no state change event after output signal")
	}
}

func TestHandleHookEvent_Unsupported(t *testing.T) {
	if New(testRC(nil)).HandleHookEvent("PreToolUse", json.RawMessage(`{}`)) {
		t.Error("HandleHookEvent should return false — grok harness has no hook integration")
	}
}

func TestHandleInterrupt_BeforePrepare(t *testing.T) {
	if New(testRC(nil)).HandleInterrupt() {
		t.Error("HandleInterrupt before PrepareForLaunch should return false")
	}
}

// --- Registry tests ---

func TestRegistered(t *testing.T) {
	for _, name := range []string{"grok", "grok_build"} {
		if got := harness.CanonicalName(name); got != "grok" {
			t.Errorf("CanonicalName(%q) = %q, want %q", name, got, "grok")
		}
	}
	if got := harness.DefaultCommand("grok"); got != "grok" {
		t.Errorf("DefaultCommand(grok) = %q, want %q", got, "grok")
	}
}
