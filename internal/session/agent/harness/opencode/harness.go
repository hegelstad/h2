// Package opencode implements the Harness for the SST opencode TUI.
// Turn-done is driven by the native session.idle plugin event (hook-driven,
// like Claude Code) — this package does not scrape the TUI.
package opencode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"h2/internal/activitylog"
	"h2/internal/config"
	"h2/internal/session/agent/harness"
	"h2/internal/session/agent/monitor"
)

// DefaultModel is the Ox Alpha OpenRouter id. Roles may override via model:.
const DefaultModel = "openrouter/stealth/ox-alpha"

func init() {
	harness.Register(harness.HarnessSpec{
		Names: []string{"opencode", "opencode_ai"},
		Factory: func(rc *config.RuntimeConfig, log *activitylog.Logger) harness.Harness {
			return New(rc, log)
		},
		DefaultCommand: "opencode",
	})
}

type stateChange struct {
	state monitor.State
	sub   monitor.SubState
}

// OpencodeHarness implements harness.Harness.
type OpencodeHarness struct {
	rc          *config.RuntimeConfig
	activityLog *activitylog.Logger

	stateCh     chan stateChange
	forceIdleCh chan struct{}
}

// New creates an OpencodeHarness.
func New(rc *config.RuntimeConfig, log *activitylog.Logger) *OpencodeHarness {
	if log == nil {
		log = activitylog.Nop()
	}
	return &OpencodeHarness{
		rc:          rc,
		activityLog: log,
		stateCh:     make(chan stateChange, 32),
		forceIdleCh: make(chan struct{}, 1),
	}
}

func (h *OpencodeHarness) Name() string           { return "opencode" }
func (h *OpencodeHarness) Command() string        { return "opencode" }
func (h *OpencodeHarness) DisplayCommand() string { return "opencode" }
func (h *OpencodeHarness) SupportsResume() bool   { return true }

// BuildCommandArgs maps RuntimeConfig to opencode CLI flags.
func (h *OpencodeHarness) BuildCommandArgs(prependArgs, extraArgs []string) []string {
	var roleArgs []string
	rc := h.rc
	if rc.ResumeSessionID != "" {
		roleArgs = append(roleArgs, "-s", rc.ResumeSessionID)
		return harness.CombineArgs(prependArgs, extraArgs, roleArgs)
	}
	if rc.SessionID != "" {
		roleArgs = append(roleArgs, "-s", rc.SessionID)
	}
	if model := h.resolvedModel(); model != "" {
		roleArgs = append(roleArgs, "-m", model)
	}
	return harness.CombineArgs(prependArgs, extraArgs, roleArgs)
}

// BuildCommandEnvVars isolates opencode config + data under the harness dir
// and sets the managed-agent flags from plan §10.5.
func (h *OpencodeHarness) BuildCommandEnvVars(h2Dir string) map[string]string {
	_ = h2Dir
	cfg := h.configDir()
	env := map[string]string{
		"OPENCODE_PERMISSION":         "bypass",
		"OPENCODE_DISABLE_AUTOUPDATE": "1",
		"OPENCODE_DISABLE_SHARE":      "1",
		"OPENCODE_AUTO_SHARE":         "0",
	}
	if h.rc != nil && h.rc.AgentName != "" {
		env["H2_AGENT_NAME"] = h.rc.AgentName
	}
	if cfg != "" {
		env["OPENCODE_CONFIG_DIR"] = cfg
		env["XDG_DATA_HOME"] = h.dataDir()
	}
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		env["OPENROUTER_API_KEY"] = key
	} else if cfg != "" {
		if b, err := os.ReadFile(filepath.Join(cfg, "openrouter.key")); err == nil {
			if k := strings.TrimSpace(string(b)); k != "" {
				env["OPENROUTER_API_KEY"] = k
			}
		}
	}
	return env
}

// PrepareForLaunch creates the force-idle channel (already allocated in New).
func (h *OpencodeHarness) PrepareForLaunch(dryRun bool) (harness.LaunchConfig, error) {
	_ = dryRun
	if h.forceIdleCh == nil {
		h.forceIdleCh = make(chan struct{}, 1)
	}
	if h.stateCh == nil {
		h.stateCh = make(chan stateChange, 32)
	}
	return harness.LaunchConfig{}, nil
}

// Start seeds Active, then forwards hook/interrupt state transitions only
// (last != want) so the idle watchdog stays load-bearing.
func (h *OpencodeHarness) Start(ctx context.Context, events chan<- monitor.AgentEvent) error {
	last := monitor.StateInitialized
	emit := func(state monitor.State, sub monitor.SubState) {
		if last == state {
			return
		}
		last = state
		ev := monitor.AgentEvent{
			Type:      monitor.EventStateChange,
			Timestamp: time.Now(),
			Data:      monitor.StateChangeData{State: state, SubState: sub},
		}
		select {
		case events <- ev:
		case <-ctx.Done():
		}
	}
	emit(monitor.StateActive, monitor.SubStateThinking)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-h.forceIdleCh:
			emit(monitor.StateIdle, monitor.SubStateNone)
		case sc := <-h.stateCh:
			emit(sc.state, sc.sub)
		}
	}
}

// HandleInterrupt forces Idle (Ctrl+C), like grok/claude.
func (h *OpencodeHarness) HandleInterrupt() bool {
	if h.forceIdleCh == nil {
		return false
	}
	select {
	case h.forceIdleCh <- struct{}{}:
	default:
	}
	return true
}

// HandleOutput is a no-op: state comes from hooks, not the TUI.
func (h *OpencodeHarness) HandleOutput() {}

// Stop is a no-op; Start exits on ctx cancel.
func (h *OpencodeHarness) Stop() {}
