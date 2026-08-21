package opencode

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyIsolationEnv_ReplacesExistingXDG(t *testing.T) {
	cfg := t.TempDir()
	in := []string{
		"PATH=/usr/bin",
		"XDG_DATA_HOME=/tmp/leaky",
		"XDG_CONFIG_HOME=/tmp/leaky-cfg",
		"XDG_STATE_HOME=/tmp/leaky-state",
		"XDG_CACHE_HOME=/tmp/leaky-cache",
		"OPENCODE_CONFIG_DIR=/tmp/leaky-oc",
		"XDG_DATA_HOME=/tmp/leaky-again",
	}
	out := ApplyIsolationEnv(in, cfg)

	counts := map[string]int{}
	vals := map[string]string{}
	for _, e := range out {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		switch k {
		case "OPENCODE_CONFIG_DIR", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME":
			counts[k]++
			vals[k] = v
		}
	}
	if counts["XDG_DATA_HOME"] != 1 {
		t.Fatalf("XDG_DATA_HOME count=%d want 1 (first-wins would leak)", counts["XDG_DATA_HOME"])
	}
	if vals["XDG_DATA_HOME"] != filepath.Join(cfg, "data") {
		t.Errorf("XDG_DATA_HOME=%q", vals["XDG_DATA_HOME"])
	}
	if vals["OPENCODE_CONFIG_DIR"] != cfg {
		t.Errorf("OPENCODE_CONFIG_DIR=%q", vals["OPENCODE_CONFIG_DIR"])
	}
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "/tmp/leaky") {
		t.Errorf("leaky parent env survived: %v", out)
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Error("unrelated env dropped")
	}
}
