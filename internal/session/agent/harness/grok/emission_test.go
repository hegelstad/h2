package grok

import (
	"testing"
	"time"

	"h2/internal/session/agent/monitor"
)

// countEvents drains the channel for d and returns the states seen, in order.
func countEvents(events <-chan monitor.AgentEvent, d time.Duration) []monitor.State {
	var got []monitor.State
	deadline := time.After(d)
	for {
		select {
		case ev := <-events:
			if ev.Type != monitor.EventStateChange {
				continue
			}
			if scd, ok := ev.Data.(monitor.StateChangeData); ok {
				got = append(got, scd.State)
			}
		case <-deadline:
			return got
		}
	}
}

// TestStart_EmitsOnlyOnTransition pins a load-bearing invariant: the harness
// must emit a state change ONLY when the state actually changes, never once
// per poll tick.
//
// This is not a style preference — it is what makes the idle-staleness
// watchdog able to rescue a classifier miss. AgentMonitor.processEvent resets
// lastActivityAt on EVERY event it processes, and maybeReconcileIdle only
// forces Active->Idle once lastActivityAt has been stale for
// DefaultIdleStaleTimeout (2m) with a delivery backlog present. A harness that
// re-emits Active every pollInterval (150ms) would reset that timer forever,
// so the watchdog could never fire and a wedged classifier would strand
// inter-agent message delivery permanently, with no recovery path.
//
// If you are refactoring the Start loop and this test fails, do not relax it —
// restore the transition guards.
func TestStart_EmitsOnlyOnTransition(t *testing.T) {
	// Comfortably more than 10 poll intervals (10 * 150ms = 1.5s).
	const observe = 2 * time.Second

	t.Run("steady active screen emits once", func(t *testing.T) {
		fs := &fakeScreen{text: miniActiveScreen}
		// Precondition: the fixture must genuinely classify Active, else the
		// exactly-1-event assertion passes vacuously (the initial emit is always
		// Active) and the transition-only guard tests nothing.
		if got := classifyScreen(fs.get()); got != stateActive {
			t.Fatalf("precondition: steady screen must classify stateActive, got %v", got)
		}
		_, events, cancel := startHarness(t, fs.get)
		defer cancel()

		got := countEvents(events, observe)
		if len(got) != 1 || got[0] != monitor.StateActive {
			t.Errorf("steady Active screen produced %v (%d events), want exactly 1 Active; "+
				"per-tick emission would permanently disarm the idle-staleness watchdog", got, len(got))
		}
	})

	t.Run("steady idle screen emits initial active then one idle", func(t *testing.T) {
		fs := &fakeScreen{text: miniIdleScreen}
		if got := classifyScreen(fs.get()); got != stateIdle {
			t.Fatalf("precondition: steady screen must classify stateIdle, got %v", got)
		}
		_, events, cancel := startHarness(t, fs.get)
		defer cancel()

		got := countEvents(events, observe)
		want := []monitor.State{monitor.StateActive, monitor.StateIdle}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("steady Idle screen produced %v (%d events), want exactly %v; "+
				"idle must be declared once, not re-emitted every poll", got, len(got), want)
		}
	})
}
