// Package grok implements the Harness for xAI's Grok Build CLI.
//
// Grok Build's flag surface is deliberately Claude-Code-compatible
// (--permission-mode shares the same mode names; --system-prompt-override and
// --rules mirror --system-prompt and --append-system-prompt), so config
// mapping follows the claude harness closely.
//
// Turn-completion is detected via Grok Build's lifecycle hooks (the same
// mechanism Claude Code exposes): h2 writes hook registrations into
// $GROK_HOME/hooks, the child CLI runs "h2 handle-hook" on Stop/StopFailure/
// StopCancelled/Notification(idle_prompt), and HandleHookEvent emits a
// non-lossy Idle transition — exactly the position the Claude agents run in.
// The ptycollector output-timer is retained as a fallback safety net so a
// missed hook still eventually settles to idle.
package grok

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"h2/internal/activitylog"
	"h2/internal/config"
	"h2/internal/session/agent/harness"
	"h2/internal/session/agent/monitor"
	"h2/internal/session/agent/shared/ptycollector"
)

// terminalEmitTimeout is how long emitTerminal blocks when the events channel
// is full before giving up (last-resort non-blocking attempt). Mirrors the
// claude harness so critical Idle transitions are never dropped.
const terminalEmitTimeout = 2 * time.Second

func init() {
	harness.Register(harness.HarnessSpec{
		Names: []string{"grok", "grok_build"},
		Factory: func(rc *config.RuntimeConfig, log *activitylog.Logger) harness.Harness {
			return New(rc)
		},
		DefaultCommand: "grok",
	})
}

// GrokHarness implements harness.Harness for the Grok Build CLI.
type GrokHarness struct {
	rc        *config.RuntimeConfig
	collector *ptycollector.Collector // created in PrepareForLaunch()

	// internalCh buffers events from hook handlers. Start() forwards these to
	// the external events channel, alongside the ptycollector's output-timer
	// state updates.
	internalCh chan monitor.AgentEvent
}

// New creates a GrokHarness.
func New(rc *config.RuntimeConfig) *GrokHarness {
	return &GrokHarness{
		rc:         rc,
		internalCh: make(chan monitor.AgentEvent, 256),
	}
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

// EnsureConfigDir creates the Grok config directory and writes h2's hook
// registrations to $GROK_HOME/hooks. Credentials are populated separately via
// 'h2 auth grok'; Grok Build initialises the rest of its config on first run.
func (h *GrokHarness) EnsureConfigDir(h2Dir string) error {
	configDir := h.rc.HarnessConfigDir()
	if configDir == "" {
		return nil
	}
	return config.EnsureGrokConfigDir(configDir)
}

// --- Launch ---

// PrepareForLaunch creates the output collector and returns an empty
// LaunchConfig. The collector is created here (not in Start) so that
// HandleOutput() works immediately after the child process starts.
func (h *GrokHarness) PrepareForLaunch(dryRun bool) (harness.LaunchConfig, error) {
	h.collector = ptycollector.New(monitor.IdleThreshold)
	return harness.LaunchConfig{}, nil
}

// --- Runtime ---

// Start bridges both the hook-driven internal events and the output collector's
// state updates to the external events channel. Blocks until ctx is cancelled.
func (h *GrokHarness) Start(ctx context.Context, events chan<- monitor.AgentEvent) error {
	for {
		select {
		case ev := <-h.internalCh:
			select {
			case events <- ev:
			case <-ctx.Done():
				return ctx.Err()
			}
		case su := <-h.collector.StateCh():
			select {
			case events <- monitor.AgentEvent{
				Type:      monitor.EventStateChange,
				Timestamp: time.Now(),
				Data:      monitor.StateChangeData(su),
			}:
			case <-ctx.Done():
				return ctx.Err()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// HandleHookEvent translates Grok Build lifecycle-hook events into AgentEvents.
// Grok's event values are snake_case ("stop", "user_prompt_submit", …), unlike
// Claude's PascalCase. Idle transitions use emitTerminal so a full channel can
// never permanently strand the agent in the active state.
func (h *GrokHarness) HandleHookEvent(eventName string, payload json.RawMessage) bool {
	// A subagent's turn end is not the session's idle: Grok tags those events
	// with subagentType. Acknowledge (known event) but do not change state.
	meta := parseGrokHookMeta(payload)
	if meta.SubagentType != "" {
		return true
	}

	now := time.Now()
	switch eventName {
	case "user_prompt_submit":
		h.emit(monitor.AgentEvent{Type: monitor.EventUserPrompt, Timestamp: now})
		h.emitStateChange(now, monitor.StateActive, monitor.SubStateThinking)

	case "stop", "stop_failure", "stop_cancelled":
		// Non-lossy: a dropped Idle would leave IsIdle false and withhold
		// messages forever (the TUI-drift wedge this change replaces).
		h.emitStateChangeTerminal(now, monitor.StateIdle, monitor.SubStateNone)

	case "notification":
		// Registered with matcher idle_prompt, so this is the idle backstop.
		// Guard on notificationType too in case a broader Notification fires.
		if meta.NotificationType != "" && meta.NotificationType != "idle_prompt" {
			return true
		}
		h.emitStateChangeTerminal(now, monitor.StateIdle, monitor.SubStateNone)

	case "session_end":
		h.emitTerminal(monitor.AgentEvent{Type: monitor.EventSessionEnded, Timestamp: now})

	default:
		return false
	}
	return true
}

// grokHookMeta holds the Grok hook envelope fields h2 keys on.
type grokHookMeta struct {
	SubagentType     string `json:"subagentType"`
	NotificationType string `json:"notificationType"`
}

func parseGrokHookMeta(payload json.RawMessage) grokHookMeta {
	var m grokHookMeta
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &m)
	}
	return m
}

func (h *GrokHarness) emit(ev monitor.AgentEvent) {
	select {
	case h.internalCh <- ev:
	default:
	}
}

func (h *GrokHarness) emitStateChange(ts time.Time, state monitor.State, subState monitor.SubState) {
	h.emit(monitor.AgentEvent{
		Type:      monitor.EventStateChange,
		Timestamp: ts,
		Data:      monitor.StateChangeData{State: state, SubState: subState},
	})
}

func (h *GrokHarness) emitStateChangeTerminal(ts time.Time, state monitor.State, subState monitor.SubState) {
	h.emitTerminal(monitor.AgentEvent{
		Type:      monitor.EventStateChange,
		Timestamp: ts,
		Data:      monitor.StateChangeData{State: state, SubState: subState},
	})
}

// emitTerminal tries non-blocking first, then blocks up to terminalEmitTimeout
// so critical Idle/Exited transitions are not dropped when the channel is full.
func (h *GrokHarness) emitTerminal(ev monitor.AgentEvent) {
	select {
	case h.internalCh <- ev:
		return
	default:
	}
	timer := time.NewTimer(terminalEmitTimeout)
	defer timer.Stop()
	select {
	case h.internalCh <- ev:
	case <-timer.C:
		select {
		case h.internalCh <- ev:
		default:
			log.Printf("h2: dropped terminal grok agent event type=%v after %s (channel full)", ev.Type, terminalEmitTimeout)
		}
	}
}

// HandleInterrupt forces an immediate idle state update for local Ctrl+C.
func (h *GrokHarness) HandleInterrupt() bool {
	if h.collector != nil {
		h.collector.SignalInterrupt()
		return true
	}
	return false
}

// HandleOutput feeds the output collector to detect activity/idle transitions.
func (h *GrokHarness) HandleOutput() {
	if h.collector != nil {
		h.collector.SignalOutput()
	}
}

// Stop cleans up the output collector.
func (h *GrokHarness) Stop() {
	if h.collector != nil {
		h.collector.Stop()
	}
}
