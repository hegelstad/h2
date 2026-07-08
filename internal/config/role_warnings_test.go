package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// yamlNode builds a non-empty yaml.Node from a YAML fragment for hooks/settings tests.
func yamlNode(t *testing.T, src string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(src), &n); err != nil {
		t.Fatalf("build yaml node: %v", err)
	}
	// Unwrap the document node to the mapping content, matching how role hooks/settings
	// are stored (the field itself is the mapping, not the document wrapper).
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		return *n.Content[0]
	}
	return n
}

// wantWarning asserts exactly one warning is produced and that it names the offending
// field, the active harness, and the harness the field belongs to.
func wantWarning(t *testing.T, warnings []string, field, activeHarness, ownerHarness string) {
	t.Helper()
	if len(warnings) != 1 {
		t.Fatalf("Warnings() = %d warnings %v, want exactly 1", len(warnings), warnings)
	}
	w := warnings[0]
	for _, sub := range []string{field, activeHarness, ownerHarness} {
		if !strings.Contains(w, sub) {
			t.Errorf("warning %q missing %q", w, sub)
		}
	}
}

func TestWarnings_CodexHarnessWithClaudePermissionMode(t *testing.T) {
	r := &Role{RoleName: "r", AgentHarness: "codex", ClaudePermissionMode: "plan"}
	wantWarning(t, r.Warnings(), "claude_permission_mode", "codex", "claude_code")
}

func TestWarnings_CodexHarnessWithClaudeConfigPrefix(t *testing.T) {
	r := &Role{RoleName: "r", AgentHarness: "codex", ClaudeCodeConfigPathPrefix: "/tmp/cc"}
	wantWarning(t, r.Warnings(), "claude_code_config_path_prefix", "codex", "claude_code")
}

func TestWarnings_CodexHarnessWithHooks(t *testing.T) {
	r := &Role{RoleName: "r", AgentHarness: "codex", Hooks: yamlNode(t, "PreToolUse: []")}
	wantWarning(t, r.Warnings(), "hooks", "codex", "claude_code")
}

func TestWarnings_CodexHarnessWithSettings(t *testing.T) {
	r := &Role{RoleName: "r", AgentHarness: "codex", Settings: yamlNode(t, "model: opus")}
	wantWarning(t, r.Warnings(), "settings", "codex", "claude_code")
}

func TestWarnings_CodexHarnessWithSystemPrompt(t *testing.T) {
	r := &Role{RoleName: "r", AgentHarness: "codex", SystemPrompt: "you are a helpful agent"}
	wantWarning(t, r.Warnings(), "system_prompt", "codex", "claude_code")
}

func TestWarnings_ClaudeHarnessWithCodexSandboxMode(t *testing.T) {
	r := &Role{RoleName: "r", AgentHarness: "claude_code", CodexSandboxMode: "danger-full-access"}
	wantWarning(t, r.Warnings(), "codex_sandbox_mode", "claude_code", "codex")
}

func TestWarnings_ClaudeHarnessWithCodexAskForApproval(t *testing.T) {
	r := &Role{RoleName: "r", AgentHarness: "claude_code", CodexAskForApproval: "on-request"}
	wantWarning(t, r.Warnings(), "codex_ask_for_approval", "claude_code", "codex")
}

func TestWarnings_ClaudeHarnessWithCodexConfigPrefix(t *testing.T) {
	r := &Role{RoleName: "r", AgentHarness: "claude_code", CodexConfigPathPrefix: "/tmp/cx"}
	wantWarning(t, r.Warnings(), "codex_config_path_prefix", "claude_code", "codex")
}

// An empty agent_harness defaults to claude_code, so codex fields on it are still misuse —
// this catches the common "forgot to set agent_harness: codex" mistake.
func TestWarnings_DefaultHarnessWithCodexField(t *testing.T) {
	r := &Role{RoleName: "r", CodexSandboxMode: "read-only"}
	wantWarning(t, r.Warnings(), "codex_sandbox_mode", "claude_code", "codex")
}

func TestWarnings_MatchingClaudeFieldsNoWarning(t *testing.T) {
	r := &Role{
		RoleName:                   "r",
		AgentHarness:               "claude_code",
		ClaudePermissionMode:       "plan",
		ClaudeCodeConfigPathPrefix: "/tmp/cc",
		SystemPrompt:               "you are a helpful agent",
		Hooks:                      yamlNode(t, "PreToolUse: []"),
		Settings:                   yamlNode(t, "model: opus"),
	}
	if w := r.Warnings(); len(w) != 0 {
		t.Fatalf("Warnings() = %v, want none for matching claude fields", w)
	}
}

func TestWarnings_MatchingCodexFieldsNoWarning(t *testing.T) {
	r := &Role{
		RoleName:              "r",
		AgentHarness:          "codex",
		CodexSandboxMode:      "danger-full-access",
		CodexAskForApproval:   "on-request",
		CodexConfigPathPrefix: "/tmp/cx",
	}
	if w := r.Warnings(); len(w) != 0 {
		t.Fatalf("Warnings() = %v, want none for matching codex fields", w)
	}
}

// generic uses agent_harness_command; both claude- and codex-specific tuning are ignored,
// so misusing either set is a mistake worth flagging.
func TestWarnings_GenericHarnessWarnsOnBothSides(t *testing.T) {
	r := &Role{
		RoleName:             "r",
		AgentHarness:         "generic",
		ClaudePermissionMode: "plan",
		CodexSandboxMode:     "read-only",
	}
	w := r.Warnings()
	if len(w) != 2 {
		t.Fatalf("Warnings() = %d %v, want 2 (one per foreign field)", len(w), w)
	}
	joined := strings.Join(w, "\n")
	for _, sub := range []string{"claude_permission_mode", "codex_sandbox_mode", "generic"} {
		if !strings.Contains(joined, sub) {
			t.Errorf("warnings %q missing %q", joined, sub)
		}
	}
}

func TestWarnings_MultipleForeignFieldsEachWarned(t *testing.T) {
	r := &Role{
		RoleName:             "r",
		AgentHarness:         "codex",
		ClaudePermissionMode: "plan",
		Hooks:                yamlNode(t, "PreToolUse: []"),
	}
	if w := r.Warnings(); len(w) != 2 {
		t.Fatalf("Warnings() = %d %v, want 2", len(w), w)
	}
}

func TestWarnings_CleanRoleNoWarnings(t *testing.T) {
	r := &Role{RoleName: "r", AgentHarness: "codex", CodexSandboxMode: "read-only"}
	if w := r.Warnings(); len(w) != 0 {
		t.Fatalf("Warnings() = %v, want none", w)
	}
}
