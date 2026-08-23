// Package grok implements the Harness for xAI's Grok Build CLI.
//
// Grok Build's flag surface is deliberately Claude-Code-compatible
// (--permission-mode shares the same mode names; --system-prompt-override and
// --rules mirror --system-prompt and --append-system-prompt), so config
// mapping follows the claude harness closely.
//
// Idle detection is SCREEN-CONTENT based (see classify.go), not output-silence
// based. Grok Build has no OTEL/hook integration, and — unlike Claude/Codex —
// its TUI repaints continuously (animated spinner + elapsed clock), so it never
// goes output-silent. A ptycollector-style silence detector therefore never
// reports idle for grok, which strands inter-agent message delivery (delivery
// only hands out normal-priority messages while idle). Instead the harness
// reads the rendered screen (via harness.ScreenReader, wired by the session)
// and classifies idle vs active from the TUI's own state indicators.
package grok

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"h2/internal/activitylog"
	"h2/internal/config"
	"h2/internal/session/agent/harness"
	"h2/internal/session/agent/monitor"
)

const (
	// pollInterval is how often the screen is sampled and classified.
	pollInterval = 150 * time.Millisecond
	// idleConfirmPolls is how many consecutive idle classifications are
	// required before declaring idle. This debounces the brief window at the
	// end of a turn (and any mid-repaint frame) so idle is never declared
	// prematurely. At pollInterval this is ~450ms of stable idle.
	idleConfirmPolls = 3
	// unknownStartupGrace is how long after Start an unknown (unrecognized)
	// screen is tolerated silently before it is logged — grok's TUI paints
	// within a second or two, so a persistent unknown past this means the TUI
	// changed or crashed and should be visible.
	unknownStartupGrace = 5 * time.Second
	// unknownLogInterval rate-limits the unknown-screen warning.
	unknownLogInterval = 60 * time.Second
	// forceIdleSuppression is how long after a HandleInterrupt-forced idle the
	// loop refuses to flip back to Active off a still-painted turn marker. The
	// interrupt takes a beat to tear down grok's turn UI, so without this the
	// very next poll could re-read the stale spinner and immediately undo the
	// forced idle. A few poll intervals is enough.
	forceIdleSuppression = 500 * time.Millisecond
)

func init() {
	harness.Register(harness.HarnessSpec{
		Names: []string{"grok", "grok_build"},
		Factory: func(rc *config.RuntimeConfig, _ *activitylog.Logger) harness.Harness {
			return New(rc)
		},
		DefaultCommand: "grok",
	})
}

// GrokHarness implements harness.Harness for the Grok Build CLI.
type GrokHarness struct {
	rc *config.RuntimeConfig

	mu           sync.Mutex
	screenSource func() string // rendered-screen accessor, set via SetScreenSource
	forceIdleCh  chan struct{} // HandleInterrupt -> immediate idle; created in PrepareForLaunch
}

// New creates a GrokHarness.
func New(rc *config.RuntimeConfig) *GrokHarness {
	return &GrokHarness{rc: rc}
}

// SetScreenSource implements harness.ScreenReader. The session provides the
// live rendered-screen accessor before Start. Safe to call concurrently with
// the Start loop.
func (h *GrokHarness) SetScreenSource(fn func() string) {
	h.mu.Lock()
	h.screenSource = fn
	h.mu.Unlock()
}

func (h *GrokHarness) screen() string {
	h.mu.Lock()
	fn := h.screenSource
	h.mu.Unlock()
	if fn == nil {
		return ""
	}
	return fn()
}

// --- Identity ---

func (h *GrokHarness) Name() string           { return "grok" }
func (h *GrokHarness) Command() string        { return "grok" }
func (h *GrokHarness) DisplayCommand() string { return "grok" }

// --- Resume ---

func (h *GrokHarness) SupportsResume() bool { return true }

// --- Config (called before launch) ---

// BuildCommandArgs maps RuntimeConfig to Grok Build CLI flags, combined with
// prependArgs and extraArgs into the complete child process argument list.
// When ResumeSessionID is set, only --resume is emitted — Grok Build restores
// the session's prompt, rules, and permission mode from its own session state.
func (h *GrokHarness) BuildCommandArgs(prependArgs, extraArgs []string) []string {
	var roleArgs []string
	rc := h.rc
	if rc.ResumeSessionID != "" {
		roleArgs = append(roleArgs, "--resume", rc.ResumeSessionID)
		return harness.CombineArgs(prependArgs, extraArgs, roleArgs)
	}
	if rc.SessionID != "" {
		roleArgs = append(roleArgs, "--session-id", rc.SessionID)
	}
	if rc.SystemPrompt != "" {
		roleArgs = append(roleArgs, "--system-prompt-override", rc.SystemPrompt)
	}
	if rc.Instructions != "" {
		roleArgs = append(roleArgs, "--rules", rc.Instructions)
	}
	if rc.Model != "" {
		roleArgs = append(roleArgs, "--model", rc.Model)
	}
	if rc.GrokPermissionMode != "" {
		roleArgs = append(roleArgs, "--permission-mode", rc.GrokPermissionMode)
	}
	// Grok Build has no --add-dir equivalent; AdditionalDirs is not mapped.
	return harness.CombineArgs(prependArgs, extraArgs, roleArgs)
}

// BuildCommandEnvVars returns env vars for Grok Build (GROK_HOME selects the
// config/credentials directory, analogous to CLAUDE_CONFIG_DIR).
func (h *GrokHarness) BuildCommandEnvVars(h2Dir string) map[string]string {
	configDir := h.rc.HarnessConfigDir()
	if configDir != "" {
		return map[string]string{
			"GROK_HOME": configDir,
		}
	}
	return nil
}

// EnsureConfigDir creates the Grok config directory. Unlike Claude, no
// default settings file is written — Grok Build initialises its own config
// on first run, and credentials are populated via 'h2 auth grok'.
func (h *GrokHarness) EnsureConfigDir(h2Dir string) error {
	configDir := h.rc.HarnessConfigDir()
	if configDir == "" {
		return nil
	}
	return os.MkdirAll(configDir, 0o755)
}

// --- Launch ---

// PrepareForLaunch creates the interrupt channel and returns an empty
// LaunchConfig. The channel is created here (not in Start) so a Ctrl+C arriving
// between launch and Start is buffered and applied once the loop begins.
func (h *GrokHarness) PrepareForLaunch(dryRun bool) (harness.LaunchConfig, error) {
	h.mu.Lock()
	h.forceIdleCh = make(chan struct{}, 1)
	h.mu.Unlock()
	return harness.LaunchConfig{}, nil
}

// --- Runtime ---

// Start samples the rendered screen on a ticker, classifies idle vs active from
// the TUI content (see classify.go), and emits state transitions to the monitor.
// It emits an initial Active (the child is launching / not yet at its prompt),
// flips to Active immediately when a turn indicator appears, and flips to Idle
// only after idleConfirmPolls consecutive idle classifications (debounce). An
// unrecognized screen holds the last known state and is logged (rate-limited)
// so a future Grok Build TUI change is visible rather than silently wedging
// delivery. Blocks until ctx is cancelled.
//
// INVARIANT — state changes are emitted TRANSITION-ONLY (each emit is guarded by
// last != want), never once per poll tick. This is load-bearing for the
// idle-staleness watchdog backstop: AgentMonitor resets lastActivityAt on every
// event it processes, and maybeReconcileIdle only force-recovers a stuck Active
// after lastActivityAt has been stale for DefaultIdleStaleTimeout (~2m). If this
// loop re-emitted Active every pollInterval the staleness timer would never
// mature and the watchdog could never rescue a wedged classifier. Debounce is
// about WHEN a transition is confirmed, not about emitting every tick. If you
// refactor this loop and TestStart_EmitsOnlyOnTransition fails, do not relax it
// — restore the transition guards.
func (h *GrokHarness) Start(ctx context.Context, events chan<- monitor.AgentEvent) error {
	h.mu.Lock()
	forceIdle := h.forceIdleCh
	src := h.screenSource
	h.mu.Unlock()
	if src == nil {
		log.Printf("h2: grok harness started without a screen source; idle detection disabled until SetScreenSource is called")
	}

	// last is the most recently emitted monitor.State. Seed with Active and
	// emit it so delivery is withheld during the launch/startup window.
	last := monitor.StateActive
	emit := func(s monitor.State) bool {
		select {
		case events <- monitor.AgentEvent{
			Type:      monitor.EventStateChange,
			Timestamp: time.Now(),
			Data:      monitor.StateChangeData(monitor.StateUpdate{State: s, SubState: monitor.SubStateNone}),
		}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !emit(monitor.StateActive) {
		return ctx.Err()
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	start := time.Now()
	idleStreak := 0
	var lastUnknownLog time.Time
	// suppressActiveUntil holds off Active re-classification briefly after a
	// forced idle so a still-painted turn marker can't immediately undo it.
	var suppressActiveUntil time.Time

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-forceIdle:
			idleStreak = 0
			suppressActiveUntil = time.Now().Add(forceIdleSuppression)
			if last != monitor.StateIdle {
				if !emit(monitor.StateIdle) {
					return ctx.Err()
				}
				last = monitor.StateIdle
			}
		case <-ticker.C:
			switch classifyScreen(h.screen()) {
			case stateActive:
				if time.Now().Before(suppressActiveUntil) {
					// Within the post-interrupt window: ignore a lingering turn
					// marker so the forced idle sticks.
					continue
				}
				idleStreak = 0
				if last != monitor.StateActive {
					if !emit(monitor.StateActive) {
						return ctx.Err()
					}
					last = monitor.StateActive
				}
			case stateIdle:
				idleStreak++
				if idleStreak >= idleConfirmPolls && last != monitor.StateIdle {
					if !emit(monitor.StateIdle) {
						return ctx.Err()
					}
					last = monitor.StateIdle
				}
			case stateUnknown:
				// Hold last state; surface a persistent unknown so a TUI change
				// (or crash) is diagnosable instead of silently stranding delivery.
				now := time.Now()
				if now.Sub(start) > unknownStartupGrace && now.Sub(lastUnknownLog) > unknownLogInterval {
					log.Printf("h2: grok screen unmatched by known TUI markers (holding state=%v); Grok Build TUI may have changed — see harness/grok/classify.go", last)
					lastUnknownLog = now
				}
			}
		}
	}
}

// HandleHookEvent returns false — the grok harness has no hook integration.
func (h *GrokHarness) HandleHookEvent(eventName string, payload json.RawMessage) bool {
	return false
}

// HandleInterrupt forces an immediate idle state update for local Ctrl+C.
// Non-blocking: if an interrupt is already pending it coalesces.
func (h *GrokHarness) HandleInterrupt() bool {
	h.mu.Lock()
	ch := h.forceIdleCh
	h.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- struct{}{}:
	default:
	}
	return true
}

// HandleOutput is a no-op for grok: state is derived by polling the rendered
// screen, not from output signals. It exists to satisfy the Harness interface.
func (h *GrokHarness) HandleOutput() {}

// Stop is a no-op: the Start loop exits on context cancellation.
func (h *GrokHarness) Stop() {}
