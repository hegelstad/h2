package grok

import "strings"

// Grok Build TUI state classification from rendered screen content.
//
// Grok Build has no OTEL/hook integration and its TUI repaints continuously
// (animated spinner + elapsed clock), so it never goes output-silent —
// silence-based idle detection (ptycollector) can never fire for it. Instead we
// read what is on screen.
//
// The classifier is REGION-SCOPED, not a whole-screen substring match. A naive
// scan of the full screen would read marker text appearing in the conversation
// TRANSCRIPT (e.g. a user quoting "Waiting for response", grok's reply
// containing "[stop]", or grok viewing this very file) as a running turn, pin
// the agent Active forever, and strand inter-agent message delivery — the exact
// bug this harness exists to fix. So markers are only honored in the two screen
// regions the transcript can never occupy, both anchored on the input box:
//
//	…transcript…                          <- markers here are IGNORED
//	⠴ Waiting for response… 3.6s  [stop]   <- STATUS line (first non-blank above box)
//	╭──────────────────────────────────╮
//	│ ❯                                │   <- input box (grows UPWARD when multi-line)
//	╰────────── Grok 4.6 (xhigh) · … ─╯    <- box bottom carries ready chrome ("Grok")
//	Shift+Tab:mode │ Esc:cancel │ …        <- FOOTER (below box bottom)
//
// Two independent, transcript-immune active signals (either implies a turn):
//  1. the status line carries an animated braille spinner (U+2800–U+28FF) AND a
//     status phrase ("Waiting for response" / "[stop]"). The braille requirement
//     defeats prose that merely quotes a phrase — transcript text has no spinner.
//  2. the footer carries "Esc:cancel". The footer sits below the input box, so
//     the transcript can never reach it; empirically Esc:cancel appears only
//     during a turn (absent when idle or composing input).
//
// UPDATE THESE STRINGS if Grok Build changes its TUI wording. The classifier is
// exercised against captured real screen frames in classify_test.go and
// regression_test.go (testdata/frame-*.txt), so a wording/layout change fails
// those tests loudly here rather than silently wedging message delivery.
const (
	boxTopBorder = "╭" // input box top border (rounded corner)
	boxBotBorder = "╰" // input box bottom border, carries the ready chrome

	// readyChrome is Grok Build chrome rendered in the input box bottom border
	// ("… Grok 4.6 (xhigh) · always-approve …"). Its presence at/below the box
	// top confirms a genuine grok prompt screen (vs blank startup / crash / a
	// bare shell), so an unrecognized screen is never mistaken for idle.
	readyChrome = "Grok"

	// footerActiveMarker appears in the footer (below the box) only during a
	// turn. Transcript-immune: the footer is below the input box.
	footerActiveMarker = "Esc:cancel"
)

// statusActivePhrases are honored ONLY on the status line (the first non-blank
// line above the input box) and ONLY when a braille spinner co-occurs there.
var statusActivePhrases = []string{
	"Waiting for response",
	"[stop]",
}

// screenState is the coarse agent state derived from a rendered screen.
type screenState int

const (
	// stateUnknown means no input box was found: the TUI has not painted yet,
	// has crashed, or changed its chrome. Callers hold the last known state.
	stateUnknown screenState = iota
	stateActive
	stateIdle
)

// hasBrailleSpinner reports whether s contains a braille pattern rune
// (U+2800–U+28FF), which is what Grok Build animates its "working" spinner with.
// Prose in the transcript does not contain these.
func hasBrailleSpinner(s string) bool {
	for _, r := range s {
		if r >= 0x2800 && r <= 0x28FF {
			return true
		}
	}
	return false
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// classifyScreen maps rendered screen text to a coarse agent state by looking
// only at the status line above the input box and the footer below it:
//   - active signal in either region -> stateActive (a turn is running)
//   - input box present, no active    -> stateIdle    (at the prompt, ready)
//   - no input box / no ready chrome  -> stateUnknown (hold last state)
func classifyScreen(screen string) screenState {
	rows := strings.Split(screen, "\n")

	// Anchor on the input box, which is always the bottom-most UI element — so
	// its top border is the LAST box-top border on screen. Everything above the
	// status line (the transcript) is deliberately never inspected.
	boxTop := lastIndexContaining(rows, boxTopBorder)
	if boxTop < 0 {
		return stateUnknown
	}
	boxBot := firstIndexContainingFrom(rows, boxTop+1, boxBotBorder)
	if boxBot < 0 {
		return stateUnknown // mid-render / malformed box: hold last state
	}

	// Confirm this is a grok prompt (ready chrome in the box or footer region,
	// never the transcript) before trusting idle.
	if !strings.Contains(strings.Join(rows[boxTop:], "\n"), readyChrome) {
		return stateUnknown
	}

	// Status line: first non-blank line scanning upward from the box top. When
	// the input box grows (multi-line input), the box top — and this status
	// line with it — move up together, so the anchor holds regardless of height.
	statusLine := ""
	for i := boxTop - 1; i >= 0; i-- {
		if strings.TrimSpace(rows[i]) != "" {
			statusLine = rows[i]
			break
		}
	}
	if hasBrailleSpinner(statusLine) && containsAny(statusLine, statusActivePhrases) {
		return stateActive
	}

	// Footer: everything below the box bottom border.
	footer := strings.Join(rows[boxBot+1:], "\n")
	if strings.Contains(footer, footerActiveMarker) {
		return stateActive
	}

	return stateIdle
}

// lastIndexContaining returns the highest index whose row contains sub, or -1.
func lastIndexContaining(rows []string, sub string) int {
	for i := len(rows) - 1; i >= 0; i-- {
		if strings.Contains(rows[i], sub) {
			return i
		}
	}
	return -1
}

// firstIndexContainingFrom returns the lowest index >= from whose row contains
// sub, or -1.
func firstIndexContainingFrom(rows []string, from int, sub string) int {
	for i := from; i < len(rows); i++ {
		if strings.Contains(rows[i], sub) {
			return i
		}
	}
	return -1
}
