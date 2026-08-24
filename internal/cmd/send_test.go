package cmd

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"h2/internal/session/message"
	"h2/internal/socketdir"
)

func TestSendCmd_SelfSendBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".h2", "sockets"), 0o700)
	t.Setenv("HOME", tmpDir)
	t.Setenv("H2_ROOT_DIR", filepath.Join(tmpDir, ".h2"))
	t.Setenv("H2_ACTOR", "test-agent")

	cmd := newSendCmd()
	cmd.SetArgs([]string{"test-agent", "hello"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when sending to self, got nil")
	}
	if got := err.Error(); got != "cannot send a message to yourself (test-agent); use --allow-self to override" {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestCleanLLMEscapes(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`Hello\!`, `Hello!`},
		{`What\?`, `What?`},
		{`Done\! This is great\!`, `Done! This is great!`},
		{`no escapes here`, `no escapes here`},
		{`keep \\n newline`, `keep \\n newline`},
		{`keep \\t tab`, `keep \\t tab`},
		{`trailing backslash\`, `trailing backslash\`},
		{`\(parens\)`, `(parens)`},
		{`price is \$10`, `price is $10`},
		{`mixed \! and \\n`, `mixed ! and \\n`},
		// Double-escaped (Bash tool doubles backslashes)
		{`Hello\\!`, `Hello!`},
		{`Done\\! Great\\!`, `Done! Great!`},
		// Triple backslash
		{`Hello\\\!`, `Hello!`},
		{``, ``},
	}
	for _, tt := range tests {
		got := cleanLLMEscapes(tt.input)
		if got != tt.want {
			t.Errorf("cleanLLMEscapes(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSendCmd_SelfSendAllowedWithFlag(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".h2", "sockets"), 0o700)
	t.Setenv("HOME", tmpDir)
	t.Setenv("H2_ROOT_DIR", filepath.Join(tmpDir, ".h2"))
	t.Setenv("H2_ACTOR", "test-agent")

	cmd := newSendCmd()
	cmd.SetArgs([]string{"test-agent", "--allow-self", "hello"})

	err := cmd.Execute()
	// With --allow-self, it should get past the self-check and fail on
	// socket lookup instead (no agent running in test).
	if err == nil {
		t.Fatal("expected socket error, got nil")
	}
	// Should NOT be the self-send error
	if got := err.Error(); got == "cannot send a message to yourself (test-agent); use --allow-self to override" {
		t.Fatal("--allow-self flag did not bypass self-send check")
	}
}

func TestSend_Closes_NoBody(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".h2", "sockets"), 0o700)
	t.Setenv("HOME", tmpDir)
	t.Setenv("H2_ROOT_DIR", filepath.Join(tmpDir, ".h2"))
	t.Setenv("H2_ACTOR", "test-agent")

	cmd := newSendCmd()
	cmd.SetArgs([]string{"--closes", "a1b2c3d4"})

	err := cmd.Execute()
	// Should succeed (close-only) but warn about missing socket.
	// The trigger_remove is best-effort, so no error returned.
	if err != nil {
		t.Fatalf("closes should not error should not error, got: %v", err)
	}
}

func TestSend_Closes_BodyNoTarget(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".h2", "sockets"), 0o700)
	t.Setenv("HOME", tmpDir)
	t.Setenv("H2_ROOT_DIR", filepath.Join(tmpDir, ".h2"))

	cmd := newSendCmd()
	// Body but no target — should error.
	cmd.SetArgs([]string{"--closes", "a1b2c3d4", "--file", "/dev/null"})

	// Write a minimal file for --file.
	tmpFile := filepath.Join(tmpDir, "body.txt")
	os.WriteFile(tmpFile, []byte("response body"), 0o644)

	cmd2 := newSendCmd()
	cmd2.SetArgs([]string{"--closes", "a1b2c3d4", "--file", tmpFile})
	err := cmd2.Execute()
	if err == nil {
		t.Fatal("expected error when body present without target")
	}
	if !strings.Contains(err.Error(), "target agent name is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSend_ExpectsResponse_NeedsBody(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".h2", "sockets"), 0o700)
	t.Setenv("HOME", tmpDir)
	t.Setenv("H2_ROOT_DIR", filepath.Join(tmpDir, ".h2"))
	t.Setenv("H2_ACTOR", "sender")

	cmd := newSendCmd()
	cmd.SetArgs([]string{"target-agent", "--expects-response"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no body provided")
	}
	if !strings.Contains(err.Error(), "message body is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSend_ExpectsResponse_FailsOnSocket(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".h2", "sockets"), 0o700)
	t.Setenv("HOME", tmpDir)
	t.Setenv("H2_ROOT_DIR", filepath.Join(tmpDir, ".h2"))
	t.Setenv("H2_ACTOR", "sender")

	cmd := newSendCmd()
	cmd.SetArgs([]string{"nonexistent-agent", "--expects-response", "check this"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected socket error for nonexistent agent")
	}
	// Should be a connection error, not a validation error.
	if !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "socket") {
		t.Fatalf("expected socket/connection error, got: %v", err)
	}
}

func TestSend_StdinTTYRejected(t *testing.T) {
	setupFakeHome(t)
	t.Setenv("H2_ACTOR", "sender")

	cmd := newSendCmd()
	cmd.SetArgs([]string{"telegram", "--stdin"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --stdin is a TTY or not a pipe")
	}
	// In this test environment stdin is typically a TTY or empty; either
	// the TTY guard or a later socket error is acceptable only if TTY
	// is not detected. Prefer the explicit TTY message when it fires.
	if !strings.Contains(err.Error(), "TTY") && !strings.Contains(err.Error(), "pipe") &&
		!strings.Contains(err.Error(), "socket") && !strings.Contains(err.Error(), "connect") &&
		!strings.Contains(err.Error(), "stream") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleStdinSend_FallsBackWhenStreamUnknown(t *testing.T) {
	setupFakeHome(t)
	t.Setenv("H2_ACTOR", "sender")

	sockDir := socketdir.Dir()
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sockPath := filepath.Join(sockDir, socketdir.Format(socketdir.TypeAgent, "old-agent"))
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	var mu sync.Mutex
	var got []message.Request
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			req, rerr := message.ReadRequest(conn)
			if rerr != nil {
				conn.Close()
				continue
			}
			mu.Lock()
			got = append(got, *req)
			mu.Unlock()
			switch req.Type {
			case "send_stream_open", "send_stream_write", "send_stream_close":
				_ = message.SendResponse(conn, &message.Response{
					Error: "unknown request type: " + req.Type,
				})
			case "send":
				_ = message.SendResponse(conn, &message.Response{OK: true, MessageID: "fallback-id"})
			default:
				_ = message.SendResponse(conn, &message.Response{Error: "unknown request type: " + req.Type})
			}
			conn.Close()
		}
	}()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	wantBody := "troubleshoot: full stdin body\nline two"
	if _, err := io.WriteString(w, wantBody); err != nil {
		t.Fatal(err)
	}
	w.Close()

	if err := handleStdinSend("old-agent", true); err != nil {
		t.Fatalf("handleStdinSend: %v", err)
	}

	mu.Lock()
	reqs := append([]message.Request(nil), got...)
	mu.Unlock()
	var sawOpen, sawSend bool
	for _, req := range reqs {
		switch req.Type {
		case "send_stream_open":
			sawOpen = true
		case "send":
			sawSend = true
			if req.Body != wantBody {
				t.Fatalf("send body = %q, want %q", req.Body, wantBody)
			}
		case "send_stream_write", "send_stream_close":
			t.Fatalf("must not continue the stream protocol after unknown open, got %s", req.Type)
		}
	}
	if !sawOpen {
		t.Fatal("expected a send_stream_open probe")
	}
	if !sawSend {
		t.Fatalf("expected ordinary send fallback, requests=%+v", reqs)
	}
}

func TestHandleStdinSend_NewDaemonWritesInChunks(t *testing.T) {
	setupFakeHome(t)
	t.Setenv("H2_ACTOR", "sender")

	sockDir := socketdir.Dir()
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sockPath := filepath.Join(sockDir, socketdir.Format(socketdir.TypeAgent, "new-agent"))
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	var mu sync.Mutex
	var writes []string
	var sawOrdinary bool
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			req, rerr := message.ReadRequest(conn)
			if rerr != nil {
				conn.Close()
				continue
			}
			switch req.Type {
			case "send_stream_open":
				_ = message.SendResponse(conn, &message.Response{OK: true, StreamID: "s1"})
			case "send_stream_write":
				mu.Lock()
				writes = append(writes, req.Body)
				mu.Unlock()
				_ = message.SendResponse(conn, &message.Response{OK: true})
			case "send_stream_close":
				_ = message.SendResponse(conn, &message.Response{OK: true, MessageID: "streamed"})
			case "send":
				mu.Lock()
				sawOrdinary = true
				mu.Unlock()
				_ = message.SendResponse(conn, &message.Response{OK: true})
			default:
				_ = message.SendResponse(conn, &message.Response{Error: "unexpected " + req.Type})
			}
			conn.Close()
		}
	}()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	payload := strings.Repeat("x", 5000)
	if _, err := io.WriteString(w, payload); err != nil {
		t.Fatal(err)
	}
	w.Close()

	if err := handleStdinSend("new-agent", true); err != nil {
		t.Fatalf("handleStdinSend: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), writes...)
	fellBack := sawOrdinary
	mu.Unlock()
	if fellBack {
		t.Fatal("new daemon must not fall back to ordinary send")
	}
	if len(got) < 2 {
		t.Fatalf("writes = %d, want at least 2 chunks, sizes=%v", len(got), chunkSizes(got))
	}
	var joined strings.Builder
	for _, c := range got {
		if len(c) > 4096 {
			t.Fatalf("chunk len %d exceeds 4096", len(c))
		}
		joined.WriteString(c)
	}
	if joined.String() != payload {
		t.Fatalf("joined writes != payload (got %d bytes)", joined.Len())
	}
}

func chunkSizes(chunks []string) []int {
	out := make([]int, len(chunks))
	for i, c := range chunks {
		out[i] = len(c)
	}
	return out
}

func TestIsUnknownRequestType(t *testing.T) {
	if !isUnknownRequestType("unknown request type: send_stream_open") {
		t.Fatal("should match listener wording")
	}
	if isUnknownRequestType("stream open: boom") {
		t.Fatal("must not treat other errors as version skew")
	}
}

func TestGenShortID(t *testing.T) {
	id := genShortID()
	if len(id) != 8 {
		t.Fatalf("expected 8-char ID, got %d: %q", len(id), id)
	}
	// Should be hex.
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex char %c in ID %q", c, id)
		}
	}

	// Should generate unique IDs.
	id2 := genShortID()
	if id == id2 {
		t.Fatal("expected different IDs")
	}
}

func TestRegisterExpectsResponseTrigger_SpecHasDedupDefaults(t *testing.T) {
	setupFakeHome(t)
	t.Setenv("H2_ACTOR", "ox-scheduler")

	var got *message.Request
	sockPath := filepath.Join(socketdir.Dir(), socketdir.Format(socketdir.TypeAgent, "ox-coder"))
	os.MkdirAll(filepath.Dir(sockPath), 0o755)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, err := message.ReadRequest(conn)
		if err != nil {
			return
		}
		got = req
		message.SendResponse(conn, &message.Response{OK: true, TriggerID: req.Trigger.ID})
	}()
	t.Cleanup(func() { ln.Close(); <-done })

	id, err := registerExpectsResponseTrigger("ox-coder", "ox-scheduler", "a1b2c3d4")
	if err != nil {
		t.Fatalf("registerExpectsResponseTrigger: %v", err)
	}
	if id != "a1b2c3d4" {
		t.Fatalf("expected id a1b2c3d4, got %s", id)
	}

	if got == nil || got.Trigger == nil {
		t.Fatal("no trigger_add request received")
	}
	spec := got.Trigger
	if spec.MaxFirings != defaultERTriggerMaxFirings {
		t.Errorf("MaxFirings = %d, want %d (bounded reminder cap)", spec.MaxFirings, defaultERTriggerMaxFirings)
	}
	wantCooldown := defaultERTriggerCooldown.String()
	if spec.Cooldown != wantCooldown {
		t.Errorf("Cooldown = %q, want %q (gap between replays)", spec.Cooldown, wantCooldown)
	}
}
