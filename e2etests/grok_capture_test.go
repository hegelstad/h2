//go:build grok_capture

// Frame-capture tool (on-demand, not a real test). It drives the live Grok
// Build CLI through the real VT and dumps a numbered ScreenText() snapshot
// every ~200ms plus a scripted interaction, so we can pick honest fixtures for
// the classifier (idle-at-prompt-after-turn, composing, active, active with a
// multi-line input box). Frames land in $H2_GROK_CAPTURE_DIR (default
// /tmp/grokframes) as NNN.txt with a MANIFEST logging what was happening.
//
//	H2_GROK_LIVE=1 H2_GROK_HOME="$HOME/h2home/grok-config/default" \
//	  go test -tags grok_capture -run TestGrokCaptureFrames -v -timeout 180s ./e2etests/
package e2etests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vito/midterm"

	"h2/internal/session/virtualterminal"
)

func TestGrokCaptureFrames(t *testing.T) {
	if os.Getenv("H2_GROK_LIVE") != "1" {
		t.Skip("set H2_GROK_LIVE=1 (and H2_GROK_HOME) to run the live capture tool")
	}
	grokHome := os.Getenv("H2_GROK_HOME")
	if grokHome == "" {
		t.Fatal("H2_GROK_HOME must point at an authenticated grok config dir")
	}
	outDir := os.Getenv("H2_GROK_CAPTURE_DIR")
	if outDir == "" {
		outDir = "/tmp/grokframes"
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const rows, cols = 40, 120
	vt := &virtualterminal.VT{
		Vt: midterm.NewTerminal(rows, cols), Rows: rows, Cols: cols, ChildRows: rows,
	}
	if err := vt.StartPTY("grok", nil, rows, cols, map[string]string{
		"GROK_HOME": grokHome, "TERM": "xterm-256color",
	}); err != nil {
		t.Fatalf("start grok: %v", err)
	}
	defer vt.KillChild()
	go vt.PipeOutput(func() {})

	var mu sync.Mutex
	phase := "startup"
	setPhase := func(p string) { mu.Lock(); phase = p; mu.Unlock() }

	// Dump a frame every 200ms with the current phase label.
	var manifest strings.Builder
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		n := 0
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				mu.Lock()
				p := phase
				mu.Unlock()
				screen := vt.ScreenText()
				name := fmt.Sprintf("%03d.txt", n)
				_ = os.WriteFile(filepath.Join(outDir, name), []byte(screen), 0o644)
				marker := ""
				for _, m := range []string{"Waiting for response", "[stop]", "Esc:cancel"} {
					if strings.Contains(screen, m) {
						marker += " " + m
					}
				}
				fmt.Fprintf(&manifest, "%s phase=%-14s markers=[%s]\n", name, p, strings.TrimSpace(marker))
				n++
			}
		}
	}()

	type step struct {
		phase string
		write string
		wait  time.Duration
	}
	steps := []step{
		{"splash-idle", "", 4 * time.Second},
		{"submit-pong", "Reply with exactly one word: PONG", 200 * time.Millisecond},
		{"submit-pong", "\r", 8 * time.Second}, // let the turn run + complete
		{"idle-post-turn", "", 3 * time.Second},
		{"composing", "here is a composed line of input that stays in the box", 3 * time.Second},
		{"clear-compose", strings.Repeat("\x7f", 60), 1 * time.Second}, // backspace it out
		// A slow prompt so we can type a multi-line message DURING the turn.
		{"submit-slow", "Count slowly from 1 to 30, one number per line.", 200 * time.Millisecond},
		{"submit-slow", "\r", 1500 * time.Millisecond},
		{"active-multiline", "draft line one\x1b\rdraft line two\x1b\rdraft line three", 4 * time.Second},
	}
	for _, s := range steps {
		setPhase(s.phase)
		if s.write != "" {
			if _, err := vt.WritePTY([]byte(s.write), 3*time.Second); err != nil {
				t.Logf("write %q: %v", s.phase, err)
			}
		}
		time.Sleep(s.wait)
	}

	setPhase("done")
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	_ = os.WriteFile(filepath.Join(outDir, "MANIFEST.txt"), []byte(manifest.String()), 0o644)
	t.Logf("captured frames to %s (see MANIFEST.txt)", outDir)
}
