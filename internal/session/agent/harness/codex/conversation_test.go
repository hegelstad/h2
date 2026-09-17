package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"h2/internal/config"
	"h2/internal/session/agent/monitor"
)

const primaryID = "019f3d5f-3dc4-7f01-b42c-d19d98e1d13d"
const helperID = "019f3d5f-3dc4-7f01-b42c-d19d98e1d13e"
const nextID = "019f3d5f-3dc4-7f01-b42c-d19d98e1d13f"

func conversationHarness(t *testing.T, resume string) *CodexHarness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("H2_DIR", home)
	t.Setenv("H2_ROOT_DIR", home)
	config.ResetResolveCache()
	config.CheckTestIsolation()
	t.Cleanup(config.ResetResolveCache)
	h := New(&config.RuntimeConfig{
		HarnessType: "codex", AgentName: "test", Command: "codex", CWD: home,
		HarnessConfigPathPrefix: filepath.Join(home, "codex-config"), Profile: "pilot",
		ResumeSessionID: resume,
	}, nil)
	if _, err := h.PrepareForLaunch(false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	return h
}

func writeConversation(t *testing.T, h *CodexHarness, id string, meta map[string]any) string {
	t.Helper()
	dir := filepath.Join(h.rc.HarnessConfigDir(), "sessions", "2026", "09", "17")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-test-"+id+".jsonl")
	if meta == nil {
		meta = map[string]any{"id": id, "source": "cli", "thread_source": "user"}
	}
	line := append(rolloutLine(t, "session_meta", meta), '\n')
	if err := os.WriteFile(path, line, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func conversationEvent(h *CodexHarness, name, id string, extra ...otelAttribute) {
	attrs := []otelAttribute{{Key: "conversation.id", Value: otelAttrValue{StringValue: id}},
		{Key: "model", Value: otelAttrValue{StringValue: "test-model"}}}
	h.eventHandler.OnLogs(makeLogsPayload(name, append(attrs, extra...)))
}

func TestConversationHelpersCannotReplaceIdentityOrActivity(t *testing.T) {
	h := conversationHarness(t, "")
	writeConversation(t, h, primaryID, nil)
	// A helper may arrive in an earlier HTTP batch than the main thread.
	conversationEvent(h, "codex.conversation_starts", helperID)
	if len(h.internalCh) != 0 {
		t.Fatal("accepted helper before primary")
	}
	conversationEvent(h, "codex.conversation_starts", primaryID)
	got := drainEvents(h.internalCh, 2)
	if got[0].Data.(monitor.SessionStartedData).SessionID != primaryID {
		t.Fatal(got)
	}
	conversationEvent(h, "codex.user_prompt", primaryID)
	drainEvents(h.internalCh, 2)
	for _, name := range []string{"codex.conversation_starts", "codex.user_prompt", "codex.tool_result", "codex.sse_event", "codex.api_request"} {
		conversationEvent(h, name, helperID,
			otelAttribute{Key: "event.kind", Value: otelAttrValue{StringValue: "response.completed"}},
			otelAttribute{Key: "input_token_count", Value: otelAttrValue{IntValue: json.RawMessage("99999")}},
			otelAttribute{Key: "http.response.status_code", Value: otelAttrValue{IntValue: json.RawMessage("429")}})
	}
	time.Sleep(2 * codexIdleDebounceDelay)
	if len(h.internalCh) != 0 {
		t.Fatal("helper emitted primary activity, usage or identity")
	}
	h.eventHandler.stateMu.Lock()
	state := h.eventHandler.currentState
	h.eventHandler.stateMu.Unlock()
	if state != monitor.StateActive {
		t.Fatalf("helper changed primary state to %v", state)
	}
	conversationEvent(h, "codex.sse_event", primaryID,
		otelAttribute{Key: "event.kind", Value: otelAttrValue{StringValue: "response.completed"}},
		otelAttribute{Key: "input_token_count", Value: otelAttrValue{IntValue: json.RawMessage("12")}})
	turn := drainEvents(h.internalCh, 1)[0].Data.(monitor.TurnCompletedData)
	if turn.InputTokens != 12 {
		t.Fatalf("helper polluted tokens: %+v", turn)
	}
	h.eventHandler.cancelPendingIdle()
}

func TestConversationLateRolloutAndRealNewThread(t *testing.T) {
	h := conversationHarness(t, "")
	for _, id := range []string{primaryID, nextID} {
		conversationEvent(h, "codex.conversation_starts", id)
		if len(h.internalCh) != 0 {
			t.Fatal("accepted missing rollout")
		}
		path := writeConversation(t, h, id, nil)
		conversationEvent(h, "codex.user_prompt", id)
		got := drainEvents(h.internalCh, 4)
		if len(got) != 4 || got[0].Data.(monitor.SessionStartedData).SessionID != id {
			t.Fatalf("not recovered: %+v", got)
		}
		if h.sessionLogPath != path {
			t.Fatalf("rollout = %s, want %s", h.sessionLogPath, path)
		}
	}
	if got := <-h.sessionLogPathCh; got != h.sessionLogPath {
		t.Fatal("tailer notification retained old /new path")
	}
}

func TestConversationResumeReattachesPersistedRollout(t *testing.T) {
	h := conversationHarness(t, primaryID)
	path := writeConversation(t, h, primaryID, nil)
	h.rc.NativeLogPathSuffix, _ = filepath.Rel(h.rc.HarnessConfigDir(), path)
	conversationEvent(h, "codex.conversation_starts", helperID)
	if len(h.internalCh) != 0 {
		t.Fatal("resume replaced by helper")
	}
	conversationEvent(h, "codex.conversation_starts", primaryID)
	if len(h.internalCh) != 2 {
		t.Fatal("resume did not start")
	}
	select {
	case got := <-h.sessionLogPathCh:
		if got != path {
			t.Fatal(got)
		}
	default:
		t.Fatal("persisted suffix prevented resume log tailing")
	}
}

func TestConversationRequiresMatchingUserHeader(t *testing.T) {
	h := conversationHarness(t, "")
	for _, meta := range []map[string]any{
		{"id": helperID, "source": "cli", "thread_source": "system"},
		{"id": helperID, "source": map[string]any{"subagent": "review"}},
		{"id": primaryID, "source": "cli"},
		{"id": helperID, "source": "cli", "parent_thread_id": primaryID},
	} {
		writeConversation(t, h, helperID, meta)
		if h.nativeConversationLog(helperID) != "" {
			t.Fatalf("accepted %+v", meta)
		}
	}
	for _, id := range []string{"../*", "*", ""} {
		if h.nativeConversationLog(id) != "" {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
	writeConversation(t, h, primaryID, map[string]any{"session_id": primaryID})
	if h.nativeConversationLog(primaryID) == "" {
		t.Fatal("rejected legacy header")
	}
}

func TestConversationTailerFollowsRealNewThread(t *testing.T) {
	h := conversationHarness(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); h.tailSessionLog(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for _, id := range []string{primaryID, nextID} {
		path := writeConversation(t, h, id, nil)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Write(append(rolloutLine(t, "event_msg", map[string]any{"type": "agent_message", "message": id}), '\n'))
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		conversationEvent(h, "codex.conversation_starts", id)
		deadline := time.After(3 * time.Second)
		for {
			select {
			case ev := <-h.internalCh:
				if ev.Type == monitor.EventAgentMessage {
					if ev.Data.(monitor.AgentMessageData).Content != id {
						t.Fatal("tailed old thread")
					}
					goto next
				}
			case <-deadline:
				t.Fatal("missing new thread message")
			}
		}
	next:
	}
}
