package opencode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"h2/internal/config"
	"h2/internal/session/agent/harness"
)

var _ harness.Harness = (*OpencodeHarness)(nil)

func isolatedRC(t *testing.T) *config.RuntimeConfig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("H2_DIR", filepath.Join(home, ".h2"))
	prefix := filepath.Join(home, "opencode-config")
	return &config.RuntimeConfig{
		AgentName:               "test-oc",
		HarnessType:             "opencode",
		Command:                 "opencode",
		HarnessConfigPathPrefix: prefix,
		Profile:                 "default",
		CWD:                     filepath.Join(home, "work"),
		Model:                   DefaultModel,
		SessionID:               "sess-1",
		StartedAt:               "2026-08-22T00:00:00Z",
	}
}

func assertIsolated(t *testing.T, path string) {
	t.Helper()
	if path == "" {
		t.Fatal("empty path")
	}
	realHome, err := os.UserHomeDir()
	if err == nil && realHome != "" {
		realCfg := filepath.Join(realHome, ".config", "opencode")
		if path == realCfg || strings.HasPrefix(path, realCfg+string(os.PathSeparator)) {
			t.Fatalf("path leaked to real opencode config: %s", path)
		}
	}
}

func TestNameCommand(t *testing.T) {
	h := New(isolatedRC(t), nil)
	if h.Name() != "opencode" {
		t.Errorf("Name() = %q", h.Name())
	}
	if h.Command() != "opencode" || h.DisplayCommand() != "opencode" {
		t.Errorf("Command = %q / %q", h.Command(), h.DisplayCommand())
	}
	if !h.SupportsResume() {
		t.Error("SupportsResume() = false")
	}
}

func TestBuildCommandArgs_FreshModelAndSession(t *testing.T) {
	rc := isolatedRC(t)
	h := New(rc, nil)
	args := h.BuildCommandArgs([]string{"--prepend"}, []string{"--extra"})
	want := []string{"--prepend", "--extra", "-s", "sess-1", "-m", DefaultModel}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", args, want)
	}
}

func TestBuildCommandArgs_Resume(t *testing.T) {
	rc := isolatedRC(t)
	rc.ResumeSessionID = "old-sess"
	h := New(rc, nil)
	args := h.BuildCommandArgs(nil, nil)
	if len(args) != 2 || args[0] != "-s" || args[1] != "old-sess" {
		t.Errorf("resume args = %v", args)
	}
}

func TestBuildCommandEnvVars_IsolatesConfigAndData(t *testing.T) {
	rc := isolatedRC(t)
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	h := New(rc, nil)
	env := h.BuildCommandEnvVars("")
	cfg := env["OPENCODE_CONFIG_DIR"]
	data := env["XDG_DATA_HOME"]
	if cfg == "" || data == "" {
		t.Fatalf("missing isolation env: %#v", env)
	}
	assertIsolated(t, cfg)
	assertIsolated(t, data)
	if !strings.HasPrefix(cfg, rc.HarnessConfigPathPrefix) {
		t.Errorf("OPENCODE_CONFIG_DIR %q not under prefix %q", cfg, rc.HarnessConfigPathPrefix)
	}
	if data != filepath.Join(cfg, "data") {
		t.Errorf("XDG_DATA_HOME = %q, want %s/data", data, cfg)
	}
	if env["OPENCODE_PERMISSION"] != "bypass" {
		t.Errorf("OPENCODE_PERMISSION = %q", env["OPENCODE_PERMISSION"])
	}
	if env["OPENCODE_DISABLE_AUTOUPDATE"] != "1" || env["OPENCODE_DISABLE_SHARE"] != "1" || env["OPENCODE_AUTO_SHARE"] != "0" {
		t.Errorf("managed-agent flags: %#v", env)
	}
	if env["H2_AGENT_NAME"] != "test-oc" {
		t.Errorf("H2_AGENT_NAME = %q", env["H2_AGENT_NAME"])
	}
	if env["OPENROUTER_API_KEY"] != "sk-test" {
		t.Errorf("OPENROUTER_API_KEY = %q", env["OPENROUTER_API_KEY"])
	}
}

func TestEnsureConfigDir_WritesPluginAndJSON(t *testing.T) {
	rc := isolatedRC(t)
	h := New(rc, nil)
	if err := h.EnsureConfigDir(""); err != nil {
		t.Fatal(err)
	}
	cfg := h.configDir()
	jsonPath := filepath.Join(cfg, "opencode.json")
	pluginPath := filepath.Join(cfg, "plugins", "h2-bridge.ts")
	body, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), DefaultModel) {
		t.Errorf("opencode.json missing model: %s", body)
	}
	if !strings.Contains(string(body), "65536") {
		t.Error("opencode.json missing generous output token limit")
	}
	plugin, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plugin), "session.idle") {
		t.Error("plugin missing session.idle")
	}
	if !strings.Contains(string(plugin), "h2 handle-hook") {
		t.Error("plugin missing h2 handle-hook")
	}

	// Idempotent: second write does not fail and leaves content.
	if err := h.EnsureConfigDir(""); err != nil {
		t.Fatal(err)
	}
	body2, _ := os.ReadFile(jsonPath)
	if string(body) != string(body2) {
		t.Error("second EnsureConfigDir changed owned file unexpectedly")
	}
}

func TestEnsureConfigDir_DoesNotClobberUserEdits(t *testing.T) {
	rc := isolatedRC(t)
	h := New(rc, nil)
	if err := h.EnsureConfigDir(""); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(h.configDir(), "opencode.json")
	if err := os.WriteFile(jsonPath, []byte(`{"model":"user/override"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.EnsureConfigDir(""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(jsonPath)
	if string(got) != `{"model":"user/override"}` {
		t.Errorf("user edit was clobbered: %s", got)
	}
}

func TestNoScreenReader(t *testing.T) {
	var h any = New(isolatedRC(t), nil)
	if _, ok := h.(interface{ ReadScreen() string }); ok {
		t.Fatal("opencode harness must not implement a screen reader")
	}
	if _, ok := h.(interface{ ReadScreen() []byte }); ok {
		t.Fatal("opencode harness must not implement a screen reader")
	}
}
