package opencode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IsolationEnv pins opencode's config + XDG dirs under cfgDir so the child
// cannot read ~/.config/opencode, ~/.local/share/opencode, or the host cache/state.
// OPENCODE_CONFIG_DIR alone is not sufficient on opencode 1.18.21: Path.config
// still walks XDG_CONFIG_HOME, and auth.json lives under XDG_DATA_HOME.
func IsolationEnv(cfgDir string) map[string]string {
	if strings.TrimSpace(cfgDir) == "" {
		return nil
	}
	return map[string]string{
		"OPENCODE_CONFIG_DIR": cfgDir,
		"XDG_DATA_HOME":       filepath.Join(cfgDir, "data"),
		"XDG_CONFIG_HOME":     filepath.Join(cfgDir, "xdg-config"),
		"XDG_STATE_HOME":      filepath.Join(cfgDir, "xdg-state"),
		"XDG_CACHE_HOME":      filepath.Join(cfgDir, "xdg-cache"),
	}
}

func mkdirIsolation(cfgDir string, perm os.FileMode) error {
	env := IsolationEnv(cfgDir)
	if env == nil {
		return fmt.Errorf("opencode harness config dir is empty; refusing unisolated launch")
	}
	seen := map[string]struct{}{}
	for _, d := range env {
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		if err := os.MkdirAll(d, perm); err != nil {
			return fmt.Errorf("opencode isolation dir %s: %w", d, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(cfgDir, "plugins"), perm); err != nil {
		return fmt.Errorf("opencode plugins dir: %w", err)
	}
	return nil
}
