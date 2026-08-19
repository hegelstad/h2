package grok

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
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

// --- Runtime tests (screen-content idle detection) ---

// fakeScreen is a mutable screen source for driving the classifier in tests.
type fakeScreen struct {
	mu   sync.Mutex
	text string
}

func (f *fakeScreen) set(s string) {
	f.mu.Lock()
	f.text = s
	f.mu.Unlock()
}

func (f *fakeScreen) get() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.text
}

func eventState(t *testing.T, ev monitor.AgentEvent) monitor.State {
	t.Helper()
	if ev.Type != monitor.EventStateChange {
		t.Fatalf("event type = %v, want %v", ev.Type, monitor.EventStateChange)
	}
	scd, ok := ev.Data.(monitor.StateChangeData)
	if !ok {
		t.Fatalf("event data = %T, want monitor.StateChangeData", ev.Data)
	}
	return scd.State
}

// waitForState drains events until it sees want or times out.
func waitForState(t *testing.T, events <-chan monitor.AgentEvent, want monitor.State) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if eventState(t, ev) == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for state %v", want)
		}
	}
}

func startHarness(t *testing.T, screen func() string) (*GrokHarness, <-chan monitor.AgentEvent, context.CancelFunc) {
	t.Helper()
	h := New(testRC(nil))
	if _, err := h.PrepareForLaunch(false); err != nil {
		t.Fatalf("PrepareForLaunch: %v", err)
	}
	h.SetScreenSource(screen)
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan monitor.AgentEvent, 64)
	go func() { _ = h.Start(ctx, events) }()
	return h, events, cancel
}

// Start emits an initial Active, then Idle once the screen shows the grok
// prompt (a ready marker, no active marker) for the debounce window.
func TestStart_ActiveThenIdleFromScreen(t *testing.T) {
	fs := &fakeScreen{text: ""} // unknown during "startup"
	_, events, cancel := startHarness(t, fs.get)
	defer cancel()

	// Initial emit is Active (child launching / not yet at prompt).
	if got := eventState(t, <-events); got != monitor.StateActive {
		t.Fatalf("first emit = %v, want Active", got)
	}
	// Screen reaches the idle prompt -> harness should declare Idle.
	fs.set(miniIdleScreen)
	waitForState(t, events, monitor.StateIdle)
}

// A turn indicator on screen flips the harness back to Active.
func TestStart_IdleThenActiveOnTurn(t *testing.T) {
	fs := &fakeScreen{text: miniIdleScreen}
	_, events, cancel := startHarness(t, fs.get)
	defer cancel()

	waitForState(t, events, monitor.StateIdle)
	fs.set(miniActiveScreen)
	waitForState(t, events, monitor.StateActive)
}

// HandleInterrupt forces an immediate Idle even while a turn indicator shows.
func TestHandleInterrupt_ForcesIdle(t *testing.T) {
	fs := &fakeScreen{text: miniActiveScreen}
	h, events, cancel := startHarness(t, fs.get)
	defer cancel()

	// Confirm it settled Active on the turn screen first.
	if got := eventState(t, <-events); got != monitor.StateActive {
		t.Fatalf("first emit = %v, want Active", got)
	}
	if !h.HandleInterrupt() {
		t.Fatal("HandleInterrupt returned false after PrepareForLaunch")
	}
	waitForState(t, events, monitor.StateIdle)
}

// An unrecognized screen must hold the last known state (no spurious flip).
func TestStart_UnknownHoldsLastState(t *testing.T) {
	fs := &fakeScreen{text: miniIdleScreen}
	_, events, cancel := startHarness(t, fs.get)
	defer cancel()

	waitForState(t, events, monitor.StateIdle)
	// Garbage screen with no input box: classifies unknown, state must remain
	// Idle (no event).
	fs.set("qwertyuiop zxcvbnm\n")
	select {
	case ev := <-events:
		t.Fatalf("unexpected state change on unknown screen: %v", eventState(t, ev))
	case <-time.After(600 * time.Millisecond):
		// good: held last state
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
