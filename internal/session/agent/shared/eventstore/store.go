// Package eventstore provides durable storage for normalized AgentEvents.
// One JSONL file per session in h2's session directory. The AgentMonitor
// appends every event as it processes it. Peek reads from this store
// instead of parsing agent-native log formats directly.
package eventstore

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"h2/internal/session/agent/monitor"
)

const eventsFileName = "events.jsonl"

// DefaultMaxEventFileBytes caps events.jsonl growth. When an append would push
// the file past this size, the file is rotated: events.jsonl -> events.jsonl.1
// (overwriting any previous .1). At most two generations ever exist, so the
// worst-case on-disk footprint is ~2x the cap regardless of how chatty a
// harness is. This is the hard OOM backstop for the 2026-08-19 incident where
// a misbehaving harness ballooned events.jsonl to ~100MB and OOM-killed the
// agent into a crash loop. It is harness-agnostic by design: every event the
// monitor persists funnels through Append.
const DefaultMaxEventFileBytes = 16 << 20 // 16 MiB

// EventStore provides append/read access to a JSONL file of AgentEvents.
type EventStore struct {
	file     *os.File
	maxBytes int64
}

// Open creates or opens the events.jsonl file in the given session directory.
func Open(sessionDir string) (*EventStore, error) {
	return openWithLimit(sessionDir, DefaultMaxEventFileBytes)
}

// openWithLimit is Open with a configurable rotation cap (tests).
func openWithLimit(sessionDir string, maxBytes int64) (*EventStore, error) {
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, fmt.Errorf("create eventstore dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(sessionDir, eventsFileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open events file: %w", err)
	}
	return &EventStore{file: f, maxBytes: maxBytes}, nil
}

// Append JSON-encodes an AgentEvent and appends it as a single line.
// Before writing it checks the file size against the rotation cap and rotates
// if needed; rotation errors are swallowed (best-effort persistence must never
// block or crash the agent) but the append itself still proceeds.
func (s *EventStore) Append(event monitor.AgentEvent) error {
	env := toEnvelope(event)
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	data = append(data, '\n')

	s.maybeRotate(int64(len(data)))

	_, err = s.file.Write(data)
	return err
}

// maybeRotate rotates events.jsonl to events.jsonl.1 when the current file is
// at/over the cap and the incoming write would grow it further. A single event
// larger than the cap is still written (to the fresh file) rather than dropped:
// the cap bounds total growth over time, not individual records.
func (s *EventStore) maybeRotate(incoming int64) {
	if s.maxBytes <= 0 {
		return // disabled
	}
	info, err := s.file.Stat()
	if err != nil {
		return // best-effort
	}
	if info.Size()+incoming <= s.maxBytes {
		return
	}
	path := s.file.Name()
	// Replace the old generation, then swap current -> .1 and reopen a fresh
	// current. Sequence chosen so a crash between steps leaves at most a
	// duplicated-or-missing rotated file, never a corrupt current file.
	rotated := path + ".1"
	os.Remove(rotated)
	// Close before rename so the fd does not keep writing to the renamed file.
	if err := s.file.Close(); err != nil {
		return
	}
	if err := os.Rename(path, rotated); err != nil {
		// Reopen the original so the store keeps working even though we did
		// not manage to rotate.
		if f, rerr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); rerr == nil {
			s.file = f
		}
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return // next Append's marshal/stat path will surface the problem
	}
	s.file = f
}

// Read reads all events from the file and returns them.
func (s *EventStore) Read() ([]monitor.AgentEvent, error) {
	path := s.file.Name()
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open events for read: %w", err)
	}
	defer f.Close()
	return readEvents(f)
}

// Tail streams new events appended after the current end of file.
// The returned channel is closed when ctx is cancelled.
func (s *EventStore) Tail(ctx context.Context) (<-chan monitor.AgentEvent, error) {
	path := s.file.Name()
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open events for tail: %w", err)
	}

	// Seek to end so we only get new events.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, fmt.Errorf("seek to end: %w", err)
	}

	ch := make(chan monitor.AgentEvent, 64)
	go func() {
		defer f.Close()
		defer close(ch)
		reader := bufio.NewReader(f)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		var partial []byte
		for {
			// Try to read all available lines.
			for {
				line, err := reader.ReadBytes('\n')
				if err != nil {
					// Partial data (no trailing newline yet) — accumulate.
					partial = append(partial, line...)
					break
				}
				if len(partial) > 0 {
					line = append(partial, line...)
					partial = nil
				}
				ev, err := parseEvent(line)
				if err != nil {
					continue // skip malformed lines
				}
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
			// Wait for more data or cancellation.
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// Close closes the underlying file.
func (s *EventStore) Close() error {
	return s.file.Close()
}

// ReadEventsFile reads all events from events.jsonl in the given session directory.
// This is a standalone function that does not require an open EventStore.
func ReadEventsFile(sessionDir string) ([]monitor.AgentEvent, error) {
	f, err := os.Open(filepath.Join(sessionDir, eventsFileName))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readEvents(f)
}

// --- Wire format ---

// eventEnvelope is the JSON representation of an AgentEvent on disk.
type eventEnvelope struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// eventTypeToString maps AgentEventType to its string representation.
var eventTypeToString = map[monitor.AgentEventType]string{
	monitor.EventSessionStarted:     "session_started",
	monitor.EventUserPrompt:         "user_prompt",
	monitor.EventTurnCompleted:      "turn_completed",
	monitor.EventToolStarted:        "tool_started",
	monitor.EventToolCompleted:      "tool_completed",
	monitor.EventApprovalRequested:  "approval_requested",
	monitor.EventAgentMessage:       "agent_message",
	monitor.EventStateChange:        "state_change",
	monitor.EventSessionEnded:       "session_ended",
	monitor.EventUsageLimitInfo:     "usage_limit_info",
	monitor.EventPermissionDecision: "permission_decision",
	monitor.EventSessionRotated:     "session_rotated",
	monitor.EventSessionRestarted:   "session_restarted",
}

// stringToEventType maps string to AgentEventType.
var stringToEventType map[string]monitor.AgentEventType

func init() {
	stringToEventType = make(map[string]monitor.AgentEventType, len(eventTypeToString))
	for k, v := range eventTypeToString {
		stringToEventType[v] = k
	}
}

var stringToState = map[string]monitor.State{
	"initialized": monitor.StateInitialized,
	"active":      monitor.StateActive,
	"idle":        monitor.StateIdle,
	"exited":      monitor.StateExited,
}

var stringToSubState = map[string]monitor.SubState{
	"":                       monitor.SubStateNone,
	"thinking":               monitor.SubStateThinking,
	"tool_use":               monitor.SubStateToolUse,
	"permission_review":      monitor.SubStatePermissionReview,
	"waiting_for_permission": monitor.SubStatePermissionReview, // legacy compat
	"compacting":             monitor.SubStateCompacting,
	"usage_limit":            monitor.SubStateUsageLimit,
	"blocked_on_permission":  monitor.SubStateBlockedOnPermission,
}

type stateChangeLogData struct {
	State    string `json:"state"`
	SubState string `json:"sub_state"`
}

func toEnvelope(ev monitor.AgentEvent) eventEnvelope {
	env := eventEnvelope{
		Type:      eventTypeToString[ev.Type],
		Timestamp: ev.Timestamp,
	}
	if ev.Data != nil {
		dataValue := ev.Data
		if ev.Type == monitor.EventStateChange {
			if d, ok := ev.Data.(monitor.StateChangeData); ok {
				dataValue = stateChangeLogData{
					State:    d.State.String(),
					SubState: d.SubState.String(),
				}
			}
		}
		data, _ := json.Marshal(dataValue)
		env.Data = data
	}
	return env
}

func parseEvent(line []byte) (monitor.AgentEvent, error) {
	var env eventEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return monitor.AgentEvent{}, err
	}

	evType, ok := stringToEventType[env.Type]
	if !ok {
		return monitor.AgentEvent{}, fmt.Errorf("unknown event type: %s", env.Type)
	}

	ev := monitor.AgentEvent{
		Type:      evType,
		Timestamp: env.Timestamp,
	}

	if len(env.Data) > 0 {
		data, err := unmarshalData(evType, env.Data)
		if err != nil {
			return monitor.AgentEvent{}, fmt.Errorf("unmarshal data for %s: %w", env.Type, err)
		}
		ev.Data = data
	}

	return ev, nil
}

func unmarshalData(evType monitor.AgentEventType, raw json.RawMessage) (any, error) {
	switch evType {
	case monitor.EventSessionStarted:
		var d monitor.SessionStartedData
		return d, json.Unmarshal(raw, &d)
	case monitor.EventUserPrompt:
		return nil, nil
	case monitor.EventTurnCompleted:
		var d monitor.TurnCompletedData
		return d, json.Unmarshal(raw, &d)
	case monitor.EventToolStarted:
		var d monitor.ToolStartedData
		return d, json.Unmarshal(raw, &d)
	case monitor.EventToolCompleted:
		var d monitor.ToolCompletedData
		return d, json.Unmarshal(raw, &d)
	case monitor.EventApprovalRequested:
		var d monitor.ApprovalRequestedData
		return d, json.Unmarshal(raw, &d)
	case monitor.EventAgentMessage:
		var d monitor.AgentMessageData
		return d, json.Unmarshal(raw, &d)
	case monitor.EventStateChange:
		var wire stateChangeLogData
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, err
		}
		state, ok := stringToState[wire.State]
		if !ok {
			return nil, fmt.Errorf("unknown state: %q", wire.State)
		}
		subState, ok := stringToSubState[wire.SubState]
		if !ok {
			return nil, fmt.Errorf("unknown sub_state: %q", wire.SubState)
		}
		return monitor.StateChangeData{State: state, SubState: subState}, nil
	case monitor.EventSessionEnded:
		var d monitor.SessionEndedData
		return d, json.Unmarshal(raw, &d)
	case monitor.EventUsageLimitInfo:
		var d monitor.UsageLimitData
		return d, json.Unmarshal(raw, &d)
	case monitor.EventPermissionDecision:
		var d monitor.PermissionDecisionData
		return d, json.Unmarshal(raw, &d)
	case monitor.EventSessionRotated:
		var d monitor.SessionRotatedData
		return d, json.Unmarshal(raw, &d)
	case monitor.EventSessionRestarted:
		var d monitor.SessionRestartedData
		return d, json.Unmarshal(raw, &d)
	default:
		// For event types without a known payload struct, preserve raw JSON.
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		return d, nil
	}
}

func readEvents(r io.Reader) ([]monitor.AgentEvent, error) {
	var events []monitor.AgentEvent
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		ev, err := parseEvent(line)
		if err != nil {
			continue // skip malformed lines
		}
		events = append(events, ev)
	}
	return events, scanner.Err()
}
