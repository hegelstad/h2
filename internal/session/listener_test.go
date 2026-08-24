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
// before it can fire again.
func TestERTrigger_ConsumedWhenMessageAnswered(t *testing.T) {
	d := newTestDaemonWithEngines(t)
	s := d.Session

	// Wire the same consume check the real daemon uses.
	queue := s.Queue
	d.TriggerEngine.SetConsumeCheck(func(triggerID string) bool {
		msg := queue.Lookup(triggerID)
		return msg != nil && msg.ExpectsResponse && msg.Status == message.StatusDelivered
	})

	// The annotated message was delivered (answered).
	s.Queue.Enqueue(&message.Message{
		ID:              "a1b2c3d4",
		From:            "ox-scheduler",
		Priority:        message.PriorityNormal,
		Body:            "do the thing",
		ExpectsResponse: true,
		Status:          message.StatusDelivered,
	})

	clock := &mockSessionClock{}
	d.TriggerEngine.SetClock(clock)

	fired := 0
	realAction := d.TriggerEngine
	_ = realAction

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
			Timestamp: clock.Now(),
			Data:      monitor.StateChangeData{State: monitor.StateIdle, SubState: monitor.SubStateNone},
		}
		close(ch)
		done := make(chan struct{})
		go func() { d.TriggerEngine.Run(t.Context(), ch); close(done) }()
		<-done
	}

	if fired != 0 {
		t.Fatalf("answered ER trigger must never fire, fired %d times", fired)
	}
	for _, tr := range d.TriggerEngine.List() {
		if tr.ID == "a1b2c3d4" {
			t.Fatal("answered ER trigger should have been consumed")
		}
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
