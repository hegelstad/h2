package crush

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"h2/internal/config"
)

func TestIdentity(t *testing.T) {
	h := New(&config.RuntimeConfig{}, nil)
	if h.Name() != "crush" {
		t.Errorf("Name = %q", h.Name())
	}
	if h.DisplayCommand() != "crush" {
		t.Errorf("DisplayCommand = %q", h.DisplayCommand())
	}
	if !h.SupportsResume() {
		t.Error("SupportsResume = false, want true (session replay via --session)")
	}
}

// The PTY child must be the supervisor shim, never `crush` itself: a crush
// run exiting after turn 1 would end the whole h2 session.
func TestCommand_IsSupervisorNotCrush(t *testing.T) {
	h := New(&config.RuntimeConfig{}, nil)
	cmd := h.Command()
	if cmd == "" || filepath.Base(cmd) == "crush" {
		t.Errorf("Command = %q, want the h2 self-exec shim path", cmd)
	}
	args := h.BuildCommandArgs(nil, nil)
	if len(args) == 0 || args[0] != SupervisorSubcommand {
		t.Errorf("BuildCommandArgs[0] = %v, want subcommand %q", args, SupervisorSubcommand)
	}
}

func TestBuildCommandArgs_Fresh(t *testing.T) {
	t.Setenv("H2_CRUSH_HOST", "")
	t.Setenv("H2_CRUSH_TURN_TIMEOUT", "")
	h := New(&config.RuntimeConfig{AgentName: "a"}, nil)
	got := h.BuildCommandArgs(nil, nil)
	joined := strings.Join(got, " ")
	for _, want := range []string{SupervisorSubcommand, "--data-dir"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %v missing %q", got, want)
		}
	}
	if strings.Contains(joined, "--resume-session") {
		t.Errorf("fresh launch should not resume: %v", got)
	}
}

func TestBuildCommandArgs_Resume(t *testing.T) {
	rc := &config.RuntimeConfig{
		AgentName:        "a",
		HarnessSessionID: "sess-123",
		ResumeSessionID:  "sess-123",
	}
	h := New(rc, nil)
	got := h.BuildCommandArgs(nil, nil)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--resume-session sess-123") {
		t.Errorf("resume args missing --resume-session sess-123: %v", got)
	}
}

func TestBuildCommandArgs_HostPin(t *testing.T) {
	t.Setenv("H2_CRUSH_HOST", "unix:///tmp/agent.sock")
	h := New(&config.RuntimeConfig{AgentName: "a"}, nil)
	got := h.BuildCommandArgs(nil, nil)
	if !strings.Contains(strings.Join(got, " "), "--host unix:///tmp/agent.sock") {
		t.Errorf("expected pinned --host in %v", got)
	}
}

func TestBuildCommandEnvVars(t *testing.T) {
	h2Dir := setupFakeHome(t)
	h := New(testRC(t, h2Dir), nil)
	env := h.BuildCommandEnvVars(h2Dir)
	wantData := filepath.Join(h2Dir, "crush-data", "test-crush-agent")
	if env["H2_CRUSH_DATA_DIR"] != wantData {
		t.Errorf("H2_CRUSH_DATA_DIR = %q, want %q", env["H2_CRUSH_DATA_DIR"], wantData)
	}
	if env["XDG_CONFIG_HOME"] != h.rc.HarnessConfigDir() {
		t.Errorf("XDG_CONFIG_HOME = %q, want %q", env["XDG_CONFIG_HOME"], h.rc.HarnessConfigDir())
	}
	if env["H2_ACTOR"] != "test-crush-agent" {
		t.Errorf("H2_ACTOR = %q", env["H2_ACTOR"])
	}
}

func TestDataDir_AgentScoped(t *testing.T) {
	h2Dir := t.TempDir()
	a := New(&config.RuntimeConfig{AgentName: "alpha"}, nil)
	b := New(&config.RuntimeConfig{AgentName: "beta"}, nil)
	if a.DataDir(h2Dir) == b.DataDir(h2Dir) {
		t.Errorf("two agents share data dir %q — same-workspace collision", a.DataDir(h2Dir))
	}
}

func TestPrepareForLaunch_DryRunSkipsVersionGate(t *testing.T) {
	t.Setenv("PATH", "/nonexistent-crush-path")
	h := New(&config.RuntimeConfig{}, nil)
	if _, err := h.PrepareForLaunch(true); err != nil {
		t.Errorf("dryRun PrepareForLaunch errored: %v", err)
	}
}

func TestPrepareForLaunch_VersionGate(t *testing.T) {
	binDir := t.TempDir()
	writeStub(t, filepath.Join(binDir, "crush"), "#!/bin/sh\necho \"crush version v0.91.0\"\n")
	t.Setenv("PATH", binDir)

	h := New(&config.RuntimeConfig{}, nil)
	if _, err := h.PrepareForLaunch(false); err != nil {
		t.Fatalf("v0.91.0 should pass gate: %v", err)
	}

	writeStub(t, filepath.Join(binDir, "crush"), "#!/bin/sh\necho \"crush version v0.20.1\"\n")
	if _, err := h.PrepareForLaunch(false); err == nil {
		t.Error("v0.20.1 should fail the version gate")
	}

	writeStub(t, filepath.Join(binDir, "crush"), "#!/bin/sh\nexit 127\n")
	if _, err := h.PrepareForLaunch(false); err == nil || !strings.Contains(err.Error(), "not usable") {
		t.Errorf("missing crush should error with install hint, got: %v", err)
	}
}

func writeStub(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}
