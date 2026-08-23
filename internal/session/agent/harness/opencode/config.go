package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	checksumFileName = ".h2-opencode-checksum"
	pluginRelPath    = "plugins/h2-bridge.ts"
	configRelPath    = "opencode.json"
	agentsRelPath    = "AGENTS.md"
)

func (h *OpencodeHarness) configDir() string {
	if h.rc == nil {
		return ""
	}
	return h.rc.HarnessConfigDir()
}

func (h *OpencodeHarness) resolvedModel() string {
	if h.rc != nil && h.rc.Model != "" {
		return h.rc.Model
	}
	return DefaultModel
}

// EnsureConfigDir writes opencode.json + plugins/h2-bridge.ts under the
// harness config dir. Idempotent and checksum-guarded: a file the user
// edited (content no longer matches the last h2-written checksum) is left
// alone. Official template updates rewrite files we still own.
//
// An empty config dir fails closed — launching unisolated would leak into
// ~/.config/opencode and ~/.local/share/opencode.
func (h *OpencodeHarness) EnsureConfigDir(h2Dir string) error {
	cfg := h.configDir()
	if err := mkdirIsolation(cfg, 0o700); err != nil {
		return err
	}

	jsonBody, err := renderConfigJSON(h.resolvedModel())
	if err != nil {
		return err
	}
	if err := writeGuarded(cfg, configRelPath, jsonBody); err != nil {
		return err
	}
	if err := writeGuarded(cfg, pluginRelPath, pluginSource); err != nil {
		return err
	}
	sys := ""
	if h.rc != nil {
		sys = strings.TrimSpace(h.rc.SystemPrompt)
		if inst := strings.TrimSpace(h.rc.Instructions); inst != "" {
			if sys != "" {
				sys += "\n\n"
			}
			sys += inst
		}
	}
	if sys != "" {
		if err := writeGuarded(cfg, agentsRelPath, []byte(sys+"\n")); err != nil {
			return err
		}
	}
	_ = h2Dir
	return nil
}

// permissionConfig returns opencode's permission settings for managed h2 agents.
// Every tool is "allow" because these agents run non-interactively: an "ask"
// has no user to answer it and blocks the turn forever. external_directory must
// be allowed so agents can reach their h2 message queue and the h2 binary, which
// live outside the project working directory.
func permissionConfig() map[string]any {
	return map[string]any{
		"edit":               "allow",
		"bash":               "allow",
		"webfetch":           "allow",
		"external_directory": "allow",
	}
}

func renderConfigJSON(model string) ([]byte, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultModel
	}
	key := strings.TrimPrefix(model, "openrouter/")
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"model":   model,
		// Route auxiliary tasks (session-title/summary generation) to the same
		// model. opencode otherwise defaults these to a paid model, which errors
		// on a free OpenRouter account ("requires more credits") and spams logs.
		"small_model": model,
		// Managed h2 agents run headless on a PTY with no interactive user, so any
		// permission "ask" blocks the turn forever. Grant everything. Critically,
		// h2 agents constantly touch paths outside their project dir (their message
		// queue under $H2_DIR, the h2 binary), which opencode gates behind the
		// "external_directory" permission — without allowing it the agent hangs the
		// first time it reads its own inbox or runs `h2 send`.
		"permission": permissionConfig(),
		"provider": map[string]any{
			"openrouter": map[string]any{
				"options": map[string]any{
					"apiKey": "{env:OPENROUTER_API_KEY}",
				},
				"models": map[string]any{
					key: map[string]any{
						"limit": map[string]any{
							"context": 200000,
							"output":  65536,
						},
					},
				},
			},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render opencode.json: %w", err)
	}
	return append(b, '\n'), nil
}

func writeGuarded(root, rel string, content []byte) error {
	path := filepath.Join(root, rel)
	sumPath := path + checksumFileName
	want := sha256Hex(content)

	existing, err := os.ReadFile(path)
	if err == nil {
		stored, _ := os.ReadFile(sumPath)
		storedSum := strings.TrimSpace(string(stored))
		if storedSum != "" {
			if sha256Hex(existing) != storedSum {
				// User edited since we last wrote.
				return nil
			}
			if storedSum == want {
				return nil
			}
			// Template changed; we still own the file → rewrite.
		}
		// No checksum: treat as an incomplete prior write, not user-owned.
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(path, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	if err := writeFileAtomic(sumPath, []byte(want+"\n"), 0o644); err != nil {
		return fmt.Errorf("write checksum for %s: %w", rel, err)
	}
	return nil
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
