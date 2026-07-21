package session

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vito/midterm"

	"h2/internal/config"
	"h2/internal/session/client"
	"h2/internal/session/virtualterminal"
)

func newPanicRecoverySession() (*Session, *client.Client) {
	s := NewFromConfig(&config.RuntimeConfig{
		AgentName:   "test",
		Command:     "true",
		HarnessType: "generic",
		SessionID:   "test-uuid",
		CWD:         "/tmp",
		StartedAt:   "2024-01-01T00:00:00Z",
	})
	s.VT = &virtualterminal.VT{
		Rows:      24,
		Cols:      80,
		ChildRows: 22,
		Vt:        midterm.NewTerminal(22, 80),
		Output:    io.Discard,
		LastOut:   time.Now(),
	}
	s.VT.SetupScrollCapture()
	s.VT.Scrollback = midterm.NewTerminal(22, 80)
	s.VT.Scrollback.AutoResizeY = true
	s.VT.Scrollback.AppendOnly = true

	cl := s.NewClient()
	cl.Output = io.Discard
	s.AddClient(cl)
	return s, cl
}

func waitForPanicRecoverySignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func panicsOnceThenSignalsStatus(first, second chan<- struct{}) func() (string, string, string) {
	var calls atomic.Int32
	return func() (string, string, string) {
		switch calls.Add(1) {
		case 1:
			close(first)
			panic("test-injected status panic")
		case 2:
			close(second)
		}
		return "idle", "", "1s"
	}
}

func TestSessionTickStatus_PanicRecoveryKeepsTicking(t *testing.T) {
	s, cl := newPanicRecoverySession()
	first := make(chan struct{})
	second := make(chan struct{})
	cl.AgentState = panicsOnceThenSignalsStatus(first, second)

	stop := make(chan struct{})
	defer close(stop)
	go s.TickStatus(stop)

	waitForPanicRecoverySignal(t, first, "first panicking session status tick")
	waitForPanicRecoverySignal(t, second, "second session status tick after panic")
}

func TestOnDeliverDaemon_PanicRecoveryKeepsCallbackUsable(t *testing.T) {
	s, cl := newPanicRecoverySession()
	first := make(chan struct{})
	second := make(chan struct{})
	cl.AgentState = panicsOnceThenSignalsStatus(first, second)
	s.installDaemonOnDeliver()

	s.OnDeliver()
	waitForPanicRecoverySignal(t, first, "first panicking daemon delivery render")
	s.OnDeliver()
	waitForPanicRecoverySignal(t, second, "second daemon delivery render after panic")
}

func TestOnDeliverInteractive_PanicRecoveryKeepsCallbackUsable(t *testing.T) {
	s, cl := newPanicRecoverySession()
	first := make(chan struct{})
	second := make(chan struct{})
	cl.AgentState = panicsOnceThenSignalsStatus(first, second)
	s.installInteractiveOnDeliver()

	s.OnDeliver()
	waitForPanicRecoverySignal(t, first, "first panicking interactive delivery render")
	s.OnDeliver()
	waitForPanicRecoverySignal(t, second, "second interactive delivery render after panic")
}
