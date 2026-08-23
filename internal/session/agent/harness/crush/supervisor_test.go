package crush

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// buildCrushStub writes an executable `crush` stub into its own dir and
// returns that dir (prepend to PATH or pass as CrushBin dir).
func buildCrushStub(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "crush")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeHookLogger writes an h2 stub that appends "argv|stdin-body" lines for
// handle-hook invocations, and returns the bin dir.
func writeHookLogger(t *testing.T, logFile string) string {
	t.Helper()
	binDir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "handle-hook" ]; then
  printf '%%s|%%s\n' "$*" "$(cat)" >> %q
fi
`, logFile)
	if err := os.WriteFile(filepath.Join(binDir, "h2"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binDir
}

func readHooks(t *testing.T, logFile string) []string {
	t.Helper()
	raw, err := os.ReadFile(logFile)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func runSupervisor(t *testing.T, o Supervisor, input string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	err := Run(ctx, o, strings.NewReader(input), &out, os.Stderr)
	return out.String(), err
}

func TestSupervisor_ExitZero_TeesStdout(t *testing.T) {
	crushBin := buildCrushStub(t, `
if [ "$1" = "run" ]; then
  cat >/dev/null
  echo "CRUSH_STUB_REPLY"
  exit 0
fi
echo '[]'
`)
	hookBin := writeHookLogger(t, filepath.Join(t.TempDir(), "hooks.log"))

	out, err := runSupervisor(t, Supervisor{
		CrushBin: filepath.Join(crushBin, "crush"),
		HookBin:  filepath.Join(hookBin, "h2"),
		Actor:    "agent-x",
	}, "[msg from: concierge] do the thing\r\n")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "CRUSH_STUB_REPLY") {
		t.Errorf("child stdout not teed to supervisor stdout, got %q", out)
	}
}

func TestSupervisor_ExitOne_EmitsFailedWithStderrTail(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "hooks.log")
	crushBin := buildCrushStub(t, `
if [ "$1" = "run" ]; then
  cat >/dev/null
  echo "payment required: can only afford 5" >&2
  exit 1
fi
echo '[]'
`)
	hookBin := writeHookLogger(t, logFile)

	_, err := runSupervisor(t, Supervisor{
		CrushBin: filepath.Join(crushBin, "crush"),
		HookBin:  filepath.Join(hookBin, "h2"),
	}, "[h] go\r\n")
	if err != nil {
		t.Fatalf("Run should survive failed turns: %v", err)
	}
	hooks := strings.Join(readHooks(t, logFile), "\n")
	for _, want := range []string{EventTurnStarted, EventTurnFailed, "payment required"} {
		if !strings.Contains(hooks, want) {
			t.Errorf("hook log missing %q:\n%s", want, hooks)
		}
	}
}

func TestSupervisor_PromptPipedViaStdin(t *testing.T) {
	echoFile := filepath.Join(t.TempDir(), "seen.txt")
	crushBin := buildCrushStub(t, fmt.Sprintf(`
cat >> %q
exit 0
`, echoFile))
	hookBin := writeHookLogger(t, filepath.Join(t.TempDir(), "hooks.log"))

	if _, err := runSupervisor(t, Supervisor{
		CrushBin: filepath.Join(crushBin, "crush"),
		HookBin:  filepath.Join(hookBin, "h2"),
	}, "[header] hello supervisor\r\n"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(echoFile)
	if !strings.Contains(string(got), "hello supervisor") {
		t.Errorf("prompt not piped to child stdin, got %q", got)
	}
}

func TestSupervisor_ReadEnvelopeLoadsFileBody(t *testing.T) {
	msgFile := filepath.Join(t.TempDir(), "m.md")
	os.WriteFile(msgFile, []byte("big body from file"), 0o644)
	echoFile := filepath.Join(t.TempDir(), "seen.txt")
	crushBin := buildCrushStub(t, fmt.Sprintf(`
cat >> %q
exit 0
`, echoFile))
	hookBin := writeHookLogger(t, filepath.Join(t.TempDir(), "hooks.log"))

	line := fmt.Sprintf("[msg from: x] Read %s\r\n", msgFile)
	if _, err := runSupervisor(t, Supervisor{
		CrushBin: filepath.Join(crushBin, "crush"),
		HookBin:  filepath.Join(hookBin, "h2"),
	}, line); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(echoFile)
	if !strings.Contains(string(got), "big body from file") {
		t.Errorf("file body not resolved into prompt: %q", got)
	}
}

// sessionCaptureScript emulates crush well enough to exercise id capture:
// `run` creates a session id per data-dir on first call; `session list`
// reports it as JSON. Runs are logged so tests can assert --session replay.
func sessionCaptureScript(runLog string) string {
	return fmt.Sprintf(`
RUN_LOG=%q
SUBCMD="$1 $2"
DATA=""
PREV=""
while [ $# -gt 0 ]; do
  case "$1" in
    --data-dir) DATA="$2"; shift 2 ;;
    --session) PREV="$2"; shift 2 ;;
    *) shift ;;
  esac
done
case "$SUBCMD" in
  "run "*)
    cat >/dev/null
    SID=""
    if [ -f "$DATA/sid" ]; then SID=$(cat "$DATA/sid"); fi
    if [ -z "$SID" ]; then SID="sess-$$$(date +%%s%%N)"; echo "$SID" > "$DATA/sid"; fi
    echo "ran:$SID:$PREV" >> "$RUN_LOG"
    exit 0 ;;
  "session list")
    mkdir -p "$DATA"
    SID=""
    if [ -f "$DATA/sid" ]; then SID=$(cat "$DATA/sid"); fi
    if [ -n "$SID" ]; then
      echo "[{\"id\":\"$SID\",\"created\":\"2030-01-01T00:00:00Z\",\"title\":\"t\"}]"
    else
      echo "[]"
    fi
    exit 0 ;;
esac
exit 1
`, runLog)
}

func TestSupervisor_SessionCaptureAndReplay(t *testing.T) {
	runLog := filepath.Join(t.TempDir(), "runs.log")
	sessionFile := filepath.Join(t.TempDir(), "session.id")
	dataA := t.TempDir()

	crushBin := buildCrushStub(t, sessionCaptureScript(runLog))
	hookBin := writeHookLogger(t, filepath.Join(t.TempDir(), "hooks.log"))

	o := Supervisor{
		CrushBin:    filepath.Join(crushBin, "crush"),
		HookBin:     filepath.Join(hookBin, "h2"),
		SessionFile: sessionFile,
		DataDir:     dataA,
	}

	// Turn 1: fresh agent — no session id exists yet.
	if _, err := runSupervisor(t, o, "[a] turn one\r\n"); err != nil {
		t.Fatal(err)
	}
	sidRaw, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("session id not persisted after first turn: %v", err)
	}
	id := strings.TrimSpace(string(sidRaw))
	if id == "" || strings.Contains(id, "/") {
		t.Fatalf("unexpected persisted session id %q", id)
	}

	// Turn 2 (same process): must pass --session <captured>.
	if _, err := runSupervisor(t, o, "[a] turn two\r\n"); err != nil {
		t.Fatal(err)
	}
	runs, _ := os.ReadFile(runLog)
	runLines := strings.Split(strings.TrimSpace(string(runs)), "\n")
	if len(runLines) != 2 {
		t.Fatalf("want 2 recorded runs, got %d (%v)", len(runLines), runLines)
	}
	parts := strings.Split(runLines[1], ":")
	if len(parts) != 3 || parts[2] != id {
		t.Errorf("second run did not replay --session %q: %v", id, runLines[1])
	}
}

func TestSupervisor_TimeoutKillsHungTurn(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "hooks.log")
	crushBin := buildCrushStub(t, `
if [ "$1" = "run" ]; then
  cat >/dev/null
  sleep 30 &
  wait $!
  exit 0
fi
echo '[]'
`)
	hookBin := writeHookLogger(t, logFile)

	start := time.Now()
	if _, err := runSupervisor(t, Supervisor{
		CrushBin:    filepath.Join(crushBin, "crush"),
		HookBin:     filepath.Join(hookBin, "h2"),
		TurnTimeout: 300 * time.Millisecond,
	}, "[h] hang please\r\n"); err != nil {
		t.Fatalf("timeout turn should not fail the loop: %v", err)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("turn took %v; kill ladder did not fire promptly", d)
	}
	hooks := strings.Join(readHooks(t, logFile), "\n")
	if !strings.Contains(hooks, "timed out") {
		t.Errorf("failed payload missing timeout reason:\n%s", hooks)
	}
}

func TestSupervisor_InterruptByteKillsChildAndSurvives(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "hooks.log")
	startedFile := filepath.Join(t.TempDir(), "started")
	// First run: hang (simulating a long turn). Later runs: exit fast.
	crushBin := buildCrushStub(t, fmt.Sprintf(`
if [ "$1" = "run" ]; then
  cat >/dev/null
  if [ -f %q ]; then
    echo "quick reply"
    exit 0
  fi
  touch %q
  exec sleep 30
fi
echo '[]'
`, startedFile, startedFile))
	hookBin := writeHookLogger(t, logFile)

	// Drive Run manually: start the turn, then deliver SIGINT to THIS
	// process — exactly what the PTY line discipline does when h2 writes
	// 0x03. The shim must forward it to the child group, report
	// turn.failed, and keep serving the next message.
	stdin := newBlockingPipe()
	var out bytes.Buffer
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- Run(context.Background(), Supervisor{
			CrushBin: filepath.Join(crushBin, "crush"),
			HookBin:  filepath.Join(hookBin, "h2"),
		}, stdin, &out, os.Stderr)
	}()

	stdin.Write([]byte("[h] long turn\r\n"))
	waitForFile(t, startedFile, 5*time.Second)
	syscall.Kill(syscall.Getpid(), syscall.SIGINT)

	// Shim survives and serves the next delivered message.
	stdin.Write([]byte("[h] follow-up turn\r\n"))
	stdin.Close()

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("Run errored: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("Run did not finish after interrupt + EOF")
	}
	if !strings.Contains(out.String(), "quick reply") {
		t.Errorf("post-interrupt turn stdout missing: %q", out.String())
	}
	hooks := strings.Join(readHooks(t, logFile), "\n")
	if !strings.Contains(hooks, EventTurnFailed) {
		t.Errorf("interrupt should emit turn.failed:\n%s", hooks)
	}
	if !strings.Contains(hooks, EventTurnCompleted) {
		t.Errorf("post-interrupt turn did not complete:\n%s", hooks)
	}
}

// Two supervisors over the same cwd must each pin their own --data-dir;
// this is the deployment shape where .crush-in-cwd would otherwise collide.
func TestSupervisor_TwoAgentsSameCwdIsolated(t *testing.T) {
	flagsFile := filepath.Join(t.TempDir(), "flags.log")
	crushBin := buildCrushStub(t, fmt.Sprintf(`
echo "$*" >> %q
exit 0
`, flagsFile))
	hookBin := writeHookLogger(t, filepath.Join(t.TempDir(), "hooks.log"))

	newSuper := func(dataDir string) Supervisor {
		return Supervisor{
			CrushBin: filepath.Join(crushBin, "crush"),
			HookBin:  filepath.Join(hookBin, "h2"),
			DataDir:  dataDir,
		}
	}

	var wg sync.WaitGroup
	for _, dd := range []string{"/agents/alpha/data", "/agents/beta/data"} {
		wg.Add(1)
		go func(dd string) {
			defer wg.Done()
			runSupervisor(t, newSuper(dd), "[h] hi\r\n")
		}(dd)
	}
	wg.Wait()

	raw, _ := os.ReadFile(flagsFile)
	content := string(raw)
	if !strings.Contains(content, "--data-dir /agents/alpha/data") ||
		!strings.Contains(content, "--data-dir /agents/beta/data") {
		t.Errorf("expected both agents' explicit --data-dir flags in:\n%s", content)
	}
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		if line != "" && !strings.Contains(line, "--data-dir /agents/") {
			t.Errorf("invocation without explicit per-agent --data-dir: %q", line)
		}
	}
}

// Pins the contract with internal/cmd's handle-hook: JSON on stdin carrying a
// non-empty hook_event_name, agent passed as --agent.
func TestHookEnvelopeMatchesHandleHook(t *testing.T) {
	inFile := filepath.Join(t.TempDir(), "hook-stdin.log")
	crushBin := buildCrushStub(t, `
if [ "$1" = "run" ]; then cat >/dev/null; exit 0; fi
echo '[]'
`)
	binDir := t.TempDir()
	hookScript := fmt.Sprintf(`#!/bin/sh
printf 'ARGV: %%s|BODY: ' "$*" >> %q
cat >> %q
echo >> %q
`, inFile, inFile, inFile)
	os.WriteFile(filepath.Join(binDir, "h2"), []byte(hookScript), 0o755)

	if _, err := runSupervisor(t, Supervisor{
		CrushBin: filepath.Join(crushBin, "crush"),
		HookBin:  filepath.Join(binDir, "h2"),
		Actor:    "coder-7",
	}, "[h] x\r\n"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(inFile)
	content := string(raw)
	if !strings.Contains(content, "--agent coder-7") {
		t.Errorf("handle-hook argv missing --agent coder-7:\n%s", content)
	}
	// handle-hook reads ONE JSON payload per invocation; validate the last
	// one the way internal/cmd would (hook_event_name required).
	lines := strings.Split(strings.TrimSpace(content), "\n")
	last := lines[len(lines)-1]
	bodyStart := strings.Index(last, "{")
	if bodyStart < 0 {
		t.Fatalf("no JSON payload found:\n%s", content)
	}
	var envelope struct {
		HookEventName string `json:"hook_event_name"`
	}
	if err := json.Unmarshal([]byte(last[bodyStart:]), &envelope); err != nil {
		t.Fatalf("hook stdin not valid JSON: %v (%s)", err, last)
	}
	if envelope.HookEventName == "" {
		t.Error("hook_event_name empty — handle_hook.go would reject this envelope")
	}
}

// blockingPipe is an io.Reader+Writer whose reads block until a write occurs
// (like a PTY would), so the supervisor can be driven interactively.
type blockingPipe struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	notify   chan struct{}
	closedC  chan struct{}
	isClosed bool
	once     sync.Once
}

func newBlockingPipe() *blockingPipe {
	return &blockingPipe{notify: make(chan struct{}, 16), closedC: make(chan struct{})}
}

func (p *blockingPipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.buf.Write(b)
	p.mu.Unlock()
	select {
	case p.notify <- struct{}{}:
	default:
	}
	return len(b), nil
}

func (p *blockingPipe) Read(b []byte) (int, error) {
	for {
		p.mu.Lock()
		if p.buf.Len() > 0 {
			n, _ := p.buf.Read(b)
			p.mu.Unlock()
			return n, nil
		}
		closed := p.isClosed
		p.mu.Unlock()
		if closed {
			return 0, io.EOF
		}
		select {
		case <-p.notify:
		case <-p.closedC:
		}
	}
}

func (p *blockingPipe) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		p.isClosed = true
		p.mu.Unlock()
		close(p.closedC)
	})
	return nil
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("file %s never appeared within %v", path, timeout)
}
