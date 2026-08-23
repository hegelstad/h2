package crush

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"h2/internal/config"
)

// Default max output tokens for the large/small model slots.
//
// DO NOT raise these toward the model catalog maximum: OpenRouter rejects
// requests with HTTP 402 ("payment required ... You requested up to 64000
// tokens, but can only afford N") when the credit balance cannot cover the
// worst-case cost of max_tokens x output price. 4096 keeps turns affordable
// on a thin balance; role configs may override via the max_tokens_large /
// max_tokens_small override keys.
const (
	DefaultMaxTokensLarge = 4096
	DefaultMaxTokensSmall = 2048
)

// allowedTools mirrors the tool names Crush v0.91.0 auto-approves without a
// permission prompt. `--yolo` is not accepted by `crush run` in this version,
// so allow-listing in config is the only bypass path. The permissions-drift
// unit test asserts these names survive version bumps.
var allowedTools = []string{
	"bash",
	"edit",
	"write",
	"multiedit",
	"read",
	"view",
	"glob",
	"grep",
	"ls",
	"patch",
	"todowrite",
	"todoread",
	"webfetch",
}

// maxTokensLarge returns the role-configured large-slot output cap.
func maxTokensLarge(rc *config.RuntimeConfig) int {
	return overrideInt(rc, "max_tokens_large", DefaultMaxTokensLarge)
}

// maxTokensSmall returns the role-configured small-slot output cap.
func maxTokensSmall(rc *config.RuntimeConfig) int {
	return overrideInt(rc, "max_tokens_small", DefaultMaxTokensSmall)
}

func overrideInt(rc *config.RuntimeConfig, key string, def int) int {
	if rc == nil || rc.Overrides == nil {
		return def
	}
	if v, ok := rc.Overrides[key]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// renderCrushJSON builds the per-agent crush.json contents.
//
// Layout decisions (docs/plans/crush-harness.md §4):
//   - providers.openrouter is defined EXPLICITLY. This is load-bearing:
//     crush only honors a models.large/small `model` pin when the named
//     provider is also declared in a providers block that lists that model.
//     Without it, crush silently ignores the pin and falls back to the
//     OpenRouter catalog's default_large_model_id (anthropic/claude-sonnet,
//     which is PAID) — so every "Ox" turn quietly billed Sonnet. The
//     api_key is written as the literal "$OPENROUTER_API_KEY" env-ref;
//     crush expands it at runtime, so the secret never lands on disk.
//   - models.large/small pin max_tokens (the OpenRouter 402 guard above).
//   - options.data_directory is set as defense-in-depth so even a stray
//     manual `crush` invocation under this agent's XDG env lands its
//     cwd-local .crush db in the agent-scoped data dir; the supervisor also
//     passes --data-dir explicitly on every command.
//   - options.global_context_paths points at the agent's CRUSH.md, which
//     carries the role prompt (crush run has no system-prompt flag).
//   - disable_provider_auto_update pins behavior across version bumps.
func renderCrushJSON(configDir, dataDir string, large, small int, model string) ([]byte, error) {
	crushDir := filepath.Join(configDir, "crush")
	cfg := map[string]any{
		"$schema": "https://charm.land/crush.json",
		"providers": map[string]any{
			"openrouter": map[string]any{
				"id":       "openrouter",
				"name":     "OpenRouter",
				"type":     "openai",
				"base_url": "https://openrouter.ai/api/v1",
				"api_key":  "$OPENROUTER_API_KEY",
				"models": []map[string]any{
					{
						"id":                 model,
						"name":               model,
						"context_window":     1000000,
						"default_max_tokens": large,
					},
				},
			},
		},
		"models": map[string]any{
			"large": map[string]any{
				"provider":   "openrouter",
				"model":      model,
				"max_tokens": large,
			},
			"small": map[string]any{
				"provider":   "openrouter",
				"model":      model,
				"max_tokens": small,
			},
		},
		"options": map[string]any{
			"data_directory":               dataDir,
			"global_context_paths":         []string{filepath.Join(crushDir, "CRUSH.md")},
			"disable_provider_auto_update": true,
		},
		"permissions": map[string]any{
			"allowed_tools": allowedTools,
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// defaultModelID is the Ox Alpha stealth model on OpenRouter — the pod
// default. The harness honors rc.Model (role agent_model) first: any
// OpenRouter model id works with no code change ("any model, one field").
const defaultModelID = "stealth/ox-alpha"

func modelFor(rc *config.RuntimeConfig) string {
	if rc != nil && rc.Model != "" {
		return rc.Model
	}
	return defaultModelID
}

// renderRoleMarkdown builds CRUSH.md from the role's system prompt and
// instructions. Crush prepends context files to the model input, which is how
// role identity reaches the model despite `crush run` having no
// system-prompt flag.
func renderRoleMarkdown(rc *config.RuntimeConfig) []byte {
	var b []byte
	add := func(s string) {
		if strings.TrimSpace(s) == "" {
			return
		}
		b = append(b, []byte(strings.TrimSpace(s)+"\n\n")...)
	}
	add(rc.SystemPrompt)
	add(rc.Instructions)
	if len(b) == 0 {
		b = []byte("You are an h2 pod agent.\n")
	}
	return b
}

// writeFileIfChanged writes content atomically only when it differs from what
// is on disk, so user-visible mtimes stay stable and relaunches are cheap.
func writeFileIfChanged(path string, content []byte) error {
	old, err := os.ReadFile(path)
	if err == nil && string(old) == string(content) {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}
