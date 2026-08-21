package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"h2/internal/config"
)

func TestValidateHarnessConfigDirExists_MissingProfileDerivedDir(t *testing.T) {
	h2Dir := setupProfileTestH2Dir(t)
	role := &config.Role{
		AgentHarness: "codex",
		Profile:      "alt",
	}

	err := validateHarnessConfigDirExists(role, buildRoleRuntimeConfig(role))
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
	err := validateHarnessConfigDirExists(role, buildRoleRuntimeConfig(role))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateHarnessConfigDirExists_OpencodeMissingDirOK(t *testing.T) {
	h2Dir := setupProfileTestH2Dir(t)
	role := &config.Role{
		AgentHarness: "opencode",
		Profile:      "default",
	}
	rc := buildRoleRuntimeConfig(role)
	if !strings.Contains(rc.HarnessConfigPathPrefix, "opencode-config") {
		t.Fatalf("prefix = %q", rc.HarnessConfigPathPrefix)
	}
	missing := filepath.Join(h2Dir, "opencode-config", "default")
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s should not exist, stat err=%v", missing, err)
	}
	if err := validateHarnessConfigDirExists(role, rc); err != nil {
		t.Fatalf("opencode must not require a pre-existing config dir: %v", err)
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
