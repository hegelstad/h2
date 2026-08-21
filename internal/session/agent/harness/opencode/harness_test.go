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

// hostHome is captured before any test mutates HOME. assertIsolated must
// compare against the real machine paths, not t.TempDir().
var hostHome string

func TestMain(m *testing.M) {
	hostHome, _ = os.UserHomeDir()
	os.Exit(m.Run())
}

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
	if hostHome == "" {
		t.Fatal("host home unset; TestMain did not capture UserHomeDir")
	}
	forbidden := []string{
		filepath.Join(hostHome, ".config", "opencode"),
		filepath.Join(hostHome, ".local", "share", "opencode"),
		filepath.Join(hostHome, ".local", "share"),
		filepath.Join(hostHome, ".local", "state", "opencode"),
		filepath.Join(hostHome, ".cache", "opencode"),
	}
	for _, p := range forbidden {
		if path == p || strings.HasPrefix(path, p+string(os.PathSeparator)) {
			t.Fatalf("path leaked to host %s: %s", p, path)
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
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		v := env[k]
		if v == "" {
			t.Errorf("%s unset", k)
			continue
		}
		assertIsolated(t, v)
		if !strings.HasPrefix(v, cfg) {
			t.Errorf("%s %q not under cfg %q", k, v, cfg)
		}
	}
	if env["XDG_DATA_HOME"] == filepath.Join(hostHome, ".local", "share") {
		t.Fatal("XDG_DATA_HOME leaked to host ~/.local/share")
	}
	if env["H2_BIN"] == "" {
		t.Error("H2_BIN unset")
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

func TestBuildCommandEnvVars_ReadsStashedKeyFile(t *testing.T) {
	rc := isolatedRC(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	h := New(rc, nil)
	if err := os.MkdirAll(h.configDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.configDir(), "openrouter.key"), []byte("sk-stashed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := h.BuildCommandEnvVars("")
	if env["OPENROUTER_API_KEY"] != "sk-stashed" {
		t.Errorf("OPENROUTER_API_KEY = %q, want sk-stashed", env["OPENROUTER_API_KEY"])
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
	if !strings.Contains(string(plugin), "handle-hook") {
		t.Error("plugin missing handle-hook")
	}
	if strings.Contains(string(plugin), "message.updated") {
		t.Error("plugin must not hook message.updated")
	}
	if !strings.Contains(string(plugin), "opencode.permission.asked") {
		t.Error("plugin must emit opencode.permission.asked")
	}
	if !strings.Contains(string(plugin), "H2_BIN") {
		t.Error("plugin must honor H2_BIN")
	}
	for _, d := range []string{"xdg-config", "xdg-state", "xdg-cache", "data"} {
		info, err := os.Stat(filepath.Join(cfg, d))
		if err != nil || !info.IsDir() {
			t.Errorf("isolation dir %s: %v", d, err)
		}
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

func TestEnsureConfigDir_EmptyPrefixFailsClosed(t *testing.T) {
	h := New(&config.RuntimeConfig{HarnessType: "opencode", AgentName: "x"}, nil)
	if err := h.EnsureConfigDir(""); err == nil {
		t.Fatal("expected error for empty config dir")
	}
	env := h.BuildCommandEnvVars("")
	if env["OPENCODE_CONFIG_DIR"] != "" || env["XDG_DATA_HOME"] != "" || env["XDG_CONFIG_HOME"] != "" {
		t.Fatalf("isolation env set without prefix: %#v", env)
	}
}

func TestEnsureConfigDir_WritesAgentsMD(t *testing.T) {
	rc := isolatedRC(t)
	rc.SystemPrompt = "sys-line"
	rc.Instructions = "be a coder"
	h := New(rc, nil)
	if err := h.EnsureConfigDir(""); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(h.configDir(), "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "sys-line") || !strings.Contains(string(body), "be a coder") {
		t.Errorf("AGENTS.md = %q", body)
	}
}

func TestWriteGuarded_UpgradesOwnedFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeGuarded(dir, "f.txt", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := writeGuarded(dir, "f.txt", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v2" {
		t.Errorf("upgrade = %q, want v2", body)
	}
}

func TestWriteGuarded_MissingChecksumRewrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("incomplete"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeGuarded(dir, "f.txt", []byte("complete")); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "complete" {
		t.Errorf("got %q, want rewrite of incomplete write", body)
	}
}

func TestRenderConfigJSON_EscapesModel(t *testing.T) {
	body, err := renderConfigJSON(`openrouter/x"y`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `openrouter/x\"y`) {
		t.Errorf("model not JSON-escaped: %s", body)
	}
	if strings.Contains(string(body), `"stealth/ox-alpha"`) && !strings.Contains(string(body), `x\"y`) {
		t.Error("still special-cased ox-alpha for a different model")
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
