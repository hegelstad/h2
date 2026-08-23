package crush

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultTurnTimeout bounds one crush turn; a hung crush process must not
// wedge the agent indefinitely (the h2 watchdog only backstops state, it
// cannot kill children).
const DefaultTurnTimeout = 30 * time.Minute

// killGrace is how long the child gets to exit after SIGINT before SIGKILL.
const killGrace = 5 * time.Second

// stderrTailMax caps how much stderr rides along in failed-turn hook payloads.
const stderrTailMax = 8 * 1024

// Options configures a Supervisor. Zero values select production defaults;
// tests override CrushBin/HookBin via these fields.
type Supervisor struct {
	// DataDir is passed as --data-dir on every crush command (mandatory:
	// cwd-local .crush dbs collide across agents sharing a workspace).
	DataDir string
	// Host optionally pins an explicit --host socket for every command so a
	// stray box-wide `crush server` can never silently collect our runs.
	Host string
	// ResumeSessionID, when set, is passed as --session on every turn and
	// disables capture of a fresh session id.
	ResumeSessionID string
	// SessionFile persists the captured session id across supervisor runs
	// (empty disables capture).
	SessionFile string
	// Actor is forwarded as --agent to handle-hook (falls back to $H2_ACTOR).
	Actor string
	// TurnTimeout kills an in-flight turn (default DefaultTurnTimeout).
	TurnTimeout time.Duration
	// CrushBin overrides the crush executable (tests stub it).
	CrushBin string
	// HookBin overrides the h2 binary used for handle-hook (defaults to this
	// process's own executable — no PATH dependency).
	HookBin string
}

// Run consumes delivered messages from stdin until EOF or ctx cancellation,
// executing one crush turn per line. The shim must never die from signals:
// h2 interrupts arrive as 0x03 on the PTY, which the line discipline turns
// into SIGINT for the foreground process group — i.e. for THIS process. We
// catch it and forward SIGINT to the child's own process group instead,
// staying alive to report turn.failed and serve the next message.
func Run(ctx context.Context, o Supervisor, stdin io.Reader, stdout, stderr io.Writer) error {
	if o.TurnTimeout <= 0 {
		o.TurnTimeout = DefaultTurnTimeout
	}
	if o.CrushBin == "" {
		o.CrushBin = "crush"
	}
	if o.HookBin == "" {
		if self, err := os.Executable(); err == nil {
			o.HookBin = self
		} else {
			o.HookBin = "h2"
		}
	}
	if o.Actor == "" {
		o.Actor = os.Getenv("H2_ACTOR")
	}

	s := &supervisor{opts: o, stderr: stderr}
	stopSignals := s.handleSignals()
	defer stopSignals()

	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	sc.Split(splitCRorLF)

	for {
		select {
		case <-ctx.Done():
			s.killCurrent("supervisor shutting down")
			return ctx.Err()
		default:
		}
		if !sc.Scan() {
			return nil // EOF: parent PTY closed; exit cleanly.
		}
		line := sc.Text()
		if interrupted(line) {
			// Belt-and-suspenders: if the PTY is in raw mode (no ISIG), a
			// 0x03 byte shows up as input between turns; drop it.
			continue
		}
		prompt, ok := parseDeliveryLine(line, stderr)
		if !ok {
			continue
		}
		// Turn-level failures are reported via hook events; the loop keeps
		// serving whatever comes next.
		_ = s.runTurn(ctx, prompt, stdout)
	}
}

// handleSignals keeps the shim alive on SIGINT/SIGHUP/SIGQUIT (the PTY line
// discipline turns h2's 0x03 interrupt byte into SIGINT for this process) and
// forwards SIGINT to the in-flight child's process group. The child's exit is
// then reported as a normal failed turn by runTurn.
//
// Kill ladder, forwarding order (docs/plans/crush-harness.md §5):
//  1. SIGINT to the child's process group — crush spawns tool subprocesses
//     (bash etc.); the group signal avoids orphans.
//  2. Grace period (killGrace) for the child to exit on its own.
//  3. SIGKILL to the same group if it survived the grace period.
func (s *supervisor) handleSignals() (stop func()) {
	ch := make(chan os.Signal, 8)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-ch:
				s.forwardSignalToChild(sig)
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// forwardSignalToChild applies the kill ladder to the in-flight child, if any.
func (s *supervisor) forwardSignalToChild(sig os.Signal) {
	s.mu.Lock()
	cmd := s.current
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid := -cmd.Process.Pid // negative pid => whole process group
	syscall.Kill(pgid, syscall.SIGINT)
	time.Sleep(killGrace)
	syscall.Kill(pgid, syscall.SIGKILL)
}

type supervisor struct {
	opts      Supervisor
	stderr    io.Writer
	mu        sync.Mutex
	current   *exec.Cmd
	sessionID string
	beforeIDs map[string]bool // session ids present before the first turn
	tookSnap  bool
}

// splitCRorLF splits input on \r or \n (h2 delivers lines terminated by \r).
func splitCRorLF(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// interrupted reports whether the delivered line was a raw Ctrl+C (0x03).
func interrupted(line string) bool {
	return line == "\x03" || line == "^C"
}

// parseDeliveryLine decodes h2's delivery envelope into a prompt body.
// Shapes: "[header] body", "[header] Read /path/to/file.md", or raw text.
// Returns ok=false for empty/non-deliverable lines.
func parseDeliveryLine(line string, stderr io.Writer) (string, bool) {
	line = strings.TrimRight(line, " ")
	if strings.TrimSpace(line) == "" {
		return "", false
	}
	body := line
	if strings.HasPrefix(line, "[") {
		end := strings.Index(line, "]")
		if end < 0 {
			return body, true // not our envelope shape; treat as raw text
		}
		rest := strings.TrimSpace(line[end+1:])
		if rest == "" {
			return "", false
		}
		body = rest
		if path, found := strings.CutPrefix(rest, "Read "); found {
			raw, err := os.ReadFile(strings.TrimSpace(path))
			if err != nil {
				fmt.Fprintf(stderr, "crush-supervisor: read message file %s: %v\n", path, err)
				return "", false
			}
			body = string(raw)
		}
	}
	if strings.TrimSpace(body) == "" {
		return "", false
	}
	return body, true
}

// runTurn executes one crush run for the given prompt and reports the result
// through hook events.
func (s *supervisor) runTurn(ctx context.Context, prompt string, stdout io.Writer) error {
	s.emit(EventTurnStarted, turnPayload{HookEventName: EventTurnStarted})

	args := []string{"run", "-q", "--data-dir", s.opts.DataDir}
	if s.opts.Host != "" {
		args = append(args, "--host", s.opts.Host)
	}
	if sid := s.currentSessionID(); sid != "" {
		args = append(args, "--session", sid)
	}
	cmd := exec.Command(s.opts.CrushBin, args...)
	cmd.Stdin = strings.NewReader(prompt + "\n") // prompt piped: no argv limits
	cmd.Stdout = stdout                          // tee: transcript streams to the PTY.

	errBuf := &limitedBuffer{max: stderrTailMax}
	cmd.Stderr = errBuf
	setChildProcessGroup(cmd)

	s.mu.Lock()
	s.current = cmd
	needSnapshot := s.sessionID == "" && s.opts.ResumeSessionID == ""
	s.mu.Unlock()
	if needSnapshot && s.opts.SessionFile != "" {
		s.snapshotSessionIDs()
	}

	if err := cmd.Start(); err != nil {
		s.fail(-1, fmt.Sprintf("spawn crush: %v", err))
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timeout := time.NewTimer(s.opts.TurnTimeout)
	defer timeout.Stop()

	var waitErr error
	timedOut := false
waitLoop:
	for {
		select {
		case waitErr = <-done:
			break waitLoop
		case <-timeout.C:
			killChildGroup(cmd)
			waitErr = <-done
			timedOut = true
			break waitLoop
		case <-ctx.Done():
			killChildGroup(cmd)
			<-done
			s.mu.Lock()
			s.current = nil
			s.mu.Unlock()
			return ctx.Err()
		}
	}
	s.mu.Lock()
	s.current = nil
	s.mu.Unlock()

	exitCode := exitCodeOf(waitErr)
	switch {
	case timedOut:
		s.fail(exitCode, "turn timed out after "+s.opts.TurnTimeout.String())
	case waitErr != nil:
		s.fail(exitCode, errBuf.tail())
	default:
		s.emit(EventTurnCompleted, turnPayload{
			HookEventName: EventTurnCompleted,
			SessionID:     s.captureSessionID(),
		})
	}
	return nil
}

// killCurrent applies the kill ladder to any in-flight turn, if one exists.
func (s *supervisor) killCurrent(reason string) {
	s.mu.Lock()
	cmd := s.current
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	killChildGroup(cmd)
	s.fail(-1, reason)
}

// Kill ladder — order matters (docs/plans/crush-harness.md §5):
//
//  1. SIGINT to the child's PROCESS GROUP (-pid): crush spawns tool
//     subprocesses (bash etc.), so signalling the group avoids orphans.
//  2. Grace period (killGrace) to let the child exit on its own.
//  3. SIGKILL to the same process group.
func killChildGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := -cmd.Process.Pid // negative pid => whole process group
	syscall.Kill(pgid, syscall.SIGINT)
	time.Sleep(killGrace)
	syscall.Kill(pgid, syscall.SIGKILL)
}

// snapshotSessionIDs records which sessions exist before the first turn so
// captureSessionID can identify the newly created one by diff.
func (s *supervisor) snapshotSessionIDs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tookSnap || s.beforeIDs != nil {
		return
	}
	s.tookSnap = true
	sessions, err := listSessions(s.opts)
	if err != nil {
		return
	}
	s.beforeIDs = make(map[string]bool, len(sessions))
	for _, sess := range sessions {
		s.beforeIDs[sess.ID] = true
	}
}

// captureSessionID diffs the post-turn session list against the pre-turn
// snapshot and takes the newest new id. Memoized in-memory and persisted to
// SessionFile for later supervisor runs.
func (s *supervisor) captureSessionID() string {
	if s.opts.ResumeSessionID != "" {
		return s.opts.ResumeSessionID
	}
	s.mu.Lock()
	if s.sessionID != "" {
		id := s.sessionID
		s.mu.Unlock()
		return id
	}
	before := s.beforeIDs
	s.mu.Unlock()

	sessions, err := listSessions(s.opts)
	if err != nil || before == nil {
		return ""
	}
	var newestID, newestCreated string
	for _, sess := range sessions {
		if before[sess.ID] {
			continue
		}
		if sess.Created > newestCreated {
			newestID, newestCreated = sess.ID, sess.Created
		}
	}
	if newestID == "" {
		return ""
	}
	if s.opts.SessionFile != "" {
		_ = os.WriteFile(s.opts.SessionFile, []byte(newestID), 0o644)
	}
	s.mu.Lock()
	s.sessionID = newestID
	s.mu.Unlock()
	return newestID
}

func (s *supervisor) currentSessionID() string {
	if s.opts.ResumeSessionID != "" {
		return s.opts.ResumeSessionID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID != "" {
		return s.sessionID
	}
	if b, err := os.ReadFile(s.opts.SessionFile); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			s.sessionID = id
			return id
		}
	}
	return ""
}

// emit sends a hook event through `h2 handle-hook`. Best-effort: failures are
// logged but never fail the turn loop (same posture as Claude Code hooks).
func (s *supervisor) emit(event string, payload turnPayload) {
	payload.HookEventName = event
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	args := []string{"handle-hook"}
	if s.opts.Actor != "" {
		args = append(args, "--agent", s.opts.Actor)
	}
	hook := exec.Command(s.opts.HookBin, args...)
	hook.Stdin = bytes.NewReader(body)
	hook.Stdout = io.Discard
	hook.Stderr = io.Discard
	if err := hook.Run(); err != nil {
		fmt.Fprintf(s.stderr, "crush-supervisor: hook %s: %v\n", event, err)
	}
}

func (s *supervisor) fail(exitCode int, tail string) {
	s.emit(EventTurnFailed, turnPayload{
		HookEventName: EventTurnFailed,
		ExitCode:      exitCode,
		StderrTail:    tail,
	})
}

// sessionRecord mirrors the entries of `crush session list --json`.
type sessionRecord struct {
	ID      string `json:"id"`
	Created string `json:"created"`
}

func listSessions(o Supervisor) ([]sessionRecord, error) {
	args := []string{"session", "list", "--json", "--data-dir", o.DataDir}
	if o.Host != "" {
		args = append(args, "--host", o.Host)
	}
	out, err := exec.Command(o.CrushBin, args...).Output()
	if err != nil {
		return nil, err
	}
	var sessions []sessionRecord
	if err := json.Unmarshal(bytes.TrimSpace(out), &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

// exitCodeOf extracts a process exit code from a Wait error.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// signalIgnore detaches the shim from SIGINT/SIGHUP/SIGQUIT. The h2 PTY
// delivers interrupts as 0x03 input bytes which we interpret explicitly; the
// shim itself must stay alive to run the kill ladder and report turn.failed.
func signalIgnore() {
	signal.Notify(make(chan os.Signal, 1), syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)
}

// limitedBuffer keeps only the last max bytes of what was written.
type limitedBuffer struct {
	max     int
	buf     bytes.Buffer
	dropped int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.buf.Write(p)
	if over := l.buf.Len() - l.max; over > 0 {
		l.dropped += over
		b := l.buf.Bytes()
		copy(b, b[over:])
		l.buf.Truncate(l.max)
	}
	return len(p), nil
}

func (l *limitedBuffer) tail() string {
	s := l.buf.String()
	if l.dropped > 0 {
		return "[...truncated...]\n" + s
	}
	return s
}
