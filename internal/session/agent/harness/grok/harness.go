// Package grok implements the Harness for xAI's Grok Build CLI.
//
// Grok Build's flag surface is deliberately Claude-Code-compatible
// (--permission-mode shares the same mode names; --system-prompt-override and
// --rules mirror --system-prompt and --append-system-prompt), so config
// mapping follows the claude harness closely. Telemetry uses output-based
// idle detection via ptycollector (like the generic harness) — Grok Build
// has no OTEL/hook integration wired up yet.
package grok

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"h2/internal/activitylog"
	"h2/internal/config"
	"h2/internal/session/agent/harness"
	"h2/internal/session/agent/monitor"
	"h2/internal/session/agent/shared/ptycollector"
)

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
}

// New creates a GrokHarness.
func New(rc *config.RuntimeConfig) *GrokHarness {
	return &GrokHarness{rc: rc}
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

// PrepareForLaunch creates the output collector and returns an empty
// LaunchConfig. The collector is created here (not in Start) so that
// HandleOutput() works immediately after the child process starts.
func (h *GrokHarness) PrepareForLaunch(dryRun bool) (harness.LaunchConfig, error) {
	h.collector = ptycollector.New(monitor.IdleThreshold)
	return harness.LaunchConfig{}, nil
}

// --- Runtime ---

// Start bridges the output collector's state updates to the events channel.
// Blocks until ctx is cancelled.
func (h *GrokHarness) Start(ctx context.Context, events chan<- monitor.AgentEvent) error {
	for {
		select {
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

// HandleHookEvent returns false — the grok harness has no hook integration.
func (h *GrokHarness) HandleHookEvent(eventName string, payload json.RawMessage) bool {
	return false
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
