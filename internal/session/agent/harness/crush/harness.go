// Package crush implements the Harness for the Charmbracelet Crush CLI.
//
// Architecture (docs/plans/crush-harness.md): h2 owns exactly one long-lived
// PTY child per session (session.lifecycleLoop pauses delivery when it exits),
// but a Crush turn is one-shot (`crush run` exits when the turn completes).
// The PTY child is therefore a turn-supervisor shim — h2 itself, re-executed
// with the hidden "crush-supervisor" subcommand — which spawns one
// short-lived `crush run` per delivered message and reports turn lifecycle
// via the existing hook plumbing (h2 handle-hook -> listener ->
// Session.HandleHookEvent -> harness.HandleHookEvent).
package crush

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"h2/internal/activitylog"
	"h2/internal/config"
	"h2/internal/session/agent/harness"
	"h2/internal/session/agent/monitor"
)

func init() {
	harness.Register(harness.HarnessSpec{
		Names: []string{"crush"},
		Factory: func(rc *config.RuntimeConfig, log *activitylog.Logger) harness.Harness {
			return New(rc, log)
		},
		DefaultCommand: "crush",
	})
}

// MinimumCrushVersion is the pinned floor for the crush binary. Upstream docs
// already diverge from this version's CLI, so the gate is mandatory.
const MinimumCrushVersion = "0.91.0"

// SupervisorSubcommand is the hidden h2 subcommand that runs as the PTY
// child of a Crush harness session.
const SupervisorSubcommand = "crush-supervisor"

// CrushHarness implements harness.Harness for Crush.
type CrushHarness struct {
	rc          *config.RuntimeConfig
	activityLog *activitylog.Logger

	events *eventHandler

	// shimBin overrides the supervisor executable (test escape hatch; empty
	// means re-exec the running h2 binary via os.Executable()).
	shimBin string
}

// New creates a CrushHarness.
func New(rc *config.RuntimeConfig, log *activitylog.Logger) *CrushHarness {
	if log == nil {
		log = activitylog.Nop()
	}
	return &CrushHarness{
		rc:          rc,
		activityLog: log,
		events:      newEventHandler(),
	}
}

// --- Identity ---

func (h *CrushHarness) Name() string { return "crush" }

// Command returns the PTY child executable: h2 itself (the supervisor is a
// hidden subcommand), unless overridden for tests.
func (h *CrushHarness) Command() string {
	if bin := os.Getenv("H2_CRUSH_SHIM"); bin != "" {
		return bin
	}
	if h.shimBin != "" {
		return h.shimBin
	}
	self, err := os.Executable()
	if err != nil {
		return "h2"
	}
	return self
}

func (h *CrushHarness) DisplayCommand() string { return "crush" }

// --- Resume ---

func (h *CrushHarness) SupportsResume() bool { return true }

// --- Config ---

// DataDir returns the per-agent crush data directory (holds crush.db and
// sessions). It MUST be agent-scoped: the default <project>/.crush db is
// cwd-local and collides when several agents share a workspace.
func (h *CrushHarness) DataDir(h2Dir string) string {
	return filepath.Join(h2Dir, "crush-data", h.agentName())
}

// BuildCommandArgs assembles the supervisor invocation. With ResumeSessionID
// set (relaunch/resume), the supervisor replays --session on every turn;
// otherwise it captures the first turn's session id and persists it to
// --session-file (REQUIRED in prod: without it the capture is never
// persisted, HarnessSessionID stays empty, and every relaunch starts a
// fresh crush session).
func (h *CrushHarness) BuildCommandArgs(prependArgs, extraArgs []string) []string {
	var roleArgs []string
	dataDir := h.DataDir(config.ConfigDir())
	roleArgs = append(roleArgs, SupervisorSubcommand)
	roleArgs = append(roleArgs, "--data-dir", dataDir)
	roleArgs = append(roleArgs, "--session-file", filepath.Join(dataDir, "session-id"))
	if m := modelFor(h.rc); m != "" {
		// v0.91.0 run-mode ignores configured models without this; see
		// Supervisor.Model.
		roleArgs = append(roleArgs, "--model", m)
	}
	if host := os.Getenv("H2_CRUSH_HOST"); host != "" {
		// Pin an explicit socket so a stray shared-server can never silently
		// collect our runs via the box-wide default socket.
		roleArgs = append(roleArgs, "--host", host)
	}
	if rc := h.rc.ResumeSessionID; rc != "" {
		roleArgs = append(roleArgs, "--resume-session", rc)
	}
	if t := turnTimeoutFromEnv(); t > 0 {
		roleArgs = append(roleArgs, "--turn-timeout", t.String())
	}
	return harness.CombineArgs(prependArgs, extraArgs, roleArgs)
}

// agentConfigDir returns THIS agent's crush config root:
// <HarnessConfigPathPrefix>/<profile>/<agentName>. Per-agent (not
// profile-level) because the generated crush.json embeds this agent's
// data_directory — a shared file would let same-profile agents overwrite
// each other's defense-in-depth data dir on every EnsureConfigDir.
func (h *CrushHarness) agentConfigDir() string {
	return filepath.Join(h.rc.HarnessConfigDir(), h.agentName())
}

func (h *CrushHarness) agentName() string {
	if name := h.rc.AgentName; name != "" {
		return name
	}
	return "unnamed"
}

// BuildCommandEnvVars returns the env vars scoping the supervisor (and its
// crush children) to this agent's config/data dirs.
func (h *CrushHarness) BuildCommandEnvVars(h2Dir string) map[string]string {
	configDir := h.agentConfigDir()
	if h.rc.HarnessConfigDir() == "" {
		return nil
	}
	env := map[string]string{
		"H2_ACTOR":          h.rc.AgentName,
		"H2_CRUSH_DATA_DIR": h.DataDir(h2Dir),
		"XDG_CONFIG_HOME":   configDir,
		"XDG_DATA_HOME":     filepath.Join(configDir, "xdg-data"),
	}
	return env
}

// EnsureConfigDir writes the per-agent crush.json and CRUSH.md role file.
// Idempotent: files are only rewritten when content changes.
func (h *CrushHarness) EnsureConfigDir(h2Dir string) error {
	if h.rc.HarnessConfigDir() == "" {
		return fmt.Errorf("crush harness requires a config path prefix")
	}
	configDir := h.agentConfigDir()
	crushDir := filepath.Join(configDir, "crush")
	if err := os.MkdirAll(crushDir, 0o755); err != nil {
		return fmt.Errorf("create crush config dir: %w", err)
	}
	dataDir := h.DataDir(h2Dir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create crush data dir: %w", err)
	}
	cfg, err := renderCrushJSON(configDir, dataDir,
		maxTokensLarge(h.rc), maxTokensSmall(h.rc), modelFor(h.rc))
	if err != nil {
		return fmt.Errorf("render crush.json: %w", err)
	}
	if err := writeFileIfChanged(filepath.Join(crushDir, "crush.json"), cfg); err != nil {
		return err
	}
	role := renderRoleMarkdown(h.rc)
	if err := writeFileIfChanged(filepath.Join(crushDir, "CRUSH.md"), role); err != nil {
		return err
	}
	return nil
}

// --- Launch ---

// PrepareForLaunch gates on the installed crush version and returns the env
// vars merged into the PTY child environment.
func (h *CrushHarness) PrepareForLaunch(dryRun bool) (harness.LaunchConfig, error) {
	env := h.BuildCommandEnvVars(config.ConfigDir())
	if dryRun {
		return harness.LaunchConfig{Env: env}, nil
	}
	if err := checkCrushVersion(MinimumCrushVersion); err != nil {
		return harness.LaunchConfig{}, err
	}
	return harness.LaunchConfig{Env: env}, nil
}

// checkCrushVersion verifies an installed crush >= min ("X.Y.Z").
func checkCrushVersion(min string) error {
	out, err := exec.Command("crush", "--version").Output()
	if err != nil {
		return fmt.Errorf("crush binary not usable (install v%s, see https://github.com/charmbracelet/crush/releases): %w", min, err)
	}
	got, ok := parseCrushVersion(string(out))
	if !ok {
		return fmt.Errorf("cannot parse crush version output %q (expected v%s or newer)", strings.TrimSpace(string(out)), min)
	}
	if versionLess(got, min) {
		return fmt.Errorf("crush v%s is too old (have v%s); install v%s or newer", got, got, min)
	}
	return nil
}

// parseCrushVersion extracts "X.Y.Z" from output like "crush version v0.91.0".
func parseCrushVersion(out string) (string, bool) {
	for _, f := range strings.Fields(out) {
		f = strings.TrimPrefix(f, "v")
		parts := strings.SplitN(f, ".", 3)
		if len(parts) == 3 {
			if _, err := strconv.Atoi(parts[0]); err == nil {
				return f, true
			}
		}
	}
	return "", false
}

// versionLess compares dotted numeric version strings.
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, y := 0, 0
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// turnTimeoutFromEnv reads H2_CRUSH_TURN_TIMEOUT (Go duration syntax).
func turnTimeoutFromEnv() time.Duration {
	if v := os.Getenv("H2_CRUSH_TURN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 0
}

// --- Runtime ---

// Start forwards internal events to the monitor until ctx is cancelled.
func (h *CrushHarness) Start(ctx context.Context, events chan<- monitor.AgentEvent) error {
	for {
		select {
		case ev := <-h.events.ch:
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

// HandleHookEvent processes crush.turn.* hook envelopes emitted by the
// supervisor shim. Returns true if the event was handled.
func (h *CrushHarness) HandleHookEvent(eventName string, payload json.RawMessage) bool {
	return h.events.handleHook(eventName, payload)
}

// HandleInterrupt signals the supervisor (via the PTY 0x03 path) that the
// in-flight turn should be killed. State transitions arrive through hooks;
// nothing to emit here.
func (h *CrushHarness) HandleInterrupt() bool { return false }

// HandleOutput is a no-op: state comes exclusively from hooks.
func (h *CrushHarness) HandleOutput() {}

// Stop is a no-op: Start exits on ctx cancel.
func (h *CrushHarness) Stop() {}
