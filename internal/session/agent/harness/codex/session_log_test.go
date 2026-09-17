package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"h2/internal/config"
	"h2/internal/session/agent/monitor"
	"h2/internal/session/agent/shared/sessionlogcollector"
)

// rolloutLine builds one Codex rollout JSONL line with the given top-level
// type and payload.
func rolloutLine(t *testing.T, typ string, payload map[string]any) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"timestamp": "2026-07-07T16:31:01.642Z",
		"type":      typ,
		"payload":   payload,
	})
	if err != nil {
		t.Fatalf("marshal rollout line: %v", err)
	}
	return line
}

func TestEventHandler_OnSessionLogLine_AgentMessage(t *testing.T) {
	events := make(chan monitor.AgentEvent, 8)
	p := NewEventHandler(events)

	line := rolloutLine(t, "event_msg", map[string]any{
		"type":    "agent_message",
		"message": "Confirmed with concierge-leaf.",
		"phase":   "final_answer",
	})
	p.OnSessionLogLine(line)

	got := drainEvents(events, 1)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Type != monitor.EventAgentMessage {
		t.Fatalf("Type = %v, want EventAgentMessage", got[0].Type)
	}
	if c := got[0].Data.(monitor.AgentMessageData).Content; c != "Confirmed with concierge-leaf." {
		t.Fatalf("Content = %q, want the assistant message text", c)
	}
}

// Commentary is interstitial assistant text (preamble before/between tools).
// It is visible conversation, so peek must show it — matching Claude, which
// emits every assistant text block.
func TestEventHandler_OnSessionLogLine_Commentary(t *testing.T) {
	events := make(chan monitor.AgentEvent, 8)
	p := NewEventHandler(events)

	line := rolloutLine(t, "event_msg", map[string]any{
		"type":    "agent_message",
		"message": "Let me check the logs first.",
		"phase":   "commentary",
	})
	p.OnSessionLogLine(line)

	got := drainEvents(events, 1)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if c := got[0].Data.(monitor.AgentMessageData).Content; c != "Let me check the logs first." {
		t.Fatalf("Content = %q, want the commentary text", c)
	}
}

// The user's own prompt (event_msg/user_message) must not be re-emitted as an
// agent message.
func TestEventHandler_OnSessionLogLine_UserMessage_NoEmit(t *testing.T) {
	events := make(chan monitor.AgentEvent, 8)
	p := NewEventHandler(events)

	line := rolloutLine(t, "event_msg", map[string]any{
		"type":    "user_message",
		"message": "please run the tests",
	})
	p.OnSessionLogLine(line)

	if got := drainEvents(events, 1); len(got) != 0 {
		t.Fatalf("got %d events, want 0 for user_message", len(got))
	}
}

// The assistant text also appears as a response_item/message. We parse only the
// flat event_msg/agent_message form, so the response_item form must not emit —
// otherwise every message would be counted twice.
func TestEventHandler_OnSessionLogLine_ResponseItemMessage_NoEmit(t *testing.T) {
	events := make(chan monitor.AgentEvent, 8)
	p := NewEventHandler(events)

	line := rolloutLine(t, "response_item", map[string]any{
		"type": "message",
		"role": "assistant",
		"content": []map[string]any{
			{"type": "output_text", "text": "Confirmed with concierge-leaf."},
		},
		"phase": "final_answer",
	})
	p.OnSessionLogLine(line)

	if got := drainEvents(events, 1); len(got) != 0 {
		t.Fatalf("got %d events, want 0 for response_item message", len(got))
	}
}

func TestEventHandler_OnSessionLogLine_NonMessageEvent_NoEmit(t *testing.T) {
	events := make(chan monitor.AgentEvent, 8)
	p := NewEventHandler(events)

	for _, typ := range []string{"token_count", "task_started", "task_complete", "context_compacted"} {
		line := rolloutLine(t, "event_msg", map[string]any{"type": typ})
		p.OnSessionLogLine(line)
	}

	if got := drainEvents(events, 1); len(got) != 0 {
		t.Fatalf("got %d events, want 0 for non-message event_msgs", len(got))
	}
}

func TestEventHandler_OnSessionLogLine_EmptyMessage_NoEmit(t *testing.T) {
	events := make(chan monitor.AgentEvent, 8)
	p := NewEventHandler(events)

	line := rolloutLine(t, "event_msg", map[string]any{
		"type":    "agent_message",
		"message": "",
		"phase":   "final_answer",
	})
	p.OnSessionLogLine(line)

	if got := drainEvents(events, 1); len(got) != 0 {
		t.Fatalf("got %d events, want 0 for empty message", len(got))
	}
}

func TestEventHandler_OnSessionLogLine_InvalidJSON_NoEmit(t *testing.T) {
	events := make(chan monitor.AgentEvent, 8)
	p := NewEventHandler(events)

	p.OnSessionLogLine([]byte("not json"))

	if got := drainEvents(events, 1); len(got) != 0 {
		t.Fatalf("got %d events, want 0 for invalid json", len(got))
	}
}

// End-to-end: the shared tailer reads a rollout file and the parser emits one
// EventAgentMessage per agent_message, in order, ignoring the surrounding
// session_meta / user_message / token_count noise.
func TestEventHandler_SessionLogCollector_EmitsAgentMessages(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "rollout.jsonl")

	lines := [][]byte{
		rolloutLine(t, "session_meta", map[string]any{"session_id": "conv-1"}),
		rolloutLine(t, "event_msg", map[string]any{"type": "user_message", "message": "hello"}),
		rolloutLine(t, "event_msg", map[string]any{"type": "agent_message", "message": "First reply.", "phase": "commentary"}),
		rolloutLine(t, "event_msg", map[string]any{"type": "token_count"}),
		rolloutLine(t, "event_msg", map[string]any{"type": "agent_message", "message": "Second reply.", "phase": "final_answer"}),
	}
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		f.Write(l)
		f.Write([]byte("\n"))
	}
	f.Close()

	events := make(chan monitor.AgentEvent, 64)
	p := NewEventHandler(events)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sessionlogcollector.New(logPath, p.OnSessionLogLine).Run(ctx)

	got := drainEventsTimeout(events, 2, 2*time.Second)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if c := got[0].Data.(monitor.AgentMessageData).Content; c != "First reply." {
		t.Fatalf("event[0].Content = %q, want 'First reply.'", c)
	}
	if c := got[1].Data.(monitor.AgentMessageData).Content; c != "Second reply." {
		t.Fatalf("event[1].Content = %q, want 'Second reply.'", c)
	}
}

// TestHarness_TailsSessionLogAfterConversationStarts is the full harness wiring
// test: PrepareForLaunch registers the conversation-started callback, a real
// codex.conversation_starts OTEL log arrives, the harness globs the rollout
// file, tails it, and emits the assistant message text as an EventAgentMessage.
func TestHarness_TailsSessionLogAfterConversationStarts(t *testing.T) {
	prefix := t.TempDir()
	configDir := filepath.Join(prefix, "default") // Profile "default"
	convID := "019f3d5f-3dc4-7f01-b42c-d19d98e1d13d"

	// Write the rollout file where the harness's glob will find it:
	//   <configDir>/sessions/<Y>/<M>/<D>/rollout-<ts>-<convID>.jsonl
	logDir := filepath.Join(configDir, "sessions", "2026", "07", "07")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "rollout-2026-07-07T09-17-59-"+convID+".jsonl")
	rollout := [][]byte{
		rolloutLine(t, "session_meta", map[string]any{"session_id": convID}),
		rolloutLine(t, "event_msg", map[string]any{"type": "agent_message", "message": "Task done, all tests pass.", "phase": "final_answer"}),
	}
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range rollout {
		f.Write(l)
		f.Write([]byte("\n"))
	}
	f.Close()

	h := New(&config.RuntimeConfig{
		HarnessType:             "codex",
		Command:                 "codex",
		AgentName:               "test",
		CWD:                     "/tmp",
		StartedAt:               "2024-01-01T00:00:00Z",
		HarnessConfigPathPrefix: prefix,
		Profile:                 "default",
	}, nil)

	if _, err := h.PrepareForLaunch(false); err != nil {
		t.Fatalf("PrepareForLaunch: %v", err)
	}
	defer h.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan monitor.AgentEvent, 64)
	go h.Start(ctx, events)
	time.Sleep(20 * time.Millisecond)

	// Drive the conversation-started event through the real OTEL server.
	postLog(t, fmt.Sprintf("http://127.0.0.1:%d/v1/logs", h.OtelPort()),
		"codex.conversation_starts", []otelAttribute{
			{Key: "conversation.id", Value: otelAttrValue{StringValue: convID}},
			{Key: "model", Value: otelAttrValue{StringValue: "gpt-5-codex"}},
		})

	// The suffix should be discovered from the glob.
	waitFor(t, 2*time.Second, "native log path suffix", func() bool {
		return h.rc.NativeLogPathSuffix != ""
	})

	// The tailer should emit the agent message from the rollout file.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == monitor.EventAgentMessage {
				if c := ev.Data.(monitor.AgentMessageData).Content; c != "Task done, all tests pass." {
					t.Fatalf("Content = %q, want the assistant message text", c)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for EventAgentMessage from the tailed rollout log")
		}
	}
}

func drainEventsTimeout(ch chan monitor.AgentEvent, n int, d time.Duration) []monitor.AgentEvent {
	var events []monitor.AgentEvent
	timeout := time.After(d)
	for len(events) < n {
		select {
		case ev := <-ch:
			events = append(events, ev)
		case <-timeout:
			return events
		}
	}
	return events
}

// Final failure is different from a retryable API 429: the TUI has returned
// to its prompt, so h2 must not keep normal messages queued behind "thinking".
func TestEventHandler_SessionLogTerminalFailure(t *testing.T) {
	for _, code := range []int{429, 401, 503, 0} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			events := make(chan monitor.AgentEvent, 16)
			p := NewEventHandler(events)
			p.OnLogs(makeLogsPayload("codex.user_prompt", nil))
			drainEvents(events, 2)
			p.OnSessionLogLine(rolloutLine(t, "event_msg", map[string]any{
				"type": "task_complete", "error": map[string]any{
					"message": "synthetic terminal failure", "codex_error_info": map[string]any{
						"response_too_many_failed_attempts": map[string]any{"http_status_code": code},
					},
				},
			}))
			got := drainEvents(events, 2)
			if len(got) != 2 {
				t.Fatalf("got %d events, want 2", len(got))
			}
			state := got[0].Data.(monitor.StateChangeData)
			sub, typ := monitor.SubStateServerError, monitor.EventServerErrorInfo
			if code == 429 {
				sub, typ = monitor.SubStateUsageLimit, monitor.EventUsageLimitInfo
			}
			if code == 401 {
				sub, typ = monitor.SubStateAuthError, monitor.EventAuthErrorInfo
			}
			if state.State != monitor.StateIdle || state.SubState != sub || got[1].Type != typ {
				t.Fatalf("unexpected terminal state/events: %+v %+v", state, got)
			}
			if code == 429 && !got[1].Data.(monitor.UsageLimitData).ResetsAt.IsZero() {
				t.Fatal("invented reset time")
			}
			p.OnLogs(makeLogsPayload("codex.sse_event", []otelAttribute{{Key: "event.kind", Value: otelAttrValue{StringValue: "response.created"}}}))
			recovered := drainEvents(events, 1)
			if len(recovered) != 1 || recovered[0].Data.(monitor.StateChangeData).State != monitor.StateActive {
				t.Fatal("not recovered on successful response")
			}
		})
	}
}
