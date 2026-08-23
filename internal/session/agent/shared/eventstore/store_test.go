package eventstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"h2/internal/session/agent/monitor"
)

func TestOpenCreatesFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// File should exist.
	if s.file == nil {
		t.Fatal("expected file to be non-nil")
	}
}

func TestOpenCreatesDir(t *testing.T) {
	dir := t.TempDir() + "/nested/sessions"
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
}

func TestAppendAndRead_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	now := time.Now().Truncate(time.Millisecond)

	events := []monitor.AgentEvent{
		{
			Type:      monitor.EventSessionStarted,
			Timestamp: now,
			Data:      monitor.SessionStartedData{SessionID: "t1", Model: "claude-4"},
		},
		{
			Type:      monitor.EventTurnCompleted,
			Timestamp: now.Add(time.Second),
			Data: monitor.TurnCompletedData{
				TurnID:       "turn-1",
				InputTokens:  100,
				OutputTokens: 200,
				CachedTokens: 50,
				CostUSD:      0.01,
			},
		},
		{
			Type:      monitor.EventToolCompleted,
			Timestamp: now.Add(2 * time.Second),
			Data: monitor.ToolCompletedData{
				ToolName:   "Bash",
				CallID:     "call-1",
				DurationMs: 500,
				Success:    true,
			},
		},
		{
			Type:      monitor.EventStateChange,
			Timestamp: now.Add(3 * time.Second),
			Data: monitor.StateChangeData{
				State:    monitor.StateActive,
				SubState: monitor.SubStateThinking,
			},
		},
	}

	for _, ev := range events {
		if err := s.Append(ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := s.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(got) != len(events) {
		t.Fatalf("expected %d events, got %d", len(events), len(got))
	}

	// Check types and timestamps.
	for i, ev := range events {
		if got[i].Type != ev.Type {
			t.Errorf("event %d: type = %v, want %v", i, got[i].Type, ev.Type)
		}
		if !got[i].Timestamp.Equal(ev.Timestamp) {
			t.Errorf("event %d: timestamp = %v, want %v", i, got[i].Timestamp, ev.Timestamp)
		}
	}

	// Check specific payloads.
	sess := got[0].Data.(monitor.SessionStartedData)
	if sess.SessionID != "t1" || sess.Model != "claude-4" {
		t.Errorf("SessionStartedData = %+v, want SessionID=t1, Model=claude-4", sess)
	}

	turn := got[1].Data.(monitor.TurnCompletedData)
	if turn.InputTokens != 100 || turn.OutputTokens != 200 {
		t.Errorf("TurnCompletedData = %+v", turn)
	}

	tool := got[2].Data.(monitor.ToolCompletedData)
	if tool.ToolName != "Bash" || !tool.Success {
		t.Errorf("ToolCompletedData = %+v", tool)
	}

	state := got[3].Data.(monitor.StateChangeData)
	if state.State != monitor.StateActive || state.SubState != monitor.SubStateThinking {
		t.Errorf("StateChangeData = %+v", state)
	}
}

func TestAppendAndRead_NoData(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ev := monitor.AgentEvent{
		Type:      monitor.EventSessionEnded,
		Timestamp: time.Now().Truncate(time.Millisecond),
	}
	if err := s.Append(ev); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].Type != monitor.EventSessionEnded {
		t.Errorf("type = %v, want EventSessionEnded", got[0].Type)
	}
}

func TestRoundTrip_PermissionSubStates(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	now := time.Now().Truncate(time.Millisecond)
	events := []monitor.AgentEvent{
		{
			Type:      monitor.EventStateChange,
			Timestamp: now,
			Data:      monitor.StateChangeData{State: monitor.StateActive, SubState: monitor.SubStatePermissionReview},
		},
		{
			Type:      monitor.EventStateChange,
			Timestamp: now.Add(time.Second),
			Data:      monitor.StateChangeData{State: monitor.StateActive, SubState: monitor.SubStateBlockedOnPermission},
		},
	}

	for _, ev := range events {
		if err := s.Append(ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := s.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}

	sc0 := got[0].Data.(monitor.StateChangeData)
	if sc0.SubState != monitor.SubStatePermissionReview {
		t.Errorf("event 0: SubState = %v, want PermissionReview", sc0.SubState)
	}

	sc1 := got[1].Data.(monitor.StateChangeData)
	if sc1.SubState != monitor.SubStateBlockedOnPermission {
		t.Errorf("event 1: SubState = %v, want BlockedOnPermission", sc1.SubState)
	}
}

func TestRoundTrip_LegacyWaitingForPermission(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Simulate a legacy events.jsonl line with "waiting_for_permission".
	legacyLine := []byte(`{"type":"state_change","timestamp":"2026-03-12T18:00:00Z","data":{"state":"active","sub_state":"waiting_for_permission"}}` + "\n")
	if _, err := s.file.Write(legacyLine); err != nil {
		t.Fatalf("write legacy line: %v", err)
	}

	got, err := s.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}

	sc := got[0].Data.(monitor.StateChangeData)
	if sc.SubState != monitor.SubStatePermissionReview {
		t.Errorf("legacy waiting_for_permission should map to PermissionReview, got %v", sc.SubState)
	}
}

func TestReadEmpty(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	got, err := s.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 events, got %d", len(got))
	}
}

func TestTail_StreamsNewEvents(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Write an event before tailing — should NOT be received.
	pre := monitor.AgentEvent{
		Type:      monitor.EventSessionStarted,
		Timestamp: time.Now().Truncate(time.Millisecond),
		Data:      monitor.SessionStartedData{SessionID: "pre", Model: "m"},
	}
	if err := s.Append(pre); err != nil {
		t.Fatalf("Append pre: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := s.Tail(ctx)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}

	// Write a new event after tailing starts.
	post := monitor.AgentEvent{
		Type:      monitor.EventUserPrompt,
		Timestamp: time.Now().Truncate(time.Millisecond),
	}
	// Small delay to let the tail goroutine start.
	time.Sleep(50 * time.Millisecond)
	if err := s.Append(post); err != nil {
		t.Fatalf("Append post: %v", err)
	}

	// Should receive the post event.
	select {
	case ev := <-ch:
		if ev.Type != monitor.EventUserPrompt {
			t.Errorf("type = %v, want EventUserPrompt", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tail event")
	}

	// Cancel and verify channel closes.
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			// Might get one more event, drain it.
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel close")
	}
}

func TestTail_CancelStopsImmediately(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := s.Tail(ctx)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}

	cancel()

	// Channel should close promptly.
	select {
	case <-ch:
		// ok, closed
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel close after cancel")
	}
}

func TestTail_PartialLineHandling(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := s.Tail(ctx)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}

	// Build a complete JSON line, then split it to simulate partial writes.
	ev := monitor.AgentEvent{
		Type:      monitor.EventUserPrompt,
		Timestamp: time.Now().Truncate(time.Millisecond),
	}
	env := toEnvelope(ev)
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	full := append(data, '\n')

	// Write the first half without a newline.
	half := len(full) / 2
	f, err := os.OpenFile(s.file.Name(), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for partial write: %v", err)
	}
	if _, err := f.Write(full[:half]); err != nil {
		t.Fatalf("write first half: %v", err)
	}
	f.Close()

	// Let the tail goroutine poll and see the partial data.
	time.Sleep(200 * time.Millisecond)

	// Verify nothing arrived yet (partial line, no newline).
	select {
	case got := <-ch:
		t.Fatalf("expected no event from partial line, got %v", got.Type)
	default:
	}

	// Write the second half (includes the newline).
	f, err = os.OpenFile(s.file.Name(), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for rest write: %v", err)
	}
	if _, err := f.Write(full[half:]); err != nil {
		t.Fatalf("write second half: %v", err)
	}
	f.Close()

	// Should now receive the complete event.
	select {
	case got := <-ch:
		if got.Type != monitor.EventUserPrompt {
			t.Errorf("type = %v, want EventUserPrompt", got.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event from reassembled partial line")
	}
}

func TestClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Double close should return an error (file already closed).
	if err := s.Close(); err == nil {
		t.Error("expected error on double Close")
	}
}

// TestRotation_CapsFileGrowth is the OOM-backstop regression test from the
// 2026-08-19 incident: a pathological emitter (a harness that signals output
// many times a second, flooding state_change events) must not be able to grow
// events.jsonl without bound. With rotation enabled at a small cap, a long
// burst of appends keeps the on-disk footprint bounded to roughly 2x the cap
// (current file + one rotated generation), and the store keeps working.
func TestRotation_CapsFileGrowth(t *testing.T) {
	dir := t.TempDir()
	const cap = 4 << 10 // 4 KiB — small so the test stays fast
	s, err := openWithLimit(dir, cap)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ev := monitor.AgentEvent{
		Type:      monitor.EventStateChange,
		Timestamp: time.Now(),
		Data:      monitor.StateChangeData(monitor.StateUpdate{State: monitor.StateActive}),
	}
	payload, err := json.Marshal(toEnvelope(ev))
	if err != nil {
		t.Fatalf("marshal probe event: %v", err)
	}
	lineLen := int64(len(payload) + 1)

	// Far more bytes than the cap: with no guard this would be unbounded.
	const bursts = 2000
	for i := 0; i < bursts; i++ {
		if err := s.Append(ev); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	total := int64(0)
	for _, name := range []string{"events.jsonl", "events.jsonl.1"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Errorf("%s exists but is empty", name)
		}
		total += info.Size()
	}

	// Hard bound: two generations of at most (cap) each. The current file can
	// hold up to cap bytes and .1 held up to cap bytes when rotated.
	if total > 2*cap+lineLen {
		t.Fatalf("total events.jsonl footprint %d bytes exceeds hard bound %d — OOM backstop failed", total, 2*cap+lineLen)
	}
	// The rotated generation must exist after this many appends.
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl.1")); err != nil {
		t.Fatalf("expected rotated events.jsonl.1 to exist: %v", err)
	}
	// The current file must still be valid JSONL readable by Read().
	events, err := s.Read()
	if err != nil {
		t.Fatalf("Read after rotations: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("current events.jsonl empty after rotations")
	}
	for _, e := range events {
		if e.Type != monitor.EventStateChange {
			t.Errorf("unexpected event type %v in current file", e.Type)
		}
	}
}

// TestRotation_DisabledWithZeroCap pins the escape hatch: maxBytes <= 0 means
// no rotation (legacy behavior).
func TestRotation_DisabledWithZeroCap(t *testing.T) {
	dir := t.TempDir()
	s, err := openWithLimit(dir, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ev := monitor.AgentEvent{Type: monitor.EventUserPrompt, Timestamp: time.Now()}
	for i := 0; i < 100; i++ {
		if err := s.Append(ev); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl.1")); !os.IsNotExist(err) {
		t.Fatalf("rotation happened despite disabled cap (err=%v)", err)
	}
}

// TestRotation_OversizedSingleEventStillWritten pins that one huge event is
// not dropped: it goes into the fresh post-rotation file even though it alone
// exceeds the cap.
func TestRotation_OversizedSingleEventStillWritten(t *testing.T) {
	dir := t.TempDir()
	s, err := openWithLimit(dir, 64)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	big := monitor.AgentEvent{
		Type:      monitor.EventAgentMessage,
		Timestamp: time.Now(),
		Data:      monitor.AgentMessageData{Content: strings.Repeat("x", 512)},
	}
	if err := s.Append(big); err != nil {
		t.Fatalf("Append oversized: %v", err)
	}
	events, err := s.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("oversized event lost: got %d events", len(events))
	}
}
