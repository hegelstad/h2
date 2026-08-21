package opencode

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"h2/internal/session/agent/monitor"
)

func TestMapHookEvent(t *testing.T) {
	idle, sub, ok := MapHookEvent("opencode.session.idle")
	if !ok || idle != monitor.StateIdle || sub != monitor.SubStateNone {
		t.Errorf("idle mapping = %v %v ok=%v", idle, sub, ok)
	}
	active, _, ok := MapHookEvent("opencode.session.active")
	if !ok || active != monitor.StateActive {
		t.Errorf("active mapping = %v ok=%v", active, ok)
	}
	if _, _, ok := MapHookEvent("UserPromptSubmit"); ok {
		t.Error("claude hook names must not be claimed by opencode")
	}
}

func TestOpencode_EmitsOnlyOnTransition(t *testing.T) {
	h := New(isolatedRC(t), nil)
	if _, err := h.PrepareForLaunch(true); err != nil {
		t.Fatal(err)
	}
	events := make(chan monitor.AgentEvent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = h.Start(ctx, events)
		close(done)
	}()

	got := collectState(t, events, 1, time.Second)
	if got[0] != monitor.StateActive {
		t.Fatalf("seed state = %v, want Active", got[0])
	}

	if !h.HandleHookEvent("opencode.session.idle", json.RawMessage(`{}`)) {
		t.Fatal("idle hook not handled")
	}
	got = collectState(t, events, 1, time.Second)
	if got[0] != monitor.StateIdle {
		t.Fatalf("after idle = %v", got[0])
	}

	// Duplicate idle must not emit again.
	if !h.HandleHookEvent("opencode.session.idle", json.RawMessage(`{}`)) {
		t.Fatal("idle hook not handled")
	}
	select {
	case ev := <-events:
		t.Fatalf("duplicate idle emitted %#v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	if !h.HandleHookEvent("opencode.session.active", json.RawMessage(`{}`)) {
		t.Fatal("active hook not handled")
	}
	got = collectState(t, events, 1, time.Second)
	if got[0] != monitor.StateActive {
		t.Fatalf("after active = %v", got[0])
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start did not exit")
	}
}

func TestHandleInterrupt_ForcesIdle(t *testing.T) {
	h := New(isolatedRC(t), nil)
	_, _ = h.PrepareForLaunch(true)
	events := make(chan monitor.AgentEvent, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Start(ctx, events) }()
	_ = collectState(t, events, 1, time.Second) // seed Active
	if !h.HandleInterrupt() {
		t.Fatal("HandleInterrupt returned false")
	}
	got := collectState(t, events, 1, time.Second)
	if got[0] != monitor.StateIdle {
		t.Fatalf("interrupt state = %v, want Idle", got[0])
	}
}

func collectState(t *testing.T, events <-chan monitor.AgentEvent, n int, wait time.Duration) []monitor.State {
	t.Helper()
	deadline := time.After(wait)
	var out []monitor.State
	for len(out) < n {
		select {
		case ev := <-events:
			if ev.Type != monitor.EventStateChange {
				continue
			}
			d, _ := ev.Data.(monitor.StateChangeData)
			out = append(out, d.State)
		case <-deadline:
			t.Fatalf("timed out waiting for %d state events, got %v", n, out)
		}
	}
	return out
}
