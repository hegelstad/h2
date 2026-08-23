package crush

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"h2/internal/session/agent/monitor"
)

// Hook event names emitted by the supervisor shim.
const (
	EventTurnStarted   = "crush.turn.started"
	EventTurnCompleted = "crush.turn.completed"
	EventTurnFailed    = "crush.turn.failed"
)

// turnPayload is the JSON body the supervisor sends with completed/failed
// events (hook_event_name required by h2 handle-hook).
type turnPayload struct {
	HookEventName string `json:"hook_event_name"`
	ExitCode      int    `json:"exit_code,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	Interrupted   bool   `json:"interrupted,omitempty"`
	StderrTail    string `json:"stderr_tail,omitempty"`
}

// eventHandler converts hook envelopes into monitor events. State changes are
// emitted transition-only; Idle and SessionStarted use terminal-style
// emission so a busy monitor channel can never permanently lose them.
type eventHandler struct {
	ch chan monitor.AgentEvent

	// mu guards the fields below: HandleHookEvent is invoked from the
	// listener's RPC goroutine, so concurrent hook deliveries must not race.
	mu           sync.Mutex
	lastState    monitor.State
	lastSubState monitor.SubState
	sessionID    string
}

func newEventHandler() *eventHandler {
	return &eventHandler{
		ch: make(chan monitor.AgentEvent, 256),
	}
}

// handleHook processes one hook event. Returns true if handled.
func (h *eventHandler) handleHook(eventName string, payload json.RawMessage) bool {
	switch eventName {
	case EventTurnStarted:
		h.emitStateTransition(monitor.StateActive, monitor.SubStateThinking)
		return true

	case EventTurnCompleted:
		var p turnPayload
		_ = json.Unmarshal(payload, &p)
		h.captureSessionID(p.SessionID)
		h.emitStateTransition(monitor.StateIdle, monitor.SubStateNone)
		return true

	case EventTurnFailed:
		var p turnPayload
		_ = json.Unmarshal(payload, &p)
		h.emitErrorInfo(&p)
		h.emitStateTransition(monitor.StateIdle, monitor.SubStateNone)
		return true

	default:
		return false
	}
}

// captureSessionID reports a newly captured crush session id exactly once per
// distinct id, as EventSessionStarted — the daemon persists it as
// HarnessSessionID for resume.
func (h *eventHandler) captureSessionID(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if id == "" || id == h.sessionID {
		return
	}
	h.sessionID = id
	h.emitTerminal(monitor.AgentEvent{
		Type:      monitor.EventSessionStarted,
		Timestamp: time.Now(),
		Data:      monitor.SessionStartedData{SessionID: id},
	})
}

// emitErrorInfo maps a failed turn's stderr tail onto the monitor's existing
// error-info events so the status bar shows the cause instead of a silent
// Idle.
func (h *eventHandler) emitErrorInfo(p *turnPayload) {
	msg := strings.TrimSpace(p.StderrTail)
	if msg == "" {
		return
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "payment required") || strings.Contains(lower, "402"):
		h.emitTerminal(monitor.AgentEvent{
			Type:      monitor.EventServerErrorInfo,
			Timestamp: time.Now(),
			Data:      monitor.ServerErrorData{StatusCode: "402", Message: msg},
		})
	case strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "invalid api key") ||
		strings.Contains(lower, "no auth") ||
		strings.Contains(lower, "authentication"):
		h.emitTerminal(monitor.AgentEvent{
			Type:      monitor.EventAuthErrorInfo,
			Timestamp: time.Now(),
			Data:      monitor.AuthErrorData{Message: msg},
		})
	default:
		// Generic failure: surface as server error with the exit code.
		code := ""
		if p.ExitCode != 0 {
			code = strconv.Itoa(p.ExitCode)
		}
		h.emitTerminal(monitor.AgentEvent{
			Type:      monitor.EventServerErrorInfo,
			Timestamp: time.Now(),
			Data:      monitor.ServerErrorData{StatusCode: code, Message: msg},
		})
	}
}

// emitStateTransition emits StateChangeData only when state or substate
// actually changes — the anti-flap guard the design doc commits to. The
// zero-value lastState (StateInitialized) guarantees the very first
// transition always emits.
func (h *eventHandler) emitStateTransition(state monitor.State, sub monitor.SubState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if state == h.lastState && sub == h.lastSubState {
		return
	}
	h.lastState = state
	h.lastSubState = sub
	h.emit(monitor.AgentEvent{
		Type:      monitor.EventStateChange,
		Timestamp: time.Now(),
		Data:      monitor.StateChangeData{State: state, SubState: sub},
	})
}

// emit is lossy under backpressure (non-blocking), matching the claude
// harness: a dropped transient state change is harmless because the
// idle-staleness watchdog backstops.
func (h *eventHandler) emit(ev monitor.AgentEvent) {
	select {
	case h.ch <- ev:
	default:
	}
}

// emitTerminal tries non-blocking, then blocks briefly, then drops with a
// log. Used for Idle/SessionStarted/error-info events that must not be
// permanently missed.
func (h *eventHandler) emitTerminal(ev monitor.AgentEvent) {
	select {
	case h.ch <- ev:
		return
	default:
	}
	select {
	case h.ch <- ev:
		return
	case <-time.After(2 * time.Second):
	}
	select {
	case h.ch <- ev:
	default:
		log.Printf("crush harness: dropped terminal event %v (monitor channel busy)", ev.Type)
	}
}
