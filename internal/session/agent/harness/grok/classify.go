package grok

import "strings"

// Grok Build TUI markers used to classify agent state from rendered screen
// content. Grok Build has no OTEL/hook integration and its TUI repaints
// continuously (animated spinner + elapsed clock), so it never goes
// output-silent — silence-based idle detection (ptycollector) can never fire
// for it. Instead we read what is on screen.
//
// UPDATE THESE STRINGS if Grok Build changes its TUI wording. The classifier is
// exercised against captured real screen frames in classify_test.go
// (testdata/frame-idle.txt and testdata/frame-active.txt), so a wording change
// fails those tests loudly here rather than silently wedging message delivery
// in production.
var (
	// activeMarkers appear ONLY while a turn is running (the model is
	// responding or a tool is executing). Their presence unambiguously means
	// "busy". Note: generic footer hints like "Ctrl+x:shortcuts" and
	// "Shift+Tab:mode" are deliberately NOT here — they also appear while
	// composing an idle prompt, so they would false-positive as active.
	activeMarkers = []string{
		"Waiting for response",
		"[stop]",
		"Esc:cancel",
	}

	// readyMarkers are Grok Build chrome present whenever the CLI is up and at
	// its prompt — in BOTH idle and active states. They distinguish a genuine
	// grok screen from an unknown one (blank startup, a crash, a bare shell),
	// so an unrecognized screen is never mistaken for idle.
	readyMarkers = []string{
		"Grok",
	}
)

// screenState is the coarse agent state derived from a rendered screen.
type screenState int

const (
	// stateUnknown means the screen matched no known grok marker: the TUI has
	// not painted yet, has crashed, or has changed its strings. Callers hold
	// the last known state rather than guessing.
	stateUnknown screenState = iota
	stateActive
	stateIdle
)

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// classifyScreen maps rendered screen text to a coarse agent state:
//   - an active marker present         -> stateActive  (a turn is running)
//   - grok chrome present, no active   -> stateIdle     (at the prompt, ready)
//   - neither                          -> stateUnknown  (hold last state)
//
// Active is checked first so a transient frame that still shows both the input
// chrome and the turn indicator is treated as busy (never delivered into).
func classifyScreen(screen string) screenState {
	if containsAny(screen, activeMarkers) {
		return stateActive
	}
	if containsAny(screen, readyMarkers) {
		return stateIdle
	}
	return stateUnknown
}
