package opencode

import (
	"encoding/json"

	"h2/internal/session/agent/monitor"
)

// MapHookEvent translates an opencode.* handle-hook event into a monitor
// state. ok is false for unknown names (not handled).
func MapHookEvent(name string) (want monitor.State, sub monitor.SubState, ok bool) {
	switch name {
	case "opencode.session.idle":
		return monitor.StateIdle, monitor.SubStateNone, true
	case "opencode.session.active", "opencode.message.updated":
		return monitor.StateActive, monitor.SubStateThinking, true
	case "opencode.permission.asked":
		return monitor.StateActive, monitor.SubStatePermissionReview, true
	case "opencode.session.error":
		return monitor.StateIdle, monitor.SubStateServerError, true
	default:
		return 0, 0, false
	}
}

// HandleHookEvent maps plugin events onto the internal state channel.
// Returns true if the name is an opencode.* event we understand.
func (h *OpencodeHarness) HandleHookEvent(eventName string, payload json.RawMessage) bool {
	want, sub, ok := MapHookEvent(eventName)
	if !ok {
		return false
	}
	_ = payload
	h.pushState(want, sub)
	return true
}

func (h *OpencodeHarness) pushState(state monitor.State, sub monitor.SubState) {
	if h.stateCh == nil {
		return
	}
	select {
	case h.stateCh <- stateChange{state: state, sub: sub}:
	default:
	}
}
