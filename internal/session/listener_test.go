package session

import (
	"net"
	"testing"
	"time"

	"h2/internal/automation"
	"h2/internal/config"
	"h2/internal/session/agent/monitor"
	"h2/internal/session/message"
	"h2/internal/session/virtualterminal"
)

func TestHandleStop_SetsQuitAndRespondsOK(t *testing.T) {
	s := NewFromConfig(&config.RuntimeConfig{
		AgentName:   "test",
		Command:     "true",
		HarnessType: "generic",
		SessionID:   "test-uuid",
		CWD:         "/tmp",
		StartedAt:   "2024-01-01T00:00:00Z",
	})
	s.VT = &virtualterminal.VT{} // minimal VT, no child process

	d := &Daemon{Session: s}

	// Create a connected socket pair.
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	// Run handleStop in background.
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.handleStop(server)
	}()

	// Read the response from the client side.
	resp, err := message.ReadResponse(client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !resp.OK {
		t.Errorf("expected OK response, got error: %s", resp.Error)
	}

	<-done

	if !s.Quit {
		t.Error("expected Session.Quit to be true after stop")
	}
}

func newTestDaemonWithEngines(t *testing.T) *Daemon {
	t.Helper()
	s := NewFromConfig(&config.RuntimeConfig{
		AgentName:   "test",
		Command:     "true",
		HarnessType: "generic",
		SessionID:   "test-uuid",
		CWD:         "/tmp",
		StartedAt:   "2024-01-01T00:00:00Z",
	})
	s.VT = &virtualterminal.VT{}

	runner := automation.NewActionRunner(&noopEnqueuer{}, nil, "")
	return &Daemon{
		Session:        s,
		TriggerEngine:  automation.NewTriggerEngine(runner),
		ScheduleEngine: automation.NewScheduleEngine(runner),
	}
}

type noopEnqueuer struct{}

func (n *noopEnqueuer) EnqueueMessage(string, string, string, message.Priority) (string, error) {
	return "noop-id", nil
}

func TestHandleTriggerAdd_Success(t *testing.T) {
	d := newTestDaemonWithEngines(t)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go d.handleTriggerAdd(server, &message.Request{
		Type: "trigger_add",
		Trigger: &message.TriggerSpec{
			ID:    "t1",
			Event: "state_change",
			State: "idle",
			Exec:  "echo hello",
		},
	})

	resp, err := message.ReadResponse(client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if resp.TriggerID != "t1" {
		t.Errorf("trigger ID = %q, want t1", resp.TriggerID)
	}
}

func TestHandleTriggerAdd_DuplicateID(t *testing.T) {
	d := newTestDaemonWithEngines(t)

	// Add first trigger directly.
	d.TriggerEngine.Add(&automation.Trigger{ID: "t1", Event: "state_change"})

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go d.handleTriggerAdd(server, &message.Request{
		Type: "trigger_add",
		Trigger: &message.TriggerSpec{
			ID:    "t1",
			Event: "state_change",
			Exec:  "echo dup",
		},
	})

	resp, err := message.ReadResponse(client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.OK {
		t.Fatal("expected error for duplicate ID")
	}
}

func TestHandleTriggerList(t *testing.T) {
	d := newTestDaemonWithEngines(t)
	d.TriggerEngine.Add(&automation.Trigger{
		ID: "t1", Name: "test", Event: "state_change",
		Action: automation.Action{Exec: "echo"},
	})

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go d.handleTriggerList(server)

	resp, err := message.ReadResponse(client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if len(resp.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(resp.Triggers))
	}
	if resp.Triggers[0].ID != "t1" {
		t.Errorf("trigger ID = %q, want t1", resp.Triggers[0].ID)
	}
}

func TestHandleTriggerRemove_Success(t *testing.T) {
	d := newTestDaemonWithEngines(t)
	d.TriggerEngine.Add(&automation.Trigger{ID: "t1", Event: "state_change"})

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go d.handleTriggerRemove(server, &message.Request{
		Type:      "trigger_remove",
		TriggerID: "t1",
	})

	resp, err := message.ReadResponse(client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
}

func TestHandleTriggerRemove_NotFound(t *testing.T) {
	d := newTestDaemonWithEngines(t)

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go d.handleTriggerRemove(server, &message.Request{
		Type:      "trigger_remove",
		TriggerID: "nonexistent",
	})

	resp, err := message.ReadResponse(client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.OK {
		t.Fatal("expected error for nonexistent trigger")
	}
}

func TestHandleScheduleAdd_Success(t *testing.T) {
	d := newTestDaemonWithEngines(t)

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go d.handleScheduleAdd(server, &message.Request{
		Type: "schedule_add",
		Schedule: &message.ScheduleSpec{
			ID:    "s1",
			RRule: "FREQ=SECONDLY;INTERVAL=30",
			Exec:  "echo hello",
		},
	})

	resp, err := message.ReadResponse(client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if resp.ScheduleID != "s1" {
		t.Errorf("schedule ID = %q, want s1", resp.ScheduleID)
	}
}

func TestHandleScheduleList(t *testing.T) {
	d := newTestDaemonWithEngines(t)
	d.ScheduleEngine.Add(&automation.Schedule{
		ID: "s1", Name: "test", RRule: "FREQ=SECONDLY;INTERVAL=30",
		Action: automation.Action{Exec: "echo"},
	})

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go d.handleScheduleList(server)

	resp, err := message.ReadResponse(client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got error: %s", resp.Error)
	}
	if len(resp.Schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(resp.Schedules))
	}
}

func TestHandleScheduleRemove_NotFound(t *testing.T) {
	d := newTestDaemonWithEngines(t)

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go d.handleScheduleRemove(server, &message.Request{
		Type:       "schedule_remove",
		ScheduleID: "nonexistent",
	})

	resp, err := message.ReadResponse(client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.OK {
		t.Fatal("expected error for nonexistent schedule")
	}
}

func TestHandleTriggerAdd_NilEngine(t *testing.T) {
	d := &Daemon{Session: &Session{}}

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go d.handleTriggerAdd(server, &message.Request{
		Type:    "trigger_add",
		Trigger: &message.TriggerSpec{ID: "t1", Event: "state_change"},
	})

	resp, err := message.ReadResponse(client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.OK {
		t.Fatal("expected error when engine is nil")
	}
}

// TestERTrigger_ConsumedWhenMessageAnswered reproduces the h2ops-epic.1
// pathology: an expects-response reminder trigger keeps re-firing on every
// idle state_change even after the agent answered. The daemon wires a
// consume check so a delivered annotated message consumes the trigger
// before it can fire again. The queue entry is built through the REAL send
// path (PrepareMessage with ExpectsResponse + TriggerID) so the test pins
// the actual uuid-key/TriggerID-field linkage, not an artificial one.
func TestERTrigger_ConsumedWhenMessageAnswered(t *testing.T) {
	d := newTestDaemonWithEngines(t)
	s := d.Session

	// Wire the same consume check the real daemon uses.
	queue := s.Queue
	d.TriggerEngine.SetConsumeCheck(func(triggerID string) bool {
		msg := queue.LookupByTriggerID(triggerID)
		return msg != nil && msg.Status == message.StatusDelivered
	})

	// A message arrives with --expects-response: PrepareMessage stamps
	// TriggerID on a uuid-keyed message. It is then delivered (answered).
	msgID, err := message.PrepareMessage(s.Queue, s.Name(), "ox-scheduler", "do the thing",
		message.PriorityNormal, message.PrepareOpts{
			ExpectsResponse: true,
			TriggerID:       "a1b2c3d4",
		})
	if err != nil {
		t.Fatalf("PrepareMessage: %v", err)
	}
	if msgID == "a1b2c3d4" {
		t.Fatal("precondition: message ID must be a uuid, not the trigger ID")
	}
	ansMsg := queue.Lookup(msgID)
	if ansMsg == nil || !ansMsg.ExpectsResponse || ansMsg.TriggerID != "a1b2c3d4" {
		t.Fatalf("queued message missing ER linkage: %+v", ansMsg)
	}
	now := time.Now()
	ansMsg.Status = message.StatusDelivered
	ansMsg.DeliveredAt = &now

	d.TriggerEngine.SetClock(&mockSessionClock{})

	d.TriggerEngine.Add(&automation.Trigger{
		ID:         "a1b2c3d4",
		Name:       "expects-response-a1b2c3d4",
		Event:      "state_change",
		State:      "idle",
		MaxFirings: -1,
		Action:     automation.Action{Exec: "true"},
	})

	// Agent cycles idle repeatedly — exactly the replay storm pattern.
	for i := 0; i < 5; i++ {
		ch := make(chan monitor.AgentEvent, 1)
		ch <- monitor.AgentEvent{
			Type:      monitor.EventStateChange,
			Timestamp: time.Now(),
			Data:      monitor.StateChangeData{State: monitor.StateIdle, SubState: monitor.SubStateNone},
		}
		close(ch)
		done := make(chan struct{})
		go func() { d.TriggerEngine.Run(t.Context(), ch); close(done) }()
		<-done
	}

	for _, tr := range d.TriggerEngine.List() {
		if tr.ID == "a1b2c3d4" {
			t.Fatal("answered ER trigger should have been consumed")
		}
	}
}

// TestERTrigger_UnansweredStillFires guards against over-consumption: with
// the same real send path but the message NOT yet delivered, the reminder
// must still fire.
func TestERTrigger_UnansweredStillFires(t *testing.T) {
	d := newTestDaemonWithEngines(t)
	s := d.Session

	queue := s.Queue
	d.TriggerEngine.SetConsumeCheck(func(triggerID string) bool {
		msg := queue.LookupByTriggerID(triggerID)
		return msg != nil && msg.Status == message.StatusDelivered
	})

	_, err := message.PrepareMessage(s.Queue, s.Name(), "ox-scheduler", "do the thing",
		message.PriorityNormal, message.PrepareOpts{
			ExpectsResponse: true,
			TriggerID:       "b2c3d4e5",
		})
	if err != nil {
		t.Fatalf("PrepareMessage: %v", err)
	}

	clock := &mockSessionClock{}
	d.TriggerEngine.SetClock(clock)

	d.TriggerEngine.Add(&automation.Trigger{
		ID:         "b2c3d4e5",
		Name:       "expects-response-b2c3d4e5",
		Event:      "state_change",
		State:      "idle",
		MaxFirings: -1,
		Action:     automation.Action{Exec: "true"},
	})

	ch := make(chan monitor.AgentEvent, 1)
	ch <- monitor.AgentEvent{
		Type:      monitor.EventStateChange,
		Timestamp: time.Now(),
		Data:      monitor.StateChangeData{State: monitor.StateIdle, SubState: monitor.SubStateNone},
	}
	close(ch)
	done := make(chan struct{})
	go func() { d.TriggerEngine.Run(t.Context(), ch); close(done) }()
	<-done

	found := false
	for _, tr := range d.TriggerEngine.List() {
		if tr.ID == "b2c3d4e5" && tr.FireCount == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("unanswered ER trigger should still fire exactly once")
	}

}

type mockSessionClock struct{ now time.Time }

func (c *mockSessionClock) Now() time.Time { return time.Now() }
func (c *mockSessionClock) NewTimer(d time.Duration) automation.Timer {
	return &neverTimer{ch: make(chan time.Time)}
}

type neverTimer struct{ ch chan time.Time }

func (t *neverTimer) C() <-chan time.Time        { return t.ch }
func (t *neverTimer) Stop() bool                 { return true }
func (t *neverTimer) Reset(d time.Duration) bool { return true }
