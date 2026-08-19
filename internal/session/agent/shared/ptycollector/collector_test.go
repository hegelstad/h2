package ptycollector

import (
	"testing"
	"time"

	"h2/internal/session/agent/monitor"
)

const testIdleThreshold = 10 * time.Millisecond

func TestCollector_ActiveOnOutput(t *testing.T) {
	c := New(testIdleThreshold)
	defer c.Stop()

	c.SignalOutput()

	select {
	case su := <-c.StateCh():
		if su.State != monitor.StateActive {
			t.Fatalf("expected StateActive, got %v", su.State)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StateActive")
	}
}

func TestCollector_IdleAfterThreshold(t *testing.T) {
	c := New(testIdleThreshold)
	defer c.Stop()

	c.SignalOutput()
	// Drain the active signal.
	<-c.StateCh()

	select {
	case su := <-c.StateCh():
		if su.State != monitor.StateIdle {
			t.Fatalf("expected StateIdle, got %v", su.State)
		}
	case <-time.After(testIdleThreshold + time.Second):
		t.Fatal("timed out waiting for StateIdle")
	}
}

func TestCollector_ResetTimerOnOutput(t *testing.T) {
	c := New(testIdleThreshold)
	defer c.Stop()

	c.SignalOutput()
	if su := <-c.StateCh(); su.State != monitor.StateActive {
		t.Fatalf("expected StateActive, got %v", su.State)
	}

	// A second output before idle fires resets the timer and, because we are
	// already active, must NOT emit a duplicate active update.
	time.Sleep(testIdleThreshold / 2)
	c.SignalOutput()
	select {
	case su := <-c.StateCh():
		t.Fatalf("second output while active emitted %v; want no duplicate", su.State)
	case <-time.After(testIdleThreshold / 4):
		// good: no premature emission, and the timer was reset
	}

	// After the threshold with no further output, it transitions to idle once.
	select {
	case su := <-c.StateCh():
		if su.State != monitor.StateIdle {
			t.Fatalf("expected StateIdle, got %v", su.State)
		}
	case <-time.After(testIdleThreshold + time.Second):
		t.Fatal("timed out waiting for StateIdle")
	}
}

// TestCollector_NoDuplicateActiveOnBurst is the regression test for the grok
// crash-loop: a chatty child that writes output many times a second must
// produce exactly one active transition, not one active update per write.
func TestCollector_NoDuplicateActiveOnBurst(t *testing.T) {
	c := New(testIdleThreshold)
	defer c.Stop()

	for i := 0; i < 50; i++ {
		c.SignalOutput()
	}

	// Exactly one active transition for the whole burst.
	select {
	case su := <-c.StateCh():
		if su.State != monitor.StateActive {
			t.Fatalf("expected StateActive, got %v", su.State)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StateActive")
	}

	// The very next emission must be the active->idle edge, proving no
	// duplicate active updates were queued behind it.
	select {
	case su := <-c.StateCh():
		if su.State != monitor.StateIdle {
			t.Fatalf("expected StateIdle after burst, got duplicate %v", su.State)
		}
	case <-time.After(testIdleThreshold + time.Second):
		t.Fatal("timed out waiting for StateIdle")
	}
}

// TestCollector_InterruptForcesIdle verifies SignalInterrupt still forces an
// active->idle transition, and is a no-op when already idle.
func TestCollector_InterruptForcesIdle(t *testing.T) {
	c := New(time.Hour) // long threshold so the idle timer never fires here
	defer c.Stop()

	c.SignalOutput()
	if su := <-c.StateCh(); su.State != monitor.StateActive {
		t.Fatalf("expected StateActive, got %v", su.State)
	}

	c.SignalInterrupt()
	select {
	case su := <-c.StateCh():
		if su.State != monitor.StateIdle {
			t.Fatalf("expected StateIdle after interrupt, got %v", su.State)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for interrupt-driven idle")
	}

	// A redundant interrupt while already idle must not emit anything.
	c.SignalInterrupt()
	select {
	case su := <-c.StateCh():
		t.Fatalf("unexpected emission %v from redundant interrupt", su.State)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCollector_Stop(t *testing.T) {
	c := New(testIdleThreshold)
	c.Stop()

	// After stop, SignalOutput should not panic.
	c.SignalOutput()
}
