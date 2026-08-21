package opencode

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"h2/internal/session/agent/monitor"
)

func TestMapHookEvent(t *testing.T) {
	tests := []struct {
		name    string
		want    monitor.State
		sub     monitor.SubState
		handled bool
	}{
		{name: "opencode.session.idle", want: monitor.StateIdle, sub: monitor.SubStateNone, handled: true},
		{name: "opencode.session.active", want: monitor.StateActive, sub: monitor.SubStateThinking, handled: true},
		{name: "opencode.message.updated", want: monitor.StateActive, sub: monitor.SubStateThinking, handled: true},
		{name: "opencode.permission.asked", want: monitor.StateActive, sub: monitor.SubStatePermissionReview, handled: true},
		{name: "opencode.session.error", want: monitor.StateIdle, sub: monitor.SubStateServerError, handled: true},
		{name: "", handled: false},
		{name: "UserPromptSubmit", handled: false},
		{name: "opencode.unknown", handled: false},
		{name: "{not-json", handled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, sub, ok := MapHookEvent(tt.name)
			if ok != tt.handled {
				t.Fatalf("ok=%v want %v", ok, tt.handled)
			}
			if !tt.handled {
				return
			}
			if got != tt.want || sub != tt.sub {
				t.Errorf("got %v/%v want %v/%v", got, sub, tt.want, tt.sub)
			}
		})
	}
}

func TestHandleHookEvent_IdleNotDroppedWhenBufferFull(t *testing.T) {
	h := New(isolatedRC(t), nil)
	if _, err := h.PrepareForLaunch(true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if !h.HandleHookEvent("opencode.session.active", json.RawMessage(`{}`)) {
			t.Fatal("active not handled")
		}
	}
	if !h.HandleHookEvent("opencode.session.idle", json.RawMessage(`{}`)) {
		t.Fatal("idle not handled")
	}

	events := make(chan monitor.AgentEvent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Start(ctx, events) }()

	got := collectState(t, events, 2, time.Second)
	if got[0] != monitor.StateActive {
		t.Fatalf("seed = %v, want Active", got[0])
	}
	if got[1] != monitor.StateIdle {
		t.Fatalf("after flooded actives idle = %v, want Idle (idle was dropped)", got[1])
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
