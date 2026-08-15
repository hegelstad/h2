package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
