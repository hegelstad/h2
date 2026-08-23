//go:build grok_live

// Package e2etests contains on-demand end-to-end tests that require live
// external agents and credentials. They are excluded from `make test` and
// `make test-external` by the `grok_live` build tag and only run when
// explicitly requested.
//
// # grok live round-trip smoke test
//
// This test drives the REAL Grok Build CLI through h2's actual integration
// stack — the virtual terminal, the grok harness screen-content idle
// classifier, and the message submit sequence — to prove the full path a fix
// for the "grok never receives/replies to h2 messages" bug depends on:
//
//  1. grok's continuously-repainting TUI is correctly classified IDLE from
//     rendered screen content (silence-based detection can never do this);
//  2. a delivered message (text + 50ms + \r, exactly as message.deliver writes)
//     starts a turn (classifier flips ACTIVE);
//  3. the turn completes (classifier flips back IDLE) and grok's reply appears
//     on screen.
//
// ## Requirements
//   - The `grok` binary on PATH, authenticated (GROK_HOME with valid creds).
//   - Network access to xAI.
//
// ## How to run (on-demand only)
//
//	H2_GROK_LIVE=1 \
//	H2_GROK_HOME="$HOME/h2home/grok-config/default" \
//	go test -tags grok_live -run TestGrokLiveRoundTrip -v -timeout 120s ./e2etests/
//
// It is NOT part of any make target and never runs in CI (build-tag gated).
package e2etests

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vito/midterm"

	"h2/internal/config"
	grokharness "h2/internal/session/agent/harness/grok"
	"h2/internal/session/agent/monitor"
	"h2/internal/session/virtualterminal"
)

func TestGrokLiveRoundTrip(t *testing.T) {
	if os.Getenv("H2_GROK_LIVE") != "1" {
		t.Skip("set H2_GROK_LIVE=1 (and H2_GROK_HOME) to run the live grok round-trip smoke test")
	}
	grokHome := os.Getenv("H2_GROK_HOME")
	if grokHome == "" {
		t.Fatal("H2_GROK_HOME must point at an authenticated grok config dir")
	}

	const rows, cols = 40, 120
	vt := &virtualterminal.VT{
		Vt:        midterm.NewTerminal(rows, cols),
		Rows:      rows,
		Cols:      cols,
		ChildRows: rows,
	}
	if err := vt.StartPTY("grok", nil, rows, cols, map[string]string{
		"GROK_HOME": grokHome,
		"TERM":      "xterm-256color",
	}); err != nil {
		t.Fatalf("start grok: %v", err)
	}
	defer vt.KillChild()
	go vt.PipeOutput(func() {}) // feed child output into the VT screen

	h := grokharness.New(&config.RuntimeConfig{
		HarnessType: "grok", Command: "grok", AgentName: "grok-live", CWD: t.TempDir(),
	})
	if _, err := h.PrepareForLaunch(false); err != nil {
		t.Fatalf("PrepareForLaunch: %v", err)
	}
	h.SetScreenSource(vt.ScreenText)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan monitor.AgentEvent, 128)
	go func() { _ = h.Start(ctx, events) }()

	// 1. Classifier must detect the real grok TUI as IDLE.
	waitState(t, events, monitor.StateIdle, 30*time.Second, "grok never classified idle at prompt")

	// 2. Deliver a prompt exactly as message.deliver does: text, 50ms, \r.
	if _, err := vt.WritePTY([]byte("Reply with exactly one word: PONG"), 3*time.Second); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := vt.WritePTY([]byte{'\r'}, 3*time.Second); err != nil {
		t.Fatalf("write submit: %v", err)
	}

	// 3. Turn starts (ACTIVE) then completes (IDLE).
	waitState(t, events, monitor.StateActive, 15*time.Second, "delivered message did not start a turn")
	waitState(t, events, monitor.StateIdle, 60*time.Second, "turn never completed")

	if screen := vt.ScreenText(); !strings.Contains(screen, "PONG") {
		t.Fatalf("expected grok reply to contain PONG; screen was:\n%s", screen)
	}
}

func waitState(t *testing.T, events <-chan monitor.AgentEvent, want monitor.State, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-events:
			if ev.Type != monitor.EventStateChange {
				continue
			}
			if scd, ok := ev.Data.(monitor.StateChangeData); ok && scd.State == want {
				return
			}
		case <-deadline:
			t.Fatalf("%s (timed out after %s waiting for %v)", msg, timeout, want)
		}
	}
}
