package grok

import (
	"os"
	"path/filepath"
	"testing"
)

// TestClassifyScreen_RealFrames pins the classifier against REAL Grok Build
// screen frames captured from grok 1.0.4 and rendered through the same midterm
// terminal the session uses (testdata/frame-*.txt). If Grok Build changes its
// TUI wording such that these markers move, this test fails here — loudly —
// instead of silently stranding inter-agent message delivery in production.
// Regenerate the fixtures and update classify.go's marker lists together.
func TestClassifyScreen_RealFrames(t *testing.T) {
	cases := []struct {
		fixture string
		want    screenState
	}{
		{"frame-idle.txt", stateIdle},     // at the prompt, no turn running
		{"frame-active.txt", stateActive}, // "Waiting for response… [stop]"
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tc.fixture))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			if got := classifyScreen(string(data)); got != tc.want {
				t.Errorf("classifyScreen(%s) = %v, want %v", tc.fixture, got, tc.want)
			}
		})
	}
}

func TestClassifyScreen_Synthetic(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   screenState
	}{
		{"empty screen is unknown", "", stateUnknown},
		{"bare shell prompt is unknown", "ubuntu@host:~$ \n", stateUnknown},
		{"active marker wins over ready", "Grok 4.6\nWaiting for response… 2s [stop]\n", stateActive},
		{"stop marker alone is active", "Grok\n... [stop]\n", stateActive},
		{"esc:cancel marker is active", "Grok\nShift+Tab:mode  │  Esc:cancel  │  Ctrl+x:shortcuts\n", stateActive},
		{"grok chrome with no active marker is idle", "❯ \nGrok 4.6 (xhigh) · always-approve\n", stateIdle},
		// A compose-idle screen shows Ctrl+x:shortcuts/Shift+Tab:mode but NO
		// active marker — must NOT be misread as active.
		{"compose-idle hints are not active", "❯ hello\nEnter:send  │  Alt+Enter:newline  │  Shift+Tab:mode  │  Ctrl+x:shortcuts\nGrok 4.6\n", stateIdle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyScreen(tc.screen); got != tc.want {
				t.Errorf("classifyScreen(%q) = %v, want %v", tc.screen, got, tc.want)
			}
		})
	}
}
