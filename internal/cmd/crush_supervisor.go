package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"h2/internal/session/agent/harness/crush"
)

func newCrushSupervisorCmd() *cobra.Command {
	var (
		dataDir       string
		host          string
		resumeSession string
		sessionFile   string
		model         string
		turnTimeout   time.Duration
	)

	cmd := &cobra.Command{
		Use:   crush.SupervisorSubcommand,
		Short: "Internal: turn supervisor shim for the Crush harness",
		Long: `Long-lived PTY child for h2 Crush agent sessions. Reads delivered
messages (one per line, h2 delivery envelope) from stdin and executes one
'crush run' turn per message, reporting lifecycle through hook events.

This command is not part of the public CLI surface; it is re-executed by the
crush harness as the session's PTY child.`,
		Hidden:        true,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dataDir == "" {
				return fmt.Errorf("--data-dir is required")
			}
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
			defer stop()
			return crush.Run(ctx, crush.Supervisor{
				DataDir:         dataDir,
				Host:            host,
				ResumeSessionID: resumeSession,
				SessionFile:     sessionFile,
				Model:           model,
				TurnTimeout:     turnTimeout,
			}, os.Stdin, os.Stdout, os.Stderr)
		},
	}

	cmd.Flags().StringVar(&dataDir, "data-dir", "", "per-agent crush data dir (passed to every crush command)")
	cmd.Flags().StringVar(&host, "host", "", "optional --host socket pinned on every crush command")
	cmd.Flags().StringVar(&resumeSession, "resume-session", "", "resume this crush session id on every turn")
	cmd.Flags().StringVar(&sessionFile, "session-file", "", "where to persist the captured crush session id")
	cmd.Flags().StringVar(&model, "model", "", "model id passed as -m/--small-model on every run turn")
	cmd.Flags().DurationVar(&turnTimeout, "turn-timeout", 0, "kill an in-flight turn after this long (default 30m)")

	return cmd
}
