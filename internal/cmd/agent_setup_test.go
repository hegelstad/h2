package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"h2/internal/config"
	"h2/internal/session"
)

func TestValidateHarnessConfigDirExists_MissingProfileDerivedDir(t *testing.T) {
	h2Dir := setupProfileTestH2Dir(t)
	role := &config.Role{
		AgentHarness: "codex",
		Profile:      "alt",
	}

	err := validateHarnessConfigDirExists(role, buildRoleRuntimeConfig("test-agent", role))
	if err == nil {
		t.Fatal("expected error for missing profile-derived config dir")
	}
	if !strings.Contains(err.Error(), `profile "alt" not found`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(h2Dir, "codex-config", "alt")) {
		t.Fatalf("error missing expected config dir path: %v", err)
	}
}

func TestValidateHarnessConfigDirExists_ExistingProfileDerivedDir(t *testing.T) {
	h2Dir := setupProfileTestH2Dir(t)
	role := &config.Role{
		AgentHarness: "codex",
		Profile:      "alt1",
	}

	if err := os.MkdirAll(filepath.Join(h2Dir, "codex-config", "alt1"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := validateHarnessConfigDirExists(role, buildRoleRuntimeConfig("test-agent", role))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBuildRoleRuntimeConfig_PopulatesAgentName is the unit-level regression
// for the crush config-write bug: the crush harness derives its per-agent
// config directory from rc.AgentName, so buildRoleRuntimeConfig MUST carry the
// resolved name. An empty name silently routed crush.json to a stray
// "unnamed" dir the launched agent never reads.
func TestBuildRoleRuntimeConfig_PopulatesAgentName(t *testing.T) {
	role := &config.Role{AgentHarness: "crush"}
	rc := buildRoleRuntimeConfig("coder-ox", role)
	if rc.AgentName != "coder-ox" {
		t.Fatalf("AgentName = %q, want %q", rc.AgentName, "coder-ox")
	}
	if rc.HarnessType != "crush" {
		t.Fatalf("HarnessType = %q, want crush", rc.HarnessType)
	}
	if rc.HarnessConfigPathPrefix == "" {
		t.Fatal("HarnessConfigPathPrefix must be populated for crush")
	}
}

// TestDoSetupAndForkAgent_CrushWritesConfigToAgentDir is the end-to-end
// regression: driving the real setup path for a crush role must write
// crush.json + CRUSH.md under <crush-config>/<profile>/<agent>/crush (the
// directory the launched agent reads via XDG_CONFIG_HOME), pinned to the
// role's model. Before the fix, EnsureConfigDir ran against a nameless minRC
// and wrote to .../unnamed/crush instead, so the agent booted on crush's paid
// default model with no role identity.
func TestDoSetupAndForkAgent_CrushWritesConfigToAgentDir(t *testing.T) {
	h2Dir := setupProfileTestH2Dir(t)
	// Crush uses its own config prefix; the default profile dir must exist
	// (h2 never auto-creates profiles on run).
	crushProfileDir := filepath.Join(h2Dir, "crush-config", "default")
	if err := os.MkdirAll(crushProfileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()

	role := &config.Role{
		RoleName:     "crush-coder",
		AgentHarness: "crush",
		Profile:      "default",
		WorkingDir:   workDir,
		SystemPrompt: "You are coder-ox.",
		Instructions: "Reply to concierge via h2 send.",
	}

	origFork := forkDaemonFunc
	forkDaemonFunc = func(sd string, hints session.TerminalHints, resume bool) error {
		return nil
	}
	defer func() { forkDaemonFunc = origFork }()

	if err := doSetupAndForkAgent("cfgtest", role, true, "", 0, nil, true); err != nil {
		t.Fatalf("doSetupAndForkAgent: %v", err)
	}

	crushDir := filepath.Join(h2Dir, "crush-config", "default", "cfgtest", "crush")
	raw, err := os.ReadFile(filepath.Join(crushDir, "crush.json"))
	if err != nil {
		t.Fatalf("crush.json not written to agent dir %s: %v", crushDir, err)
	}

	// The stray "unnamed" dir must NOT exist — that was the bug.
	if _, err := os.Stat(filepath.Join(h2Dir, "crush-config", "default", "unnamed")); !os.IsNotExist(err) {
		t.Errorf("stray 'unnamed' crush config dir exists (err=%v); config was routed to the wrong agent dir", err)
	}

	var cfg struct {
		Models struct {
			Large struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"large"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse crush.json: %v", err)
	}
	if cfg.Models.Large.Provider != "openrouter" {
		t.Errorf("large.provider = %q, want openrouter", cfg.Models.Large.Provider)
	}
	if cfg.Models.Large.Model != "stealth/ox-alpha" {
		t.Errorf("large.model = %q, want stealth/ox-alpha (free default)", cfg.Models.Large.Model)
	}

	role_md, err := os.ReadFile(filepath.Join(crushDir, "CRUSH.md"))
	if err != nil {
		t.Fatalf("CRUSH.md not written: %v", err)
	}
	if !strings.Contains(string(role_md), "coder-ox") {
		t.Errorf("CRUSH.md missing role identity, got: %q", role_md)
	}
}

func TestDoSetupAndForkAgent_FailsWhenProfileMissing(t *testing.T) {
	setupProfileTestH2Dir(t)
	role := &config.Role{
		RoleName:             "reviewer-extra-1-sand",
		AgentHarness:         "codex",
		Profile:              "alt",
		CodexAskForApproval:  "never",
		CodexSandboxMode:     "workspace-write",
		WorktreeEnabled:      false,
		ClaudePermissionMode: "",
	}

	err := doSetupAndForkAgent("missing-profile-test", role, true, "", 0, nil, true)
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
	if !strings.Contains(err.Error(), `profile "alt" not found`) {
		t.Fatalf("unexpected error: %v", err)
	}
}
