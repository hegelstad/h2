package crush

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"h2/internal/config"
)

// setupFakeHome isolates tests from the real filesystem by setting HOME,
// H2_ROOT_DIR, and H2_DIR to temp directories and resetting the resolve cache.
func setupFakeHome(t *testing.T) string {
	t.Helper()
	fakeHome := t.TempDir()
	fakeRootDir := filepath.Join(fakeHome, ".h2")
	if err := os.MkdirAll(fakeRootDir, 0o755); err != nil {
		t.Fatalf("create fake h2 dir: %v", err)
	}
	if err := config.WriteMarker(fakeRootDir); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Setenv("HOME", fakeHome)
	t.Setenv("H2_ROOT_DIR", fakeRootDir)
	t.Setenv("H2_DIR", fakeRootDir)
	config.ResetResolveCache()
	t.Cleanup(config.ResetResolveCache)
	return fakeHome
}

func testRC(t *testing.T, h2Dir string) *config.RuntimeConfig {
	t.Helper()
	return &config.RuntimeConfig{
		AgentName:               "test-crush-agent",
		HarnessType:             "crush",
		HarnessConfigPathPrefix: filepath.Join(h2Dir, "crush-config"),
		Profile:                 "default",
	}
}

func TestEnsureConfigDir_WritesCrushJSONAndRoleFile(t *testing.T) {
	h2Dir := setupFakeHome(t)
	h := New(testRC(t, h2Dir), nil)
	h.rc.SystemPrompt = "You are coder-ox."
	h.rc.Instructions = "Reply to concierge via h2 send."

	if err := h.EnsureConfigDir(h2Dir); err != nil {
		t.Fatalf("EnsureConfigDir: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(h.rc.HarnessConfigDir(), "crush", "crush.json"))
	if err != nil {
		t.Fatalf("read crush.json: %v", err)
	}
	var cfg struct {
		Models struct {
			Large struct {
				Model     string `json:"model"`
				Provider  string `json:"provider"`
				MaxTokens int    `json:"max_tokens"`
			} `json:"large"`
			Small struct {
				MaxTokens int `json:"max_tokens"`
			} `json:"small"`
		} `json:"models"`
		Options struct {
			DataDirectory         string   `json:"data_directory"`
			GlobalContextPaths    []string `json:"global_context_paths"`
			DisableProviderUpdate bool     `json:"disable_provider_auto_update"`
		} `json:"options"`
		Permissions struct {
			AllowedTools []string `json:"allowed_tools"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse crush.json: %v\n%s", err, raw)
	}

	wantDataDir := filepath.Join(h2Dir, "crush-data", "test-crush-agent")
	if cfg.Models.Large.Model != modelID {
		t.Errorf("large.model = %q, want %q", cfg.Models.Large.Model, modelID)
	}
	if cfg.Models.Large.Provider != "openrouter" {
		t.Errorf("large.provider = %q, want openrouter", cfg.Models.Large.Provider)
	}
	if cfg.Models.Large.MaxTokens != DefaultMaxTokensLarge {
		t.Errorf("large.max_tokens = %d, want %d (402 guard)", cfg.Models.Large.MaxTokens, DefaultMaxTokensLarge)
	}
	if cfg.Models.Small.MaxTokens != DefaultMaxTokensSmall {
		t.Errorf("small.max_tokens = %d, want %d (402 guard)", cfg.Models.Small.MaxTokens, DefaultMaxTokensSmall)
	}
	if cfg.Options.DataDirectory != wantDataDir {
		t.Errorf("options.data_directory = %q, want agent-scoped %q", cfg.Options.DataDirectory, wantDataDir)
	}
	if len(cfg.Options.GlobalContextPaths) != 1 ||
		cfg.Options.GlobalContextPaths[0] != filepath.Join(h.rc.HarnessConfigDir(), "crush", "CRUSH.md") {
		t.Errorf("global_context_paths = %v, want single absolute CRUSH.md path", cfg.Options.GlobalContextPaths)
	}
	if !cfg.Options.DisableProviderUpdate {
		t.Error("disable_provider_auto_update = false, want true")
	}
	foundBash := false
	for _, tool := range cfg.Permissions.AllowedTools {
		if tool == "bash" {
			foundBash = true
		}
	}
	if !foundBash {
		t.Errorf("allowed_tools missing \"bash\": %v", cfg.Permissions.AllowedTools)
	}

	roleRaw, err := os.ReadFile(filepath.Join(h.rc.HarnessConfigDir(), "crush", "CRUSH.md"))
	if err != nil {
		t.Fatalf("read CRUSH.md: %v", err)
	}
	if !strings.Contains(string(roleRaw), "You are coder-ox.") || !strings.Contains(string(roleRaw), "h2 send") {
		t.Errorf("CRUSH.md missing role prompt content:\n%s", roleRaw)
	}

	// Data dir must exist too (sessions and crush.db land there).
	info, err := os.Stat(wantDataDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("data dir %s missing: %v", wantDataDir, err)
	}
}

func TestEnsureConfigDir_Idempotent(t *testing.T) {
	h2Dir := setupFakeHome(t)
	h := New(testRC(t, h2Dir), nil)
	if err := h.EnsureConfigDir(h2Dir); err != nil {
		t.Fatalf("first EnsureConfigDir: %v", err)
	}
	cfgPath := filepath.Join(h.rc.HarnessConfigDir(), "crush", "crush.json")
	first, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.EnsureConfigDir(h2Dir); err != nil {
		t.Fatalf("second EnsureConfigDir: %v", err)
	}
	second, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("crush.json changed between idempotent EnsureConfigDir calls")
	}
}

func TestEnsureConfigDir_RoleOverridesMaxTokens(t *testing.T) {
	h2Dir := setupFakeHome(t)
	rc := testRC(t, h2Dir)
	rc.Overrides = map[string]string{
		"max_tokens_large": "8192",
		"max_tokens_small": "1024",
	}
	h := New(rc, nil)
	if err := h.EnsureConfigDir(h2Dir); err != nil {
		t.Fatalf("EnsureConfigDir: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(h.rc.HarnessConfigDir(), "crush", "crush.json"))
	var cfg struct {
		Models struct {
			Large struct {
				MaxTokens int `json:"max_tokens"`
			} `json:"large"`
			Small struct {
				MaxTokens int `json:"max_tokens"`
			} `json:"small"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Models.Large.MaxTokens != 8192 || cfg.Models.Small.MaxTokens != 1024 {
		t.Errorf("overrides not applied: large=%d small=%d, want 8192/1024",
			cfg.Models.Large.MaxTokens, cfg.Models.Small.MaxTokens)
	}
}

func TestEnsureConfigDir_RequiresConfigPrefix(t *testing.T) {
	setupFakeHome(t)
	h := New(&config.RuntimeConfig{AgentName: "x", HarnessType: "crush"}, nil)
	if err := h.EnsureConfigDir(t.TempDir()); err == nil {
		t.Fatal("expected error for empty HarnessConfigPathPrefix")
	}
}

// TestAllowedTools_NoSilentDrift pins the exact allow-list against Crush
// v0.91.0 tool names. Upstream renames tools silently; a version bump that
// changes names must fail here so the permission config gets revisited.
func TestAllowedTools_NoSilentDrift(t *testing.T) {
	want := []string{
		"bash", "edit", "write", "multiedit", "read", "view",
		"glob", "grep", "ls", "patch", "todowrite", "todoread", "webfetch",
	}
	if len(allowedTools) != len(want) {
		t.Fatalf("allowed_tools count = %d, want %d: %v", len(allowedTools), len(want), allowedTools)
	}
	for i, name := range want {
		if allowedTools[i] != name {
			t.Errorf("allowed_tools[%d] = %q, want %q", i, allowedTools[i], name)
		}
	}
}
