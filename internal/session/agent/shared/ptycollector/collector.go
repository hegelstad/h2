// Package ptycollector provides a reusable PTY idle detector. It monitors
// output notifications and emits active/idle state transitions based on a
// configurable silence threshold.
package ptycollector

import (
	"time"

	"h2/internal/session/agent/monitor"
)

// Collector derives state from child PTY output.
// It goes active on the first SignalOutput signal and idle after the configured
// threshold with no further output. State updates are emitted only on genuine
// transitions (idle->active, active->idle); repeated output while already
// active does not re-emit active — it only defers the idle timer. This keeps a
// chatty child (e.g. a streaming CLI that writes output many times a second)
// from flooding the event stream with duplicate "active" updates, which
// previously ballooned the per-agent event log and OOM-killed the harness.
type Collector struct {
	idleThreshold time.Duration
	notifyCh      chan struct{}
	interruptCh   chan struct{}
	stateCh       chan monitor.StateUpdate
	stopCh        chan struct{}
}

// New creates and starts a Collector with the given idle threshold.
func New(idleThreshold time.Duration) *Collector {
	c := &Collector{
		idleThreshold: idleThreshold,
		notifyCh:      make(chan struct{}, 1),
		interruptCh:   make(chan struct{}, 1),
		stateCh:       make(chan monitor.StateUpdate, 1),
		stopCh:        make(chan struct{}),
	}
	go c.run()
	return c
}

// SignalOutput signals that the child process produced output.
func (c *Collector) SignalOutput() {
	select {
	case c.notifyCh <- struct{}{}:
	default:
	}
}

// SignalInterrupt forces an idle transition (e.g. on local Ctrl+C). It is
// routed through the run loop so state tracking stays consistent; if the
// collector is already idle it is a no-op.
func (c *Collector) SignalInterrupt() {
	select {
	case c.interruptCh <- struct{}{}:
	default:
	}
}

// StateCh returns the channel that receives state updates.
func (c *Collector) StateCh() <-chan monitor.StateUpdate {
	return c.stateCh
}

// Stop stops the internal goroutine.
func (c *Collector) Stop() {
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
}

func (c *Collector) run() {
	idleTimer := time.NewTimer(c.idleThreshold)
	defer idleTimer.Stop()

	// current tracks the last emitted state so we only send on transitions.
	// Seed with StateInitialized (the monitor's own starting state), not Idle:
	// a child that produces no output must still emit its first idle->... edge
	// so the monitor leaves Initialized. Seeding Idle would dedup that first
	// idle away and strand the monitor in Initialized forever.
	current := monitor.StateInitialized
	setState := func(s monitor.State) {
		if s == current {
			return
		}
		current = s
		c.send(s)
	}

	for {
		select {
		case <-c.notifyCh:
			setState(monitor.StateActive)
			resetTimer(idleTimer, c.idleThreshold)
		case <-c.interruptCh:
			setState(monitor.StateIdle)
		case <-idleTimer.C:
			setState(monitor.StateIdle)
		case <-c.stopCh:
			return
		}
	}
}

func (c *Collector) send(s monitor.State) {
	su := monitor.StateUpdate{State: s, SubState: monitor.SubStateNone}
	select {
	case <-c.stateCh:
	default:
	}
	c.stateCh <- su
}

// resetTimer safely resets a timer, draining the channel if needed.
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}
