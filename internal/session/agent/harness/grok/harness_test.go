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

// drainEvents reads all currently-buffered events from the harness internalCh.
func drainEvents(h *GrokHarness) []monitor.AgentEvent {
	var evs []monitor.AgentEvent
	for {
		select {
		case ev := <-h.internalCh:
			evs = append(evs, ev)
		default:
			return evs
		}
	}
}

func TestHandleHookEvent_StopSettlesIdle(t *testing.T) {
	for _, event := range []string{"stop", "stop_failure", "stop_cancelled"} {
		t.Run(event, func(t *testing.T) {
			h := New(testRC(nil))
			if !h.HandleHookEvent(event, json.RawMessage(`{"reason":"end_turn"}`)) {
				t.Fatalf("HandleHookEvent(%q) = false, want true", event)
			}
			evs := drainEvents(h)
			if len(evs) != 1 {
				t.Fatalf("got %d events, want 1: %+v", len(evs), evs)
			}
			if evs[0].Type != monitor.EventStateChange {
				t.Fatalf("event type = %v, want EventStateChange", evs[0].Type)
			}
			data := evs[0].Data.(monitor.StateChangeData)
			if data.State != monitor.StateIdle || data.SubState != monitor.SubStateNone {
				t.Errorf("state = %v/%v, want Idle/None", data.State, data.SubState)
			}
		})
	}
}

func TestHandleHookEvent_UserPromptSubmitGoesActive(t *testing.T) {
	h := New(testRC(nil))
	if !h.HandleHookEvent("user_prompt_submit", json.RawMessage(`{}`)) {
		t.Fatal("HandleHookEvent(user_prompt_submit) = false, want true")
	}
	evs := drainEvents(h)
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2 (UserPrompt + StateChange): %+v", len(evs), evs)
	}
	if evs[0].Type != monitor.EventUserPrompt {
		t.Errorf("first event = %v, want EventUserPrompt", evs[0].Type)
	}
	data := evs[1].Data.(monitor.StateChangeData)
	if data.State != monitor.StateActive || data.SubState != monitor.SubStateThinking {
		t.Errorf("state = %v/%v, want Active/Thinking", data.State, data.SubState)
	}
}

func TestHandleHookEvent_NotificationIdlePrompt(t *testing.T) {
	h := New(testRC(nil))
	if !h.HandleHookEvent("notification", json.RawMessage(`{"notificationType":"idle_prompt"}`)) {
		t.Fatal("HandleHookEvent(notification idle_prompt) = false, want true")
	}
	evs := drainEvents(h)
	if len(evs) != 1 || evs[0].Data.(monitor.StateChangeData).State != monitor.StateIdle {
		t.Fatalf("want single Idle event, got %+v", evs)
	}
}

func TestHandleHookEvent_NotificationNonIdleIgnored(t *testing.T) {
	h := New(testRC(nil))
	if !h.HandleHookEvent("notification", json.RawMessage(`{"notificationType":"permission_prompt"}`)) {
		t.Fatal("HandleHookEvent should acknowledge a known notification event")
	}
	if evs := drainEvents(h); len(evs) != 0 {
		t.Errorf("non-idle notification should emit no state change, got %+v", evs)
	}
}

func TestHandleHookEvent_SubagentEventIgnored(t *testing.T) {
	h := New(testRC(nil))
	// A subagent stop must not settle the whole session to idle.
	if !h.HandleHookEvent("stop", json.RawMessage(`{"subagentType":"explore","reason":"end_turn"}`)) {
		t.Fatal("HandleHookEvent should acknowledge a subagent event")
	}
	if evs := drainEvents(h); len(evs) != 0 {
		t.Errorf("subagent stop should emit no state change, got %+v", evs)
	}
}

func TestHandleHookEvent_SessionEnd(t *testing.T) {
	h := New(testRC(nil))
	if !h.HandleHookEvent("session_end", json.RawMessage(`{}`)) {
		t.Fatal("HandleHookEvent(session_end) = false, want true")
	}
	evs := drainEvents(h)
	if len(evs) != 1 || evs[0].Type != monitor.EventSessionEnded {
		t.Fatalf("want single EventSessionEnded, got %+v", evs)
	}
}

func TestHandleHookEvent_UnknownReturnsFalse(t *testing.T) {
	h := New(testRC(nil))
	if h.HandleHookEvent("pre_tool_use", json.RawMessage(`{}`)) {
		t.Error("HandleHookEvent(pre_tool_use) should return false — not a state-affecting event")
	}
	if evs := drainEvents(h); len(evs) != 0 {
		t.Errorf("unknown event should emit nothing, got %+v", evs)
	}
}

func TestStartForwardsHookEvents(t *testing.T) {
	h := New(testRC(nil))
	if _, err := h.PrepareForLaunch(false); err != nil {
		t.Fatalf("PrepareForLaunch: %v", err)
	}
	defer h.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan monitor.AgentEvent, 16)
	go func() { _ = h.Start(ctx, events) }()

	h.HandleHookEvent("stop", json.RawMessage(`{"reason":"end_turn"}`))

	select {
	case ev := <-events:
		if ev.Type != monitor.EventStateChange {
			t.Fatalf("event type = %v, want EventStateChange", ev.Type)
		}
		if ev.Data.(monitor.StateChangeData).State != monitor.StateIdle {
			t.Errorf("state = %v, want Idle", ev.Data.(monitor.StateChangeData).State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not forward the hook-driven idle event")
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
