package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"h2/internal/config"
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

func TestValidateFormat(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"", false},
		{"HTML", false},
		{"MarkdownV2", false},
		{"html", true},     // case-sensitive: Telegram only accepts the capitalized values
		{"Markdown", true}, // legacy Telegram "Markdown" mode unsupported here
		{"markdown_v2", true},
		{"rich", false},          // rich message (markdown body)
		{"rich-markdown", false}, // explicit markdown alias
		{"rich-html", false},     // rich message (html body)
		{"rich-xml", true},       // not a recognized rich format
		{"Rich", true},           // case-sensitive
	}
	for _, tt := range tests {
		err := validateFormat(tt.in)
		gotErr := err != nil
		if gotErr != tt.wantErr {
			t.Errorf("validateFormat(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
		}
	}
}

// setupIsolatedSendHome pins H2_DIR to a temp h2 tree so send tests cannot
// walk up into the real sockets dir (e.g. $H2_DIR/sockets/bridge.telegram.sock).
func setupIsolatedSendHome(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	h2Dir := filepath.Join(tmpDir, ".h2")
	if err := os.MkdirAll(h2Dir, 0o700); err != nil {
		t.Fatalf("mkdir h2 dir: %v", err)
	}
	if err := config.WriteMarker(h2Dir); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Setenv("HOME", tmpDir)
	t.Setenv("H2_DIR", h2Dir)
	t.Setenv("H2_ROOT_DIR", h2Dir)
	t.Setenv("H2_ACTOR", "sender")
	config.ResetResolveCache()
	socketdir.ResetDirCache()
	t.Cleanup(func() {
		config.ResetResolveCache()
		socketdir.ResetDirCache()
	})
}

func TestSend_RichHTMLRejectsNewlinesWithoutBlockTags(t *testing.T) {
	setupIsolatedSendHome(t)

	bodyFile := filepath.Join(t.TempDir(), "body.html")
	if err := os.WriteFile(bodyFile, []byte("<b>bold</b>\n<code>code</code>\nplain"), 0o644); err != nil {
		t.Fatalf("write body file: %v", err)
	}

	cmd := newSendCmd()
	cmd.SetArgs([]string{"telegram", "--format", "rich-html", "--file", bodyFile})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected rich-html validation error, got nil")
	}
	if !strings.Contains(err.Error(), "rich-html body has line breaks but no block tags") {
		t.Fatalf("expected rich-html block-tag error, got: %v", err)
	}
	// Must fail before socket lookup so a missing target cannot mask the guard.
	if strings.Contains(err.Error(), "connect") || strings.Contains(err.Error(), "socket") {
		t.Fatalf("validation should reject before dial, got: %v", err)
	}
}

func TestSend_RichHTMLAllowsNewlinesWithBlockTags(t *testing.T) {
	setupIsolatedSendHome(t)

	bodyFile := filepath.Join(t.TempDir(), "body.html")
	if err := os.WriteFile(bodyFile, []byte("<p>hello</p>\n<p>world</p>"), 0o644); err != nil {
		t.Fatalf("write body file: %v", err)
	}

	cmd := newSendCmd()
	cmd.SetArgs([]string{"telegram", "--format", "rich-html", "--file", bodyFile})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected socket error after validation passed, got nil")
	}
	if strings.Contains(err.Error(), "rich-html body has line breaks but no block tags") {
		t.Fatalf("block tags should pass the guard, got: %v", err)
	}
	if !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "socket") && !strings.Contains(err.Error(), "no socket found") {
		t.Fatalf("expected socket/connection error after guard passed, got: %v", err)
	}
}

func TestSend_FormatFlagRejectsInvalidBeforeDial(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".h2", "sockets"), 0o700)
	t.Setenv("HOME", tmpDir)
	t.Setenv("H2_ROOT_DIR", filepath.Join(tmpDir, ".h2"))
	t.Setenv("H2_ACTOR", "sender")

	cmd := newSendCmd()
	cmd.SetArgs([]string{"telegram", "--format", "Markdown", "hi"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected validation error for invalid --format")
	}
	if !strings.Contains(err.Error(), "invalid --format") {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestSend_FormatFlagRejectsAgentTarget(t *testing.T) {
	tmpDir := t.TempDir()
	h2Dir := filepath.Join(tmpDir, ".h2")
	os.MkdirAll(h2Dir, 0o700)
	if err := config.WriteMarker(h2Dir); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Setenv("HOME", tmpDir)
	t.Setenv("H2_DIR", h2Dir)
	t.Setenv("H2_ROOT_DIR", h2Dir)
	t.Setenv("H2_ACTOR", "sender")
	config.ResetResolveCache()
	socketdir.ResetDirCache()
	t.Cleanup(func() {
		config.ResetResolveCache()
		socketdir.ResetDirCache()
	})

	sockDir := socketdir.Dir()
	os.MkdirAll(sockDir, 0o700)

	// Create a fake (non-listening) agent socket file so socketdir.Find succeeds.
	agentSock := filepath.Join(sockDir, "agent.bob.sock")
	if f, err := os.Create(agentSock); err != nil {
		t.Fatalf("create fake socket: %v", err)
	} else {
		f.Close()
	}

	cmd := newSendCmd()
	cmd.SetArgs([]string{"bob", "--format", "HTML", "hi"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --format used with agent target")
	}
	if !strings.Contains(err.Error(), "only valid for bridge targets") {
		t.Fatalf("expected bridge-target error, got: %v", err)
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
