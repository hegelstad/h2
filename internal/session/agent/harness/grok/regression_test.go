package grok

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadFrame reads a captured Grok Build screen fixture as rendered rows.
func loadFrame(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return strings.Split(string(data), "\n")
}

// injectAt replaces row i (0-indexed) with text, preserving screen height so
// the frame stays a realistic full-height render.
func injectAt(rows []string, i int, text string) string {
	out := append([]string(nil), rows...)
	if i < len(out) {
		out[i] = text
	}
	return strings.Join(out, "\n")
}

// TestClassify_MarkerInTranscriptIsNotActive is the regression test for the
// whole-screen-match bug: marker strings appearing in the CONVERSATION
// TRANSCRIPT (not the status region) must not be read as a running turn.
//
// Without region-scoped matching every case here classifies Active, which pins
// the agent Active for as long as the text stays on screen and strands
// inter-agent message delivery — the exact failure this harness exists to fix.
// Most concretely: grok agents work on the h2 repo, so grok opening
// classify.go (which contains all three marker strings verbatim) wedges itself.
func TestClassify_MarkerInTranscriptIsNotActive(t *testing.T) {
	idle := loadFrame(t, "frame-idle.txt")
	cases := []struct{ name, transcript string }{
		{"user message quoting a marker", `  ❯ why does h2 print "Waiting for response" forever?`},
		{"grok reply containing [stop]", `  The active markers are "[stop]" and Esc:cancel.`},
		{"grok reading classify.go", `     "Waiting for response", "[stop]", "Esc:cancel",`},
		{"tool result diffing the marker list", `  -   "[stop]",  +   "Esc:cancel",`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Row 3 is well inside the transcript region, far from the
			// input box / footer status region at the bottom.
			screen := injectAt(idle, 3, tc.transcript)
			if got := classifyScreen(screen); got != stateIdle {
				t.Errorf("classifyScreen = %v, want stateIdle; a marker in the transcript "+
					"must not be read as a running turn (would strand message delivery)", got)
			}
		})
	}
}

// TestClassify_ActiveStillDetectedAfterScoping guards the INVERSE failure of
// the fix above: the status region must be wide enough to still see a real
// turn. "Waiting for response"/"[stop]" render ABOVE the input box while
// "Esc:cancel" renders BELOW it, so a window anchored at the box and scanning
// downward would miss the primary turn indicators entirely — and h2 would then
// deliver messages into a live turn.
func TestClassify_ActiveStillDetectedAfterScoping(t *testing.T) {
	if got := classifyScreen(strings.Join(loadFrame(t, "frame-active.txt"), "\n")); got != stateActive {
		t.Errorf("classifyScreen(frame-active) = %v, want stateActive; the status region "+
			"must still cover the turn indicator above the input box", got)
	}
	// Same, with the transcript also mentioning a marker: still active.
	active := loadFrame(t, "frame-active.txt")
	screen := injectAt(active, 3, `  ❯ explain "[stop]" and Esc:cancel`)
	if got := classifyScreen(screen); got != stateActive {
		t.Errorf("classifyScreen(frame-active + transcript marker) = %v, want stateActive", got)
	}
}
