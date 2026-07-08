package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// harnessSpecificField describes a role field that only takes effect under one harness.
// If the field is set while a different harness is active, the field is silently ignored
// by that harness — a config mistake worth warning about.
type harnessSpecificField struct {
	yamlName string // the YAML key, as the user wrote it
	owner    string // the harness this field applies to (a ValidHarnessTypes value)
	isSet    func(r *Role) bool
}

// harnessSpecificFields is the catalog of fields that belong to exactly one harness.
// Warnings() flags any of these that is set while a different harness is active.
var harnessSpecificFields = []harnessSpecificField{
	{"claude_code_config_path_prefix", "claude_code", func(r *Role) bool { return r.ClaudeCodeConfigPathPrefix != "" }},
	{"claude_permission_mode", "claude_code", func(r *Role) bool { return r.ClaudePermissionMode != "" }},
	{"hooks", "claude_code", func(r *Role) bool { return yamlNodeSet(r.Hooks) }},
	{"settings", "claude_code", func(r *Role) bool { return yamlNodeSet(r.Settings) }},
	{"codex_config_path_prefix", "codex", func(r *Role) bool { return r.CodexConfigPathPrefix != "" }},
	{"codex_sandbox_mode", "codex", func(r *Role) bool { return r.CodexSandboxMode != "" }},
	{"codex_ask_for_approval", "codex", func(r *Role) bool { return r.CodexAskForApproval != "" }},
}

// yamlNodeSet reports whether a passthrough yaml.Node field (hooks, settings) was present
// in the role. An absent field decodes to the zero Node (Kind == 0).
func yamlNodeSet(n yaml.Node) bool {
	return n.Kind != 0
}

// Warnings returns non-fatal advisories about a role's configuration. Unlike Validate,
// which rejects a role outright, these flag likely mistakes that don't prevent launch —
// currently, harness-specific fields set under the wrong agent_harness (e.g. a codex role
// that sets claude_permission_mode, which the codex harness ignores). Returns nil when the
// role is clean.
func (r *Role) Warnings() []string {
	active := r.GetHarnessType()
	var warnings []string
	for _, f := range harnessSpecificFields {
		if f.owner != active && f.isSet(r) {
			warnings = append(warnings, fmt.Sprintf(
				"role %q sets %q, which only applies to the %s harness, but this role uses agent_harness %q; the field will be ignored",
				r.RoleName, f.yamlName, f.owner, active,
			))
		}
	}
	return warnings
}
