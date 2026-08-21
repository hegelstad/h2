package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveOpenRouterKey_FlagBeatsEnvAndFile(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "from-env")
	dir := t.TempDir()
	secrets := filepath.Join(dir, ".secrets.env")
	if err := os.WriteFile(secrets, []byte("OPENROUTER_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveOpenRouterKey("from-flag", secrets); got != "from-flag" {
		t.Errorf("flag: got %q", got)
	}
	if got := resolveOpenRouterKey("", secrets); got != "from-env" {
		t.Errorf("env: got %q", got)
	}
	t.Setenv("OPENROUTER_API_KEY", "")
	if got := resolveOpenRouterKey("", secrets); got != "from-file" {
		t.Errorf("file: got %q", got)
	}
}

func TestStashOpenRouterKey_IsolatedAndMode600(t *testing.T) {
	cfg := t.TempDir()
	if err := stashOpenRouterKey(cfg, "sk-test-not-real"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg, openRouterKeyFile)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", st.Mode().Perm())
	}
	body, _ := os.ReadFile(path)
	if strings.TrimSpace(string(body)) != "sk-test-not-real" {
		t.Errorf("body = %q", body)
	}
}

func TestOpencodeAuthEnv_PointsInsideConfigDir(t *testing.T) {
	cfg := t.TempDir()
	env := opencodeAuthEnv(cfg, "sk-test")
	var gotCfg, gotData, gotKey string
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "OPENCODE_CONFIG_DIR="):
			gotCfg = strings.TrimPrefix(e, "OPENCODE_CONFIG_DIR=")
		case strings.HasPrefix(e, "XDG_DATA_HOME="):
			gotData = strings.TrimPrefix(e, "XDG_DATA_HOME=")
		case strings.HasPrefix(e, "OPENROUTER_API_KEY="):
			gotKey = strings.TrimPrefix(e, "OPENROUTER_API_KEY=")
		}
	}
	if gotCfg != cfg {
		t.Errorf("OPENCODE_CONFIG_DIR = %q", gotCfg)
	}
	if gotData != filepath.Join(cfg, "data") {
		t.Errorf("XDG_DATA_HOME = %q", gotData)
	}
	if !strings.HasPrefix(gotData, cfg) {
		t.Errorf("data dir not isolated under config dir")
	}
	if gotKey != "sk-test" {
		t.Errorf("OPENROUTER_API_KEY = %q", gotKey)
	}
}

func TestAuthOpencode_OpenRouterKeyFlag_DoesNotInvokeLogin(t *testing.T) {
	called := false
	old := runOpencodeLogin
	runOpencodeLogin = func(string, []string) error {
		called = true
		return nil
	}
	t.Cleanup(func() { runOpencodeLogin = old })

	cfg := t.TempDir()
	cmd := newAuthOpencodeCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{cfg, "--openrouter-key", "sk-test-flag"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("interactive login should not run when a key is stashed")
	}
	body, err := os.ReadFile(filepath.Join(cfg, openRouterKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(body)) != "sk-test-flag" {
		t.Errorf("stashed = %q", body)
	}
	if !strings.Contains(out.String(), "Stashed OpenRouter key") {
		t.Errorf("output = %q", out.String())
	}
}
