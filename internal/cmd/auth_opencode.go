package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"h2/internal/config"
	ocharness "h2/internal/session/agent/harness/opencode"
)

const openRouterKeyFile = "openrouter.key"

func newAuthOpencodeCmd() *cobra.Command {
	var openRouterKey string
	cmd := &cobra.Command{
		Use:   "opencode [config-dir]",
		Short: "Authenticate an opencode config directory",
		Long: `Authenticate opencode for use with h2 agents.

If no config-dir is provided, authenticates the default shared config:
  ~/.h2/opencode-config/default

Two modes (the flag selects the mode — a host OPENROUTER_API_KEY does not
force stash on a bare invocation):
  h2 auth opencode
      Interactive: runs "opencode auth login" with OPENCODE_CONFIG_DIR and
      XDG_* isolated under the config dir (parent XDG_* are replaced, not
      appended, so libc first-wins cannot leak to ~/.local/share/opencode).

  h2 auth opencode --openrouter-key <KEY>
  h2 auth opencode --openrouter-key=
      Non-interactive Ox Alpha path. Stashes the key in the config dir so
      OPENROUTER_API_KEY resolves. If the flag is present but empty,
      reads OPENROUTER_API_KEY from the environment or ~/h2home/.secrets.env.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthOpencode(cmd, args, openRouterKey)
		},
	}
	cmd.Flags().StringVar(&openRouterKey, "openrouter-key", "", "OpenRouter API key (or set OPENROUTER_API_KEY / ~/h2home/.secrets.env)")
	return cmd
}

func defaultOpencodeConfigDir() string {
	return filepath.Join(config.ConfigDir(), "opencode-config", "default")
}

func expandHome(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return home + path[1:], nil
}

func defaultSecretsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "h2home", ".secrets.env")
}

func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, sc.Err()
}

// resolveOpenRouterKey prefers the flag, then process env, then secrets file.
func resolveOpenRouterKey(flagKey, secretsPath string) string {
	if strings.TrimSpace(flagKey) != "" {
		return strings.TrimSpace(flagKey)
	}
	if v := os.Getenv("OPENROUTER_API_KEY"); v != "" {
		return v
	}
	if secretsPath == "" {
		return ""
	}
	m, err := parseEnvFile(secretsPath)
	if err != nil {
		return ""
	}
	return m["OPENROUTER_API_KEY"]
}

func stashOpenRouterKey(cfgDir, key string) error {
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(cfgDir, openRouterKeyFile)
	return os.WriteFile(path, []byte(strings.TrimSpace(key)+"\n"), 0o600)
}

func opencodeAuthEnv(cfgDir, key string) []string {
	env := ocharness.ApplyIsolationEnv(os.Environ(), cfgDir)
	if key != "" {
		filtered := env[:0]
		for _, e := range env {
			if strings.HasPrefix(e, "OPENROUTER_API_KEY=") {
				continue
			}
			filtered = append(filtered, e)
		}
		env = append(filtered, "OPENROUTER_API_KEY="+key)
	}
	return env
}

var runOpencodeLogin = func(cfgDir string, env []string) error {
	cmd := exec.Command("opencode", "auth", "login")
	cmd.Env = env
	cmd.Dir = cfgDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runAuthOpencode(cmd *cobra.Command, args []string, flagKey string) error {
	var configDir string
	if len(args) > 0 {
		configDir = args[0]
	} else {
		configDir = defaultOpencodeConfigDir()
	}
	var err error
	configDir, err = expandHome(configDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create opencode config dir: %w", err)
	}
	for _, d := range ocharness.IsolationEnv(configDir) {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create opencode isolation dir: %w", err)
		}
	}

	if cmd.Flags().Changed("openrouter-key") {
		key := resolveOpenRouterKey(flagKey, defaultSecretsPath())
		if key == "" {
			return fmt.Errorf("OPENROUTER_API_KEY not found (flag empty, env unset, secrets file missing)")
		}
		if err := stashOpenRouterKey(configDir, key); err != nil {
			return fmt.Errorf("stash openrouter key: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "✓ Stashed OpenRouter key in %s (mode 0600)\n", filepath.Join(configDir, openRouterKeyFile))
		fmt.Fprintf(cmd.OutOrStdout(), "  OPENCODE_CONFIG_DIR=%s\n", configDir)
		fmt.Fprintf(cmd.OutOrStdout(), "  XDG_DATA_HOME=%s\n", filepath.Join(configDir, "data"))
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Launching opencode auth login (OPENCODE_CONFIG_DIR=%s)\n", configDir)
	if err := runOpencodeLogin(configDir, opencodeAuthEnv(configDir, "")); err != nil {
		return fmt.Errorf("opencode auth login: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "✓ opencode auth login finished: %s\n", configDir)
	return nil
}
