package grok

import (
	"encoding/json"
	"log"
	"time"

	"h2/internal/activitylog"
	"h2/internal/session/agent/monitor"
)

// terminalEmitTimeout is how long emitTerminal blocks when the events channel
// is full before giving up (last-resort non-blocking attempt). Mirrors the
// claude harness so critical Idle transitions are never dropped.
const terminalEmitTimeout = 2 * time.Second

// EventHandler translates Grok Build lifecycle-hook events into normalized
// AgentEvents. It is the grok counterpart of the claude harness's
// EventHandler: hooks are the ONLY turn-done source (no output-silence and no
// screen-content scraping).
//
// Grok's hook envelope differs from Claude's in the ways that matter here:
//   - envelope keys are camelCase ("hookEventName", "sessionId") and event
//     values are snake_case ("stop", "user_prompt_submit");
//   - Stop fires MORE than once per turn: on every continuation round of a
//     gated turn AND once more at session teardown. Only reason=="end_turn"
//     means the turn genuinely finished; "channel_closed"/"shutdown" are
//     teardown noise and must NOT settle the session to idle mid-work;
//   - interrupts fire StopCancelled (not Stop) and API errors fire
//     StopFailure. Both mean the turn IS over, so both settle to Idle;
//   - a subagent's turn end carries subagentType and is not the session's idle.
type EventHandler struct {
	events            chan<- monitor.AgentEvent
	activityLog       *activitylog.Logger
	expectedSessionID string
}

// NewEventHandler creates an EventHandler that emits events on the given channel.
// An optional activityLog argument may be provided; if absent or nil, a no-op
// logger is used. This variadic form lets call sites that do not need activity
// logging omit the second argument entirely.
func NewEventHandler(events chan<- monitor.AgentEvent, activityLog ...*activitylog.Logger) *EventHandler {
	var alog *activitylog.Logger
	if len(activityLog) > 0 {
		alog = activityLog[0]
	}
	if alog == nil {
		alog = activitylog.Nop()
	}
	return &EventHandler{events: events, activityLog: alog}
}

// SetExpectedSessionID sets the parent session ID for hook event filtering,
// mirroring the claude harness: hook events carrying a different non-empty
// sessionId belong to another concurrent session sharing GROK_HOME and are
// ignored for state emission.
func (h *EventHandler) SetExpectedSessionID(sessionID string) {
	h.expectedSessionID = sessionID
}

// grokHookEnvelope holds the Grok hook stdin fields h2 keys on. Grok uses
// camelCase keys (Claude uses snake_case), so this parser is grok-specific.
type grokHookEnvelope struct {
	HookEventName    string `json:"hookEventName"`
	SessionID        string `json:"sessionId"`
	Reason           string `json:"reason"`
	StopHookActive   bool   `json:"stopHookActive"`
	Error            string `json:"error"`
	SubagentType     string `json:"subagentType"`
	NotificationType string `json:"notificationType"`
}

func parseGrokHookEnvelope(payload json.RawMessage) grokHookEnvelope {
	var env grokHookEnvelope
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &env)
	}
	return env
}

// ProcessHookEvent translates one Grok hook event into AgentEvents. Returns
// true when eventName is a known Grok hook event (even if it produced no state
// change), false otherwise — the same contract as HandleHookEvent.
//
// The eventName argument is the snake_case value from handle-hook forwarding;
// the payload's own hookEventName field agrees with it.
func (h *EventHandler) ProcessHookEvent(eventName string, payload json.RawMessage) bool {
	env := parseGrokHookEnvelope(payload)
	now := time.Now()

	h.activityLog.HookEvent(env.SessionID, eventName, "")

	// Session filtering mirrors the claude harness: only settle/activate on our
	// own session's hooks. If no session ID is expected yet, adopt the first
	// one we see (grok restarts its session ID under the same GROK_HOME; a
	// fixed expected ID would strand state forever). Once set, foreign-session
	// hooks are ignored for state emission.
	if env.SessionID != "" && (h.expectedSessionID == "" || h.expectedSessionID == env.SessionID) {
		h.resyncExpectedSessionID(env.SessionID, eventName)
	} else if h.shouldIgnoreHookSession(env.SessionID) {
		log.Printf(
			"h2: ignoring grok hook %q due to sessionId mismatch: got %q, expected %q",
			eventName, env.SessionID, h.expectedSessionID,
		)
		return true
	}

	switch eventName {
	case "user_prompt_submit":
		h.emit(monitor.AgentEvent{Type: monitor.EventUserPrompt, Timestamp: now})
		h.emitStateChange(now, monitor.StateActive, monitor.SubStateThinking)

	case "pre_tool_use":
		h.emit(monitor.AgentEvent{
			Type:      monitor.EventToolStarted,
			Timestamp: now,
			Data:      monitor.ToolStartedData{ToolName: env.toolName(payload)},
		})
		h.emitStateChange(now, monitor.StateActive, monitor.SubStateToolUse)

	case "post_tool_use":
		h.emit(monitor.AgentEvent{
			Type:      monitor.EventToolCompleted,
			Timestamp: now,
			Data:      monitor.ToolCompletedData{ToolName: env.toolName(payload), Success: true},
		})
		h.emitStateChange(now, monitor.StateActive, monitor.SubStateThinking)

	case "post_tool_use_failure":
		h.emit(monitor.AgentEvent{
			Type:      monitor.EventToolCompleted,
			Timestamp: now,
			Data:      monitor.ToolCompletedData{ToolName: env.toolName(payload), Success: false},
		})
		h.emitStateChange(now, monitor.StateActive, monitor.SubStateThinking)

	case "pre_compact":
		h.emitStateChange(now, monitor.StateActive, monitor.SubStateCompacting)

	case "session_start":
		// Non-lossy: ready-for-delivery after startup must not be dropped.
		h.emitStateChangeTerminal(now, monitor.StateIdle, monitor.SubStateNone)

	case "stop":
		// Stop fires on every continuation round of a gated turn (those carry
		// stopHookActive=true) and again at session teardown
		// (reason=channel_closed/shutdown). Only a genuine completion settles
		// to idle; see the type doc for why this gating is load-bearing.
		if env.Reason != "" && env.Reason != stopReasonEndTurn {
			return true // teardown noise: known but ignored
		}
		if env.StopHookActive {
			return true // continuation round: the turn is still running
		}
		// Non-lossy: a dropped Idle leaves IsIdle false and withholds messages.
		h.emitStateChangeTerminal(now, monitor.StateIdle, monitor.SubStateNone)

	case "stop_cancelled", "stop_failure":
		// Interrupted or failed turns ARE over — settle so delivery resumes.
		h.emitStateChangeTerminal(now, monitor.StateIdle, monitor.SubStateNone)

	case "notification":
		// Registered with matcher idle_prompt; guard the field too in case a
		// broader Notification ever reaches us.
		if env.NotificationType != "" && env.NotificationType != "idle_prompt" {
			return true
		}
		h.emitStateChangeTerminal(now, monitor.StateIdle, monitor.SubStateNone)

	case "subagent_stop", "subagent_start":
		// A subagent's lifecycle is not the session's state.
		return true

	case "session_end":
		h.emitTerminal(monitor.AgentEvent{Type: monitor.EventSessionEnded, Timestamp: now})

	default:
		return false
	}
	return true
}

// stopReasonEndTurn is the only Stop reason that means the turn completed.
const stopReasonEndTurn = "end_turn"

// HandleInterrupt emits the normalized local interrupt transition (Ctrl+C may
// never surface as StopCancelled if it kills the CLI outright).
func (h *EventHandler) HandleInterrupt() bool {
	h.emitStateChangeTerminal(time.Now(), monitor.StateIdle, monitor.SubStateNone)
	return true
}

func (h *EventHandler) resyncExpectedSessionID(sessionID, reason string) {
	if sessionID == "" || sessionID == h.expectedSessionID {
		return
	}
	if h.expectedSessionID != "" {
		log.Printf(
			"h2: resyncing grok expectedSessionID from %q to %q (reason=%s)",
			h.expectedSessionID, sessionID, reason,
		)
	}
	h.expectedSessionID = sessionID
}

func (h *EventHandler) shouldIgnoreHookSession(sessionID string) bool {
	if h.expectedSessionID == "" || sessionID == "" {
		return false
	}
	return sessionID != h.expectedSessionID
}

func (h *EventHandler) emit(ev monitor.AgentEvent) {
	select {
	case h.events <- ev:
	default:
	}
}

func (h *EventHandler) emitStateChange(ts time.Time, state monitor.State, subState monitor.SubState) {
	h.emit(monitor.AgentEvent{
		Type:      monitor.EventStateChange,
		Timestamp: ts,
		Data:      monitor.StateChangeData{State: state, SubState: subState},
	})
}

func (h *EventHandler) emitStateChangeTerminal(ts time.Time, state monitor.State, subState monitor.SubState) {
	h.emitTerminal(monitor.AgentEvent{
		Type:      monitor.EventStateChange,
		Timestamp: ts,
		Data:      monitor.StateChangeData{State: state, SubState: subState},
	})
}

// emitTerminal tries non-blocking first, then blocks up to terminalEmitTimeout
// so critical Idle transitions are not dropped when the channel is full.
func (h *EventHandler) emitTerminal(ev monitor.AgentEvent) {
	select {
	case h.events <- ev:
		return
	default:
	}
	timer := time.NewTimer(terminalEmitTimeout)
	defer timer.Stop()
	select {
	case h.events <- ev:
	case <-timer.C:
		select {
		case h.events <- ev:
		default:
			log.Printf("h2: dropped terminal grok agent event type=%v after %s (channel full)", ev.Type, terminalEmitTimeout)
		}
	}
}

// toolName extracts toolName/tool_name (grok uses camelCase; accept either).
func (e grokHookEnvelope) toolName(payload json.RawMessage) string {
	var fields struct {
		ToolName string `json:"toolName"`
		Alt      string `json:"tool_name"`
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &fields)
	}
	if fields.ToolName != "" {
		return fields.ToolName
	}
	return fields.Alt
}
