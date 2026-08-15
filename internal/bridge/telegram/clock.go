package telegram

import "time"

// clock is injectable so draft floor / refresh / abandon can be tested
// without real sleeps.
type clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) (stop func())
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) func() {
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

// manualClock is a test clock. AfterFunc callbacks fire when Advance
// crosses their deadline.
type manualClock struct {
	now    time.Time
	timers []manualTimer
}

type manualTimer struct {
	when time.Time
	fn   func()
	stop bool
}

func newManualClock(now time.Time) *manualClock {
	return &manualClock{now: now}
}

func (c *manualClock) Now() time.Time { return c.now }

func (c *manualClock) AfterFunc(d time.Duration, f func()) func() {
	t := manualTimer{when: c.now.Add(d), fn: f}
	c.timers = append(c.timers, t)
	idx := len(c.timers) - 1
	return func() { c.timers[idx].stop = true }
}

func (c *manualClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
	// Fire due timers; newly scheduled ones during callbacks are also
	// considered if already due.
	for {
		fired := false
		for i := range c.timers {
			t := &c.timers[i]
			if t.stop || t.fn == nil || t.when.After(c.now) {
				continue
			}
			fn := t.fn
			t.fn = nil
			fn()
			fired = true
		}
		if !fired {
			return
		}
	}
}
