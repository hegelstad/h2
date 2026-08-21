package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

const (
	checksumFileName = ".h2-opencode-checksum"
	pluginRelPath    = "plugins/h2-bridge.ts"
	configRelPath    = "opencode.json"
	agentsRelPath    = "AGENTS.md"
)

type configTemplateData struct {
	Model string
}

func (h *OpencodeHarness) configDir() string {
	if h.rc == nil {
		return ""
	}
	return h.rc.HarnessConfigDir()
}

func (h *OpencodeHarness) dataDir() string {
	cfg := h.configDir()
	if cfg == "" {
		return ""
	}
	return filepath.Join(cfg, "data")
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
func (h *OpencodeHarness) EnsureConfigDir(h2Dir string) error {
	cfg := h.configDir()
	if cfg == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(cfg, "plugins"), 0o755); err != nil {
		return fmt.Errorf("opencode config dir: %w", err)
	}
	if err := os.MkdirAll(h.dataDir(), 0o755); err != nil {
		return fmt.Errorf("opencode data dir: %w", err)
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

func renderConfigJSON(model string) ([]byte, error) {
	tmpl, err := template.New("opencode.json").Parse(string(configJSONTemplate))
	if err != nil {
		return nil, fmt.Errorf("parse opencode.json template: %w", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, configTemplateData{Model: model}); err != nil {
		return nil, fmt.Errorf("render opencode.json: %w", err)
	}
	return []byte(b.String()), nil
}

func writeGuarded(root, rel string, content []byte) error {
	path := filepath.Join(root, rel)
	sumPath := path + checksumFileName
	want := sha256Hex(content)

	existing, err := os.ReadFile(path)
	if err == nil {
		stored, _ := os.ReadFile(sumPath)
		storedSum := strings.TrimSpace(string(stored))
		if storedSum == "" {
			// File exists without our checksum → treat as user-owned.
			return nil
		}
		if sha256Hex(existing) != storedSum {
			// User edited since we last wrote.
			return nil
		}
		if storedSum == want {
			return nil
		}
		// Template changed; we still own the file → rewrite.
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(rel)+".*")
	if err != nil {
		return fmt.Errorf("temp file for %s: %w", rel, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.WriteFile(sumPath, []byte(want+"\n"), 0o644)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
