// Package grok implements the Harness for xAI's Grok Build CLI.
//
// Grok Build's flag surface is deliberately Claude-Code-compatible
// (--permission-mode shares the same mode names; --system-prompt-override and
// --rules mirror --system-prompt and --append-system-prompt), so config
// mapping follows the claude harness closely.
//
// Turn-done detection is PURE HOOK-BASED: grok fires Stop (reason=="end_turn"),
// StopCancelled, StopFailure, SessionStart, etc. via `h2 handle-hook`, exactly
// mirroring how the claude harness works. No output-silence / screen-content
// scraping is used.
package grok

import (
	"context"
	"encoding/json"

	"h2/internal/activitylog"
	"h2/internal/config"
	"h2/internal/session/agent/harness"
	"h2/internal/session/agent/monitor"
)

func init() {
	harness.Register(harness.HarnessSpec{
		Names: []string{"grok", "grok_build"},
		Factory: func(rc *config.RuntimeConfig, log *activitylog.Logger) harness.Harness {
			return New(rc, log)
		},
		DefaultCommand: "grok",
	})
}

// GrokHarness implements harness.Harness for the Grok Build CLI.
type GrokHarness struct {
	rc           *config.RuntimeConfig
	activityLog  *activitylog.Logger
	eventHandler *EventHandler

	// internalCh buffers events from hook callbacks.
	// Start() forwards these to the external events channel.
	internalCh chan monitor.AgentEvent
}

// New creates a GrokHarness.
func New(rc *config.RuntimeConfig, log *activitylog.Logger) *GrokHarness {
	if log == nil {
		log = activitylog.Nop()
	}
	ch := make(chan monitor.AgentEvent, 256)
	return &GrokHarness{
		rc:           rc,
		activityLog:  log,
		internalCh:   ch,
		eventHandler: NewEventHandler(ch, log),
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

// EnsureConfigDir creates the Grok config directory and writes the h2 hooks
// config so grok fires `h2 handle-hook` for lifecycle events.
func (h *GrokHarness) EnsureConfigDir(h2Dir string) error {
	configDir := h.rc.HarnessConfigDir()
	if configDir == "" {
		return nil
	}
	return config.EnsureGrokConfigDir(configDir)
}

// --- Launch ---

// PrepareForLaunch returns an empty LaunchConfig. Grok is pure hook-based;
// no output collector or OTEL server is needed.
func (h *GrokHarness) PrepareForLaunch(dryRun bool) (harness.LaunchConfig, error) {
	return harness.LaunchConfig{}, nil
}

// --- Runtime ---

// Start forwards internal events (emitted by hook callbacks) to the external
// channel. Blocks until ctx is cancelled.
func (h *GrokHarness) Start(ctx context.Context, events chan<- monitor.AgentEvent) error {
	for {
		select {
		case ev := <-h.internalCh:
			select {
			case events <- ev:
			case <-ctx.Done():
				return nil
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// HandleHookEvent delegates hook events to the EventHandler. Returns true when
// the event is recognized as a grok lifecycle hook.
func (h *GrokHarness) HandleHookEvent(eventName string, payload json.RawMessage) bool {
	return h.eventHandler.ProcessHookEvent(eventName, payload)
}

// HandleInterrupt emits an idle transition for a local Ctrl+C / interrupt
// (which may never surface as StopCancelled if it kills the CLI outright).
func (h *GrokHarness) HandleInterrupt() bool {
	if h.eventHandler != nil {
		return h.eventHandler.HandleInterrupt()
	}
	return false
}

// HandleOutput is a no-op for Grok (state is tracked via hooks, not output).
func (h *GrokHarness) HandleOutput() {}

// Stop is a no-op for Grok (no OTEL server or collector to clean up).
func (h *GrokHarness) Stop() {}
