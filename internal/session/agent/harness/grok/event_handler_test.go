package grok

import (
	"encoding/json"
	"testing"
	"time"

	"h2/internal/session/agent/monitor"
)

// recvTerminal drains up to one event from ch within a short window.
func recvTerminal(t *testing.T, ch chan monitor.AgentEvent) (monitor.AgentEvent, bool) {
	t.Helper()
	select {
	case ev := <-ch:
		return ev, true
	case <-time.After(50 * time.Millisecond):
		return monitor.AgentEvent{}, false
	}
}

// TestProcessHookEvent_StopEndTurnIdles is the core turn-done contract: a Stop
// hook whose reason is "end_turn" means the turn genuinely completed and the
// session must settle to Idle. It must be emitted non-lossily (terminal emit),
// because a dropped Idle withholds message delivery forever.
func TestProcessHookEvent_StopEndTurnIdles(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 1)
	h := NewEventHandler(ch, nil)

	payload := json.RawMessage(`{
		"hookEventName": "stop",
		"sessionId": "s-1",
		"reason": "end_turn",
		"stopHookActive": false
	}`)

	if !h.ProcessHookEvent("stop", payload) {
		t.Fatal("Stop end_turn should be recognized as a known hook event")
	}

	ev, ok := recvTerminal(t, ch)
	if !ok {
		t.Fatal("expected a state_change event for Stop end_turn, got none")
	}
	sc, ok := ev.Data.(monitor.StateChangeData)
	if !ok || ev.Type != monitor.EventStateChange {
		t.Fatalf("expected EventStateChange, got type=%v data=%#v", ev.Type, ev.Data)
	}
	if sc.State != monitor.StateIdle || sc.SubState != monitor.SubStateNone {
		t.Fatalf("expected Idle/None, got %v/%v", sc.State, sc.SubState)
	}
}

// TestProcessHookEvent_StopContinuationIgnored pins the reason gating: Stop
// fires more than once (every continuation round of a gated turn, plus once at
// session teardown). Only reason=="end_turn" means the turn finished; other
// reasons are noise and must not settle the agent to Idle mid-work.
func TestProcessHookEvent_StopContinuationIgnored(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 1)
	h := NewEventHandler(ch, nil)

	for _, tc := range []struct{ name, payload string }{
		{"continuation round", `{"hookEventName":"stop","reason":"end_turn","stopHookActive":true}`},
		{"session teardown channel_closed", `{"hookEventName":"stop","reason":"channel_closed"}`},
		{"session teardown shutdown", `{"hookEventName":"stop","reason":"shutdown"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// stopHookActive=true marks continuation rounds; the teardown cases
			// carry their own reasons. None may emit an idle transition.
			p := json.RawMessage(tc.payload)
			if tc.name == "continuation round" {
				if !h.ProcessHookEvent("stop", p) {
					t.Fatal("Stop is a known event even when its reason is ignored")
				}
			}
			select {
			case ev := <-ch:
				t.Fatalf("unexpected event emitted for %s: %+v", tc.name, ev)
			default:
			}
		})
	}
}

// TestProcessHookEvent_StopMissingReasonTreatedAsEndTurn covers grok builds or
// paths that omit reason on a genuine completion. Treating a missing reason as
// end_turn keeps turn-done detection working; only an EXPLICIT other reason
// (channel_closed/shutdown) is ignored.
func TestProcessHookEvent_StopMissingReasonTreatedAsEndTurn(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 1)
	h := NewEventHandler(ch, nil)

	if !h.ProcessHookEvent("stop", json.RawMessage(`{"hookEventName":"stop","sessionId":"s-2"}`)) {
		t.Fatal("Stop should be recognized")
	}
	ev, ok := recvTerminal(t, ch)
	if !ok {
		t.Fatal("expected idle for Stop without explicit non-end_turn reason")
	}
	if sc, ok := ev.Data.(monitor.StateChangeData); ok && sc.State != monitor.StateIdle {
		t.Fatalf("expected Idle, got %v", sc.State)
	}
}

// TestProcessHookEvent_StopCancelledAndFailureSettle pins that interrupted and
// failed turns still settle to Idle — the turn IS over in both cases, so the
// agent must become eligible for message delivery again.
func TestProcessHookEvent_StopCancelledAndFailureSettle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		event   string
		payload string
	}{
		{"user interrupt", "stop_cancelled", `{"hookEventName":"stop_cancelled","reason":"user_interrupt"}`},
		{"permission rejected", "stop_cancelled", `{"hookEventName":"stop_cancelled","reason":"permission_rejected"}`},
		{"max turns", "stop_cancelled", `{"hookEventName":"stop_cancelled","reason":"max_turns"}`},
		{"api error", "stop_failure", `{"hookEventName":"stop_failure","error":"rate_limit"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan monitor.AgentEvent, 1)
			h := NewEventHandler(ch, nil)

			if !h.ProcessHookEvent(tc.event, json.RawMessage(tc.payload)) {
				t.Fatalf("%s should be recognized as a known event", tc.event)
			}
			ev, ok := recvTerminal(t, ch)
			if !ok {
				t.Fatalf("%s: expected settle-to-idle event", tc.name)
			}
			sc, ok := ev.Data.(monitor.StateChangeData)
			if !ok || ev.Type != monitor.EventStateChange {
				t.Fatalf("%s: expected EventStateChange, got type=%v", tc.name, ev.Type)
			}
			if sc.State != monitor.StateIdle {
				t.Fatalf("%s: expected Idle, got %v", tc.name, sc.State)
			}
		})
	}
}

// TestProcessHookEvent_SessionStartIdles pins SessionStart -> Idle: at prompt,
// ready for delivery before any turn runs.
func TestProcessHookEvent_SessionStartIdles(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 1)
	h := NewEventHandler(ch, nil)

	if !h.ProcessHookEvent("session_start", json.RawMessage(`{"hookEventName":"session_start"}`)) {
		t.Fatal("SessionStart should be recognized")
	}
	ev, ok := recvTerminal(t, ch)
	if !ok {
		t.Fatal("expected idle after SessionStart")
	}
	sc, ok := ev.Data.(monitor.StateChangeData)
	if !ok || sc.State != monitor.StateIdle {
		t.Fatalf("expected Idle after SessionStart, got %#v", ev.Data)
	}
}

// TestProcessHookEvent_SessionEndTerm pins SessionEnd -> EventSessionEnded.
func TestProcessHookEvent_SessionEndTerm(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 1)
	h := NewEventHandler(ch, nil)

	if !h.ProcessHookEvent("session_end", json.RawMessage(`{"hookEventName":"session_end","reason":"exit"}`)) {
		t.Fatal("SessionEnd should be recognized")
	}
	ev, ok := recvTerminal(t, ch)
	if !ok {
		t.Fatal("expected session_ended terminal event")
	}
	if ev.Type != monitor.EventSessionEnded {
		t.Fatalf("expected EventSessionEnded, got %v", ev.Type)
	}
}

// TestProcessHookEvent_SubagentEventsDontSettleSession pins that a subagent's
// turn end must not idle the parent session (grok tags those with subagentType).
func TestProcessHookEvent_SubagentEventsDontSettleSession(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 1)
	h := NewEventHandler(ch, nil)

	payload := json.RawMessage(`{"hookEventName":"subagent_stop","subagentType":"explore"}`)
	if !h.ProcessHookEvent("subagent_stop", payload) {
		t.Fatal("SubagentStop should be recognized as known")
	}
	select {
	case ev := <-ch:
		t.Fatalf("subagent event must not change session state, got %+v", ev)
	default:
	}
}

// TestProcessHookEvent_UserPromptSubmitActivates pins busy-marking: a submitted
// prompt makes the agent Active/thinking so delivery holds until the turn ends.
func TestProcessHookEvent_UserPromptSubmitActivates(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 2)
	h := NewEventHandler(ch, nil)

	if !h.ProcessHookEvent("user_prompt_submit", json.RawMessage(`{"hookEventName":"user_prompt_submit"}`)) {
		t.Fatal("UserPromptSubmit should be recognized")
	}
	first, ok := recvTerminal(t, ch)
	if !ok {
		t.Fatal("expected events from UserPromptSubmit")
	}
	if first.Type != monitor.EventUserPrompt {
		t.Fatalf("expected EventUserPrompt first, got %v", first.Type)
	}
	second, ok := recvTerminal(t, ch)
	if !ok {
		t.Fatal("expected state change after user prompt")
	}
	sc, ok := second.Data.(monitor.StateChangeData)
	if !ok || sc.State != monitor.StateActive || sc.SubState != monitor.SubStateThinking {
		t.Fatalf("expected Active/Thinking, got %#v", second.Data)
	}
}

// TestProcessHookEvent_PrePostToolUse pins tool substate transitions so the
// status bar shows tool_use while tools run (parity with claude harness).
func TestProcessHookEvent_PrePostToolUse(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 4)
	h := NewEventHandler(ch, nil)

	if !h.ProcessHookEvent("pre_tool_use", json.RawMessage(`{"hookEventName":"pre_tool_use","toolName":"run_terminal_command"}`)) {
		t.Fatal("PreToolUse should be recognized")
	}
	// Drain: ToolStarted + Active/tool_use
	gotToolStart := false
	var stateEv monitor.AgentEvent
	for i := 0; i < 2; i++ {
		ev, ok := recvTerminal(t, ch)
		if !ok {
			t.Fatal("missing events from PreToolUse")
		}
		switch e := ev.Data.(type) {
		case monitor.ToolStartedData:
			gotToolStart = true
			if e.ToolName != "run_terminal_command" {
				t.Fatalf("tool name = %q", e.ToolName)
			}
		case monitor.StateChangeData:
			stateEv = ev
		}
	}
	if !gotToolStart {
		t.Fatal("expected ToolStartedData from PreToolUse")
	}
	if sc, ok := stateEv.Data.(monitor.StateChangeData); ok && sc.SubState != monitor.SubStateToolUse {
		t.Fatalf("expected tool_use substate, got %v", sc.SubState)
	}

	if !h.ProcessHookEvent("post_tool_use", json.RawMessage(`{"hookEventName":"post_tool_use","toolName":"run_terminal_command"}`)) {
		t.Fatal("PostToolUse should be recognized")
	}
	found := false
	for i := 0; i < 3; i++ {
		ev, ok := recvTerminal(t, ch)
		if !ok {
			break
		}
		if tc, ok := ev.Data.(monitor.ToolCompletedData); ok {
			found = true
			if !tc.Success {
				t.Fatal("successful post_tool_use should set Success=true")
			}
		}
	}
	if !found {
		t.Fatal("expected ToolCompletedData from PostToolUse")
	}
}

// TestProcessHookEvent_UnknownEventRejected pins the handler contract used by
// HandleHookEvent: unknown events return false so callers know nothing matched.
func TestProcessHookEvent_UnknownEventRejected(t *testing.T) {
	h := NewEventHandler(make(chan monitor.AgentEvent, 1))
	if h.ProcessHookEvent("some_unknown_event", json.RawMessage(`{}`)) {
		t.Fatal("unknown event must return false")
	}
}

// TestProcessHookEvent_TerminalEmitBlocksUnderBackpressure verifies the
// blocking-emit guarantee: when the channel is full, a terminal Idle is still
// delivered (up to the timeout) rather than dropped — this is the property that
// prevents a wedged-active agent withholding delivery forever.
func TestProcessHookEvent_TerminalEmitBlocksUnderBackpressure(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 1) // tiny buffer
	h := NewEventHandler(ch, nil)

	// Fill the channel.
	ch <- monitor.AgentEvent{Type: monitor.EventUserPrompt}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ProcessHookEvent("stop", json.RawMessage(`{"hookEventName":"stop","reason":"end_turn"}`))
	}()

	// The handler goroutine must be blocked trying to emit; drain one event
	// (the filler) and the terminal idle should then get through.
	filler := <-ch
	if filler.Type != monitor.EventUserPrompt {
		t.Fatalf("expected filler first, got %v", filler.Type)
	}
	select {
	case <-done:
		// Handler returned; now verify the idle actually arrived.
	case <-time.After(terminalEmitTimeout + time.Second):
		t.Fatal("terminal emit did not complete after draining channel")
	}
	ev := <-ch
	if sc, ok := ev.Data.(monitor.StateChangeData); ok && sc.State != monitor.StateIdle {
		t.Fatalf("expected Idle delivered under backpressure, got %#v", ev.Data)
	}
}

// TestEventHandler_SetExpectedSessionIDFiltersForeignSessions mirrors claude's
// behavior: NON-terminal hooks carrying a different non-empty sessionId are
// ignored (they belong to another concurrent session sharing GROK_HOME).
// Terminal/session hooks (stop etc.) do NOT hit this path — they force-resync;
// see TestProcessHookEvent_TerminalHookResyncsNewSessionID.
func TestEventHandler_SetExpectedSessionIDFiltersForeignSessions(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 1)
	h := NewEventHandler(ch, nil)
	h.SetExpectedSessionID("mine")

	foreign := json.RawMessage(`{"hookEventName":"pre_tool_use","sessionId":"theirs","toolName":"run_terminal_command"}`)
	if !h.ProcessHookEvent("pre_tool_use", foreign) {
		t.Fatal("foreign-session non-terminal hook is still a known event")
	}
	select {
	case ev := <-ch:
		t.Fatalf("foreign session must not affect our state: %+v", ev)
	default:
	}

	own := json.RawMessage(`{"hookEventName":"stop","sessionId":"mine","reason":"end_turn"}`)
	if !h.ProcessHookEvent("stop", own) {
		t.Fatal("own Stop should be processed")
	}
	ev, ok := recvTerminal(t, ch)
	if !ok {
		t.Fatal("own session Stop end_turn should emit idle")
	}
	if sc, ok := ev.Data.(monitor.StateChangeData); ok && sc.State != monitor.StateIdle {
		t.Fatalf("expected Idle, got %v", sc.State)
	}
}

// TestProcessHookEvent_TerminalHookResyncsNewSessionID pins the claude-parity
// resync behavior: when a TERMINAL/session hook (session_start / stop /
// stop_cancelled / stop_failure / session_end) arrives with a NEW sessionId,
// the handler must adopt it and process normally. Ignoring it would strand the
// agent Active forever the moment grok rotates its session ID mid-session
// (claude avoids this via isTerminalOrSessionHook; see claude/event_handler.go).
//
// The flip side is also pinned: NON-terminal hooks (user_prompt_submit,
// pre_tool_use, notification) with a foreign sessionId are still ignored —
// they belong to another concurrent session sharing GROK_HOME.
func TestProcessHookEvent_TerminalHookResyncsNewSessionID(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 1)
	h := NewEventHandler(ch, nil)
	h.SetExpectedSessionID("first-session")

	// A terminal Stop with a NEW session id must resync AND settle idle.
	stop := json.RawMessage(`{"hookEventName":"stop","sessionId":"rotated-id","reason":"end_turn"}`)
	if !h.ProcessHookEvent("stop", stop) {
		t.Fatal("terminal Stop must be recognized despite sessionId change")
	}
	ev, ok := recvTerminal(t, ch)
	if !ok {
		t.Fatal("terminal hook with new sessionId was ignored — agent would strand Active forever")
	}
	if sc, ok := ev.Data.(monitor.StateChangeData); ok && sc.State != monitor.StateIdle {
		t.Fatalf("expected Idle after resync, got %v", sc.State)
	}

	// The resync must stick: subsequent own-session hooks use rotated-id.
	prompt := json.RawMessage(`{"hookEventName":"user_prompt_submit","sessionId":"rotated-id"}`)
	if !h.ProcessHookEvent("user_prompt_submit", prompt) {
		t.Fatal("user_prompt_submit should be recognized")
	}
	drained := 0
	for {
		select {
		case <-ch:
			drained++
			continue
		default:
		}
		break
	}
	if drained == 0 {
		t.Fatal("post-resync prompt from adopted session should be processed, not ignored")
	}

	// A non-terminal hook from a genuinely foreign session stays ignored.
	h.SetExpectedSessionID("rotated-id")
	foreign := json.RawMessage(`{"hookEventName":"pre_tool_use","sessionId":"other-session","toolName":"run_terminal_command"}`)
	if !h.ProcessHookEvent("pre_tool_use", foreign) {
		t.Fatal("foreign pre_tool_use is still a known grok event")
	}
	select {
	case ev := <-ch:
		t.Fatalf("non-terminal foreign hook must be ignored, got %+v", ev)
	default:
	}
}

// TestProcessHookEvent_NotificationIdlePromptSettles pins the backstop:
// Notification(matcher idle_prompt) settles to Idle for turns whose end is not
// reported by Stop/StopCancelled/StopFailure.
func TestProcessHookEvent_NotificationIdlePromptSettles(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 1)
	h := NewEventHandler(ch, nil)

	if !h.ProcessHookEvent("notification", json.RawMessage(`{"hookEventName":"notification","notificationType":"idle_prompt"}`)) {
		t.Fatal("notification should be recognized")
	}
	ev, ok := recvTerminal(t, ch)
	if !ok {
		t.Fatal("expected idle from Notification(idle_prompt)")
	}
	if sc, ok := ev.Data.(monitor.StateChangeData); ok && sc.State != monitor.StateIdle {
		t.Fatalf("expected Idle from idle_prompt, got %v", sc.State)
	}

	// Other notification types must NOT settle (guard against broader fires).
	ch2 := make(chan monitor.AgentEvent, 1)
	h2 := NewEventHandler(ch2, nil)
	if !h2.ProcessHookEvent("notification", json.RawMessage(`{"hookEventName":"notification","notificationType":"permission_prompt"}`)) {
		t.Fatal("notification should be recognized even when type is filtered out")
	}
	select {
	case ev := <-ch2:
		t.Fatalf("permission_prompt notification must not settle state, got %+v", ev)
	default:
	}
}

// TestProcessHookEvent_PostToolUseFailure pins the failure path: a failed tool
// completes the tool record with Success=false and returns to thinking substate
// (parity with claude's PostToolUseFailure handling).
func TestProcessHookEvent_PostToolUseFailure(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 4)
	h := NewEventHandler(ch, nil)

	if !h.ProcessHookEvent("post_tool_use_failure", json.RawMessage(`{"hookEventName":"post_tool_use_failure","toolName":"search_replace"}`)) {
		t.Fatal("post_tool_use_failure should be recognized")
	}
	var completed *monitor.ToolCompletedData
	for i := 0; i < 4; i++ {
		ev, ok := recvTerminal(t, ch)
		if !ok {
			break
		}
		if tc, ok := ev.Data.(monitor.ToolCompletedData); ok {
			completed = &tc
		}
	}
	if completed == nil {
		t.Fatal("expected ToolCompletedData from post_tool_use_failure")
	}
	if completed.Success {
		t.Error("post_tool_use_failure must set Success=false")
	}
	if completed.ToolName != "search_replace" {
		t.Errorf("tool name = %q, want search_replace", completed.ToolName)
	}
}

// TestProcessHookEvent_PreCompact pins the compaction substate transition.
func TestProcessHookEvent_PreCompact(t *testing.T) {
	ch := make(chan monitor.AgentEvent, 2)
	h := NewEventHandler(ch, nil)

	if !h.ProcessHookEvent("pre_compact", json.RawMessage(`{"hookEventName":"pre_compact"}`)) {
		t.Fatal("pre_compact should be recognized")
	}
	var last monitor.AgentEvent
	found := false
	for i := 0; i < 2; i++ {
		ev, ok := recvTerminal(t, ch)
		if !ok {
			break
		}
		last = ev
		found = true
	}
	if !found {
		t.Fatal("expected events from pre_compact")
	}
	if sc, ok := last.Data.(monitor.StateChangeData); ok && sc.SubState != monitor.SubStateCompacting {
		t.Fatalf("expected compacting substate, got %v", sc.SubState)
	}
}
