package grok

import (
	"os"
	"path/filepath"
	"testing"
)

// Minimal but STRUCTURALLY REAL screens shared across the grok harness tests.
// The classifier is region-scoped (status line above the input box, footer
// below it), so test screens must contain a real input box — a bare line of
// text classifies as stateUnknown, by design. These mirror the layout of the
// captured testdata/frame-*.txt fixtures at small size.
const (
	miniIdleScreen = "prior reply text from grok\n" +
		"\n" +
		"  ╭──────────────────────────────────────────╮\n" +
		"  │ ❯                                        │\n" +
		"  ╰──────── Grok 4.6 (xhigh) · always-approve ────╯\n" +
		"\n" +
		"  Shift+Tab:mode  │  Ctrl+x:shortcuts\n"

	miniActiveScreen = "  ⠧ Waiting for response… 1.2s                    1.2s [stop]\n" +
		"\n" +
		"  ╭──────────────────────────────────────────╮\n" +
		"  │ ❯                                        │\n" +
		"  ╰──────── Grok 4.6 (xhigh) · always-approve ────╯\n" +
		"\n" +
		"  Shift+Tab:mode  │  Esc:cancel  │  Ctrl+x:shortcuts\n"

	// idle while composing non-empty input: box at the very top of the region,
	// no status line above it, footer has no Esc:cancel.
	miniComposeScreen = "  ╭──────────────────────────────────────────╮\n" +
		"  │ ❯ here is my draft message               │\n" +
		"  ╰──────── Grok 4.6 (xhigh) · always-approve ────╯\n" +
		"\n" +
		"  Enter:send  │  Alt+Enter:newline  │  Shift+Tab:mode  │  Ctrl+x:shortcuts\n"
)

// TestClassifyScreen_RealFrames pins the classifier against REAL Grok Build
// screen frames captured from grok 1.0.4 and rendered through the same midterm
// terminal the session uses (testdata/frame-*.txt). If Grok Build changes its
// TUI wording/layout such that the markers or the input box move, this fails
// here — loudly — instead of silently stranding inter-agent message delivery.
// Regenerate the fixtures (see e2etests/grok_capture_test.go) and update
// classify.go together.
func TestClassifyScreen_RealFrames(t *testing.T) {
	cases := []struct {
		fixture string
		want    screenState
	}{
		{"frame-idle.txt", stateIdle},               // at the prompt just after a turn completed
		{"frame-compose-idle.txt", stateIdle},       // idle with non-empty input in the box
		{"frame-active.txt", stateActive},           // turn running (spinner + [stop] + Esc:cancel)
		{"frame-active-multiline.txt", stateActive}, // turn running, multi-line input box grown upward
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

// TestClassifyScreen_Regions exercises the region-scoping rules directly.
func TestClassifyScreen_Regions(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   screenState
	}{
		{"empty screen is unknown", "", stateUnknown},
		{"no input box is unknown", "ubuntu@host:~$ ls\nfile.go\n", stateUnknown},
		{"idle prompt", miniIdleScreen, stateIdle},
		{"idle while composing", miniComposeScreen, stateIdle},
		{"turn running", miniActiveScreen, stateActive},

		// Region scoping: markers in the TRANSCRIPT (above the status line) must
		// be ignored — this is the whole-screen-match bug the harness exists to
		// avoid re-introducing.
		{
			"marker phrase in transcript is ignored",
			"  ❯ why does it print \"Waiting for response\" and [stop] forever?\n" +
				"\n" +
				"  ╭──────────────╮\n  │ ❯            │\n  ╰── Grok 4.6 ──╯\n" +
				"  Shift+Tab:mode  │  Ctrl+x:shortcuts\n",
			stateIdle,
		},
		{
			"Esc:cancel in transcript (above box) is ignored",
			"  the active footer shows Esc:cancel while running\n" +
				"  ╭──────────────╮\n  │ ❯            │\n  ╰── Grok 4.6 ──╯\n" +
				"  Shift+Tab:mode  │  Ctrl+x:shortcuts\n",
			stateIdle,
		},

		// The braille spinner is required to co-occur with a status phrase: a
		// status line that quotes a phrase but has no spinner is NOT a turn.
		{
			"status phrase without braille spinner is not active",
			"  Waiting for response is the phrase we grep for\n" +
				"  ╭──────────────╮\n  │ ❯            │\n  ╰── Grok 4.6 ──╯\n" +
				"  Shift+Tab:mode  │  Ctrl+x:shortcuts\n",
			stateIdle,
		},

		// Either active signal alone suffices.
		{
			"braille + phrase on status line alone is active",
			"  ⠹ Waiting for response… 3s\n" +
				"  ╭──────────────╮\n  │ ❯            │\n  ╰── Grok 4.6 ──╯\n" +
				"  Shift+Tab:mode  │  Ctrl+x:shortcuts\n",
			stateActive,
		},
		{
			"Esc:cancel in footer alone is active",
			"  ╭──────────────╮\n  │ ❯            │\n  ╰── Grok 4.6 ──╯\n" +
				"  Shift+Tab:mode  │  Esc:cancel  │  Ctrl+x:shortcuts\n",
			stateActive,
		},

		// A box with no ready chrome ("Grok") is not trusted as a grok prompt.
		{
			"input box without grok chrome is unknown",
			"  ╭──────────────╮\n  │ ❯            │\n  ╰──────────────╯\n" +
				"  Shift+Tab:mode  │  Ctrl+x:shortcuts\n",
			stateUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyScreen(tc.screen); got != tc.want {
				t.Errorf("classifyScreen = %v, want %v\nscreen:\n%s", got, tc.want, tc.screen)
			}
		})
	}
}
