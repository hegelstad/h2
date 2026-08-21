package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"h2/internal/bridge"
	"h2/internal/bridge/tghtml"
)

const (
	// botAPITimeout bounds every Bot API call except getUpdates (long poll).
	botAPITimeout = 30 * time.Second

	// mirrorTag prefixes every mirrored outbound copy so the recipient
	// (e.g. concierge) can tell it apart from a direct message.
	mirrorTag = "[telegram-out] "
)

// mirror enqueues a best-effort copy of a successfully-sent message to the
// configured sink. It is fire-and-forget: the copy runs in its own goroutine
// and any error (or panic) is logged, never propagated, so mirroring can
// neither block nor fail the user-facing send.
func (t *Telegram) mirror(text string) {
	if t.Mirror == nil || text == "" {
		return
	}
	t.mirrorWG.Add(1)
	go func() {
		defer t.mirrorWG.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("telegram mirror: panic: %v", r)
			}
		}()
		if err := t.Mirror(mirrorTag + text); err != nil {
			log.Printf("telegram mirror: %v", err)
		}
	}()
}

// Send renders text as Telegram chat HTML and persists it with
// sendMessage + parse_mode=HTML. A genuine render or persist error
// falls back to plain sendMessage of the original unmodified text.
func (t *Telegram) Send(ctx context.Context, text string) error {
	t.streamMu.Lock()
	defer t.streamMu.Unlock()
	html, err := tghtml.HTML(text)
	if err != nil {
		log.Printf("telegram html: %v; falling back to plain sendMessage", err)
		if err := t.sendPlain(ctx, text); err != nil {
			return err
		}
		t.mirror(text)
		return nil
	}
	if err := t.sendHTML(ctx, html); err != nil {
		log.Printf("telegram sendMessage HTML: %v; falling back to plain", err)
		if err := t.sendPlain(ctx, text); err != nil {
			return err
		}
		t.mirror(text)
		return nil
	}
	t.mirror(text)
	return nil
}

func (t *Telegram) sendPlain(ctx context.Context, text string) error {
	chunks := bridge.SplitMessage(text, maxMessageLen, maxPages)
	for _, chunk := range chunks {
		if err := t.sendMessage(ctx, chunk, ""); err != nil {
			return err
		}
	}
	return nil
}

func (t *Telegram) sendHTML(ctx context.Context, html string) error {
	if html == "" {
		return nil
	}
	chunks := bridge.SplitMessage(html, maxMessageLen, maxPages)
	for _, chunk := range chunks {
		if err := t.sendMessage(ctx, chunk, "HTML"); err != nil {
			return err
		}
	}
	return nil
}

func (t *Telegram) sendHTTPClient() *http.Client {
	c := t.client
	if c.Timeout == 0 {
		c.Timeout = botAPITimeout
	}
	return &c
}

func withAPITimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, botAPITimeout)
}

type sendMessageResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
	Result      struct {
		MessageID int64 `json:"message_id"`
	} `json:"result"`
}

func (t *Telegram) sendMessage(ctx context.Context, text, parseMode string) error {
	ctx, cancel := withAPITimeout(ctx)
	defer cancel()
	form := url.Values{
		"chat_id": {strconv.FormatInt(t.ChatID, 10)},
		"text":    {text},
	}
	if parseMode != "" {
		form.Set("parse_mode", parseMode)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.apiURL("sendMessage"), strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram send: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.sendHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("telegram send: %w", err)
	}
	defer resp.Body.Close()
	var result sendMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("telegram send: decode: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("telegram send: API error: %s", result.Description)
	}
	return nil
}

// Stream buffers one --stdin send and persists it on Close.
// Writes never hit the Bot API. Typing while the sender is active
// is the existing 4s sendChatAction loop, not a second mechanism.
type Stream struct {
	t   *Telegram
	ctx context.Context

	mu     sync.Mutex
	buf    []byte
	closed bool
	done   chan struct{}
}

// OpenStream starts a serialized outbound buffer. A second call blocks
// until the first stream is Closed.
func (t *Telegram) OpenStream(ctx context.Context) (bridge.MessageStream, error) {
	t.streamMu.Lock()
	return &Stream{
		t:    t,
		ctx:  ctx,
		done: make(chan struct{}),
	}, nil
}

func (s *Stream) Done() <-chan struct{} { return s.done }

func (s *Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, fmt.Errorf("telegram stream: write after close")
	}
	s.buf = append(s.buf, p...)
	return len(p), nil
}

func (s *Stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	text := string(s.buf)
	close(s.done)
	s.mu.Unlock()
	defer s.t.streamMu.Unlock()

	if text == "" {
		return nil
	}
	html, err := tghtml.HTML(text)
	if err != nil {
		log.Printf("telegram persist render: %v; falling back to plain", err)
		if err := s.t.sendPlain(s.ctx, text); err != nil {
			return err
		}
		s.t.mirror(text)
		return nil
	}
	if err := s.t.sendHTML(s.ctx, html); err != nil {
		log.Printf("telegram sendMessage HTML: %v; falling back to plain", err)
		if err := s.t.sendPlain(s.ctx, text); err != nil {
			return err
		}
		s.t.mirror(text)
		return nil
	}
	s.t.mirror(text)
	return nil
}
