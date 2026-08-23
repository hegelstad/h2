package crush

import (
	"encoding/json"
	"testing"

	"h2/internal/session/agent/monitor"
)

func hookPayload(t *testing.T, p turnPayload) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func drainStates(h *eventHandler) []monitor.AgentEvent {
	var out []monitor.AgentEvent
	for {
		select {
		case ev := <-h.ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestHandleHookEvent_StartedActive(t *testing.T) {
	h := newEventHandler()
	if !h.handleHook(EventTurnStarted, hookPayload(t, turnPayload{HookEventName: EventTurnStarted})) {
		t.Fatal("started not handled")
	}
	evs := drainStates(h)
	if len(evs) != 1 || evs[0].Type != monitor.EventStateChange {
		t.Fatalf("want single state change, got %+v", evs)
	}
	sc := evs[0].Data.(monitor.StateChangeData)
	if sc.State != monitor.StateActive || sc.SubState != monitor.SubStateThinking {
		t.Errorf("state = %v/%v, want active/thinking", sc.State, sc.SubState)
	}
}

// The load-bearing invariant: repeated started events must not flap the
// monitor, and completed/failed must land exactly one Idle.
func TestCrush_EmitsOnlyOnTransition(t *testing.T) {
	h := newEventHandler()
	started := hookPayload(t, turnPayload{HookEventName: EventTurnStarted})
	done := hookPayload(t, turnPayload{HookEventName: EventTurnCompleted, SessionID: "s1"})

	h.handleHook(EventTurnStarted, started)
	h.handleHook(EventTurnStarted, started)
	h.handleHook(EventTurnStarted, started)
	h.handleHook(EventTurnCompleted, done)
	h.handleHook(EventTurnCompleted, done)

	evs := drainStates(h)
	states := 0
	idles := 0
	for _, ev := range evs {
		if sc, ok := ev.Data.(monitor.StateChangeData); ok {
			states++
			switch sc.State {
			case monitor.StateActive:
				if idles > 0 {
					t.Error("Active emitted after Idle")
				}
			case monitor.StateIdle:
				idles++
			}
		}
	}
	if states != 2 {
		t.Errorf("want exactly 2 state-change events (active+idle), got %d: %+v", states, evs)
	}
	if idles != 1 {
		t.Errorf("want exactly 1 Idle, got %d", idles)
	}
}

func TestHandleHookEvent_CompletedCapturesSessionID(t *testing.T) {
	h := newEventHandler()
	h.handleHook(EventTurnCompleted, hookPayload(t, turnPayload{
		HookEventName: EventTurnCompleted,
		SessionID:     "sess-abc",
	}))
	h.handleHook(EventTurnCompleted, hookPayload(t, turnPayload{
		HookEventName: EventTurnCompleted,
		SessionID:     "sess-abc",
	}))
	var starts []monitor.AgentEvent
	for _, ev := range drainStates(h) {
		if ev.Type == monitor.EventSessionStarted {
			starts = append(starts, ev)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("want exactly 1 EventSessionStarted, got %d", len(starts))
	}
	if got := starts[0].Data.(monitor.SessionStartedData).SessionID; got != "sess-abc" {
		t.Errorf("session id = %q, want sess-abc", got)
	}
}

func TestHandleHookEvent_402MapsToServerError(t *testing.T) {
	h := newEventHandler()
	h.handleHook(EventTurnFailed, hookPayload(t, turnPayload{
		HookEventName: EventTurnFailed,
		ExitCode:      1,
		StderrTail:    "Agent processing failed: payment required: This request requires more credits, or fewer max_tokens",
	}))
	var serverErrs, authErrs, idles int
	for _, ev := range drainStates(h) {
		switch ev.Type {
		case monitor.EventServerErrorInfo:
			serverErrs++
			d := ev.Data.(monitor.ServerErrorData)
			if d.StatusCode != "402" {
				t.Errorf("status = %q, want 402", d.StatusCode)
			}
		case monitor.EventAuthErrorInfo:
			authErrs++
		case monitor.EventStateChange:
			if ev.Data.(monitor.StateChangeData).State == monitor.StateIdle {
				idles++
			}
		}
	}
	if serverErrs != 1 || authErrs != 0 || idles != 1 {
		t.Errorf("402 mapping: serverErrs=%d authErrs=%d idles=%d, want 1/0/1", serverErrs, authErrs, idles)
	}
}

func TestHandleHookEvent_AuthErrorMapping(t *testing.T) {
	h := newEventHandler()
	h.handleHook(EventTurnFailed, hookPayload(t, turnPayload{
		HookEventName: EventTurnFailed,
		ExitCode:      1,
		StderrTail:    "OpenRouter error: Unauthorized: invalid api key",
	}))
	var authErrs int
	for _, ev := range drainStates(h) {
		if ev.Type == monitor.EventAuthErrorInfo {
			authErrs++
		}
	}
	if authErrs != 1 {
		t.Errorf("want 1 auth error event, got %d", authErrs)
	}
}

func TestHandleHookEvent_UnknownEventNotHandled(t *testing.T) {
	h := newEventHandler()
	if h.handleHook("some.other.event", json.RawMessage(`{"hook_event_name":"some.other.event"}`)) {
		t.Error("unknown event reported as handled")
	}
}
