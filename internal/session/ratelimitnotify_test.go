package session

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"h2/internal/config"
	"h2/internal/session/message"
	"h2/internal/socketdir"
)

func TestIsNewRateLimit(t *testing.T) {
	reset := time.Now().Add(time.Hour)

	tests := []struct {
		name string
		prev *config.RateLimitInfo
		now  time.Time
		want bool
	}{
		{"no prior record", nil, reset, true},
		{"same reset minute", &config.RateLimitInfo{ResetsAt: reset}, reset, false},
		{"different reset time", &config.RateLimitInfo{ResetsAt: reset}, reset.Add(2 * time.Hour), true},
		{"prior already expired", &config.RateLimitInfo{ResetsAt: time.Now().Add(-time.Hour)}, reset, true},
		{"both indefinite (zero)", &config.RateLimitInfo{}, time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNewRateLimit(tt.prev, tt.now); got != tt.want {
				t.Errorf("isNewRateLimit = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildRateLimitNotice(t *testing.T) {
	withReset := buildRateLimitNotice("coder-1", "staging", time.Now().Add(time.Hour))
	for _, want := range []string{"coder-1", "staging", "Tilbakestilles", "h2 rotate coder-1"} {
		if !strings.Contains(withReset, want) {
			t.Errorf("notice %q missing %q", withReset, want)
		}
	}

	indefinite := buildRateLimitNotice("coder-1", "", time.Time{})
	if !strings.Contains(indefinite, "ukjent") {
		t.Errorf("indefinite notice should say reset unknown: %q", indefinite)
	}
	if !strings.Contains(indefinite, "default") {
		t.Errorf("empty profile should render as default: %q", indefinite)
	}
}

func TestNotifyBridges(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("H2_DIR", dir)
	config.ResetResolveCache()
	t.Cleanup(config.ResetResolveCache)

	if err := os.MkdirAll(socketdir.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", socketdir.Path(socketdir.TypeBridge, "telegram"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type received struct{ from, body string }
	ch := make(chan received, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, err := message.ReadRequest(conn)
		if err != nil {
			return
		}
		_ = message.SendResponse(conn, &message.Response{OK: true})
		ch <- received{req.From, req.Body}
	}()

	notifyBridges("", "⚠️ test notice")

	select {
	case got := <-ch:
		if got.body != "⚠️ test notice" {
			t.Errorf("body = %q, want %q", got.body, "⚠️ test notice")
		}
		if got.from != "" {
			t.Errorf("from = %q, want empty (no agent tag for system notice)", got.from)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not receive the notification")
	}
}
