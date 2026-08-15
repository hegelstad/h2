package telegram

import (
	"bytes"
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
	draftMinInterval = 200 * time.Millisecond
	draftRefresh     = 20 * time.Second
	streamIdle       = 60 * time.Second

	// botAPITimeout bounds every Bot API call except getUpdates (long poll).
	botAPITimeout = 30 * time.Second

	// previewDraftID is the single outbound preview id for this chat.
	// Thinking and --stdin share it so the placeholder animates into
	// the streamed body instead of stacking two drafts.
	previewDraftID int64 = 1
)

// Send renders text as Telegram chat HTML and persists it with
// sendMessage + parse_mode=HTML. One-shot never drafts. A genuine
// render or persist error falls back to plain sendMessage of the
// original unmodified text.
func (t *Telegram) Send(ctx context.Context, text string) error {
	t.StopThinking()
	t.streamMu.Lock()
	defer t.streamMu.Unlock()
	html, err := tghtml.HTML(text)
	if err != nil {
		log.Printf("telegram html: %v; falling back to plain sendMessage", err)
		return t.sendPlain(ctx, text)
	}
	if err := t.sendHTML(ctx, html); err != nil {
		log.Printf("telegram sendMessage HTML: %v; falling back to plain", err)
		return t.sendPlain(ctx, text)
	}
	return nil
}

func (t *Telegram) sendPlain(ctx context.Context, text string) error {
	chunks := bridge.SplitMessage(text, maxMessageLen, maxPages)
	for _, chunk := range chunks {
		if _, err := t.sendMessage(ctx, chunk, ""); err != nil {
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
		if _, err := t.sendMessage(ctx, chunk, "HTML"); err != nil {
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

func (t *Telegram) clk() clock {
	if t.clock != nil {
		return t.clock
	}
	return realClock{}
}

// ShowThinking posts an ephemeral sendMessageDraft with empty text so
// Telegram shows the official “Thinking…” placeholder. Private chats
// only; otherwise the normal typing chat action. Same draft_id so
// refreshes animate instead of stacking.
func (t *Telegram) ShowThinking(ctx context.Context) error {
	t.mu.Lock()
	t.thinking = true
	t.mu.Unlock()
	if err := t.postThinking(ctx); err != nil {
		return err
	}
	t.armThinkingRefresh()
	return nil
}

func (t *Telegram) StopThinking() {
	t.mu.Lock()
	t.thinking = false
	if t.stopThinkRefresh != nil {
		t.stopThinkRefresh()
		t.stopThinkRefresh = nil
	}
	t.mu.Unlock()
}

func (t *Telegram) postThinking(ctx context.Context) error {
	if !t.chatIsPrivate(ctx) {
		return t.SendTyping(ctx)
	}
	if err := t.sendDraft(ctx, previewDraftID, "", ""); err != nil {
		log.Printf("telegram thinking draft: %v; falling back to typing", err)
		return t.SendTyping(ctx)
	}
	return nil
}

func (t *Telegram) armThinkingRefresh() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopThinkRefresh != nil {
		t.stopThinkRefresh()
		t.stopThinkRefresh = nil
	}
	if !t.thinking {
		return
	}
	t.stopThinkRefresh = t.clk().AfterFunc(draftRefresh, func() {
		t.mu.Lock()
		on := t.thinking
		t.mu.Unlock()
		if !on {
			return
		}
		_ = t.postThinking(context.Background())
		t.armThinkingRefresh()
	})
}

type sendMessageResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
	Result      struct {
		MessageID int64 `json:"message_id"`
	} `json:"result"`
}

func (t *Telegram) sendMessage(ctx context.Context, text, parseMode string) (messageID int64, err error) {
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
		return 0, fmt.Errorf("telegram send: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.sendHTTPClient().Do(req)
	if err != nil {
		return 0, fmt.Errorf("telegram send: %w", err)
	}
	defer resp.Body.Close()
	var result sendMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("telegram send: decode: %w", err)
	}
	if !result.OK {
		return 0, fmt.Errorf("telegram send: API error: %s", result.Description)
	}
	return result.Result.MessageID, nil
}

func (t *Telegram) sendEditHTML(ctx context.Context, messageID int64, html string) error {
	ctx, cancel := withAPITimeout(ctx)
	defer cancel()
	form := url.Values{
		"chat_id":    {strconv.FormatInt(t.ChatID, 10)},
		"message_id": {strconv.FormatInt(messageID, 10)},
		"text":       {html},
		"parse_mode": {"HTML"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.apiURL("editMessageText"), strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram editMessageText: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.sendHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("telegram editMessageText: %w", err)
	}
	defer resp.Body.Close()
	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("telegram editMessageText: decode: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("telegram editMessageText: API error: %s", result.Description)
	}
	return nil
}

type sendDraftRequest struct {
	ChatID    int64  `json:"chat_id"`
	DraftID   int64  `json:"draft_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

func (t *Telegram) sendDraft(ctx context.Context, draftID int64, text, parseMode string) error {
	body, err := json.Marshal(sendDraftRequest{
		ChatID:    t.ChatID,
		DraftID:   draftID,
		Text:      text,
		ParseMode: parseMode,
	})
	if err != nil {
		return fmt.Errorf("telegram sendMessageDraft: marshal: %w", err)
	}
	resp, err := t.postJSON(ctx, "sendMessageDraft", body)
	if err != nil {
		return fmt.Errorf("telegram sendMessageDraft: %w", err)
	}
	defer resp.Body.Close()
	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("telegram sendMessageDraft: decode: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("telegram sendMessageDraft: API error: %s", result.Description)
	}
	return nil
}

func (t *Telegram) postJSON(ctx context.Context, method string, body []byte) (*http.Response, error) {
	ctx, cancel := withAPITimeout(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.apiURL(method), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return t.sendHTTPClient().Do(req)
}

type getChatResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
	Result      struct {
		Type string `json:"type"`
	} `json:"result"`
}

func (t *Telegram) chatIsPrivate(ctx context.Context) bool {
	t.mu.Lock()
	cached := t.chatType
	t.mu.Unlock()
	if cached != "" {
		return cached == "private"
	}
	ctx, cancel := withAPITimeout(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.apiURL("getChat")+"?chat_id="+strconv.FormatInt(t.ChatID, 10), nil)
	if err != nil {
		return true
	}
	resp, err := t.sendHTTPClient().Do(req)
	if err != nil {
		return true // try drafts; a not-private error is handled at draft time
	}
	defer resp.Body.Close()
	var result getChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || !result.OK {
		return true
	}
	t.mu.Lock()
	t.chatType = result.Result.Type
	t.mu.Unlock()
	return result.Result.Type == "private"
}

func isNotPrivateDraftError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return containsFold(s, "private chat") || containsFold(s, "not a private")
}

func containsFold(s, sub string) bool {
	return bytes.Contains(bytes.ToLower([]byte(s)), bytes.ToLower([]byte(sub)))
}

// Stream is one in-flight draft/persist send.
type Stream struct {
	t       *Telegram
	ctx     context.Context
	draftID int64
	private bool

	mu          sync.Mutex
	buf         []byte
	lastHTML    string
	lastDraftAt time.Time
	dirty       bool
	closed      bool
	gaveUp      bool // genuine draft error → persist skipped, plain fallback
	drafted     bool
	persisted   bool
	messageID   int64
	stopRefresh func()
	stopFlush   func()
	stopAbandon func()
	abandoned   bool
	done        chan struct{}
}

// OpenStream starts a serialized outbound stream. A second call blocks
// until the first stream is Closed. Chat type is resolved before taking
// streamMu so a stalled getChat cannot pin the outbound lock.
func (t *Telegram) OpenStream(ctx context.Context) (bridge.MessageStream, error) {
	t.StopThinking()
	private := t.chatIsPrivate(ctx)
	t.streamMu.Lock()
	s := &Stream{
		t:       t,
		ctx:     ctx,
		draftID: previewDraftID,
		private: private,
		done:    make(chan struct{}),
	}
	s.armRefresh()
	s.armAbandon()
	return s, nil
}

func (s *Stream) Done() <-chan struct{} { return s.done }

func (s *Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, fmt.Errorf("telegram stream: write after close")
	}
	s.buf = append(s.buf, p...)
	s.dirty = true
	s.armAbandonLocked()
	if s.gaveUp {
		return len(p), nil
	}
	if s.canFlushLocked() {
		s.flushLocked()
	} else {
		s.scheduleFlushLocked()
	}
	return len(p), nil
}

func (s *Stream) canFlushLocked() bool {
	if s.lastDraftAt.IsZero() {
		return true
	}
	return !s.t.clk().Now().Before(s.lastDraftAt.Add(draftMinInterval))
}

func (s *Stream) scheduleFlushLocked() {
	if s.stopFlush != nil {
		return
	}
	wait := draftMinInterval
	if !s.lastDraftAt.IsZero() {
		wait = s.lastDraftAt.Add(draftMinInterval).Sub(s.t.clk().Now())
		if wait < 0 {
			wait = 0
		}
	}
	s.stopFlush = s.t.clk().AfterFunc(wait, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.stopFlush = nil
		if s.closed || !s.dirty || s.gaveUp {
			return
		}
		s.flushLocked()
	})
}

// flushLocked: draft (preview) → sendMessage (persist, get id) →
// editMessageText on that same message. A draft is not a message and
// cannot be edited into permanence.
func (s *Stream) flushLocked() {
	html, err := tghtml.HTML(string(s.buf))
	if err != nil {
		log.Printf("telegram stream render: %v; will fall back on close", err)
		s.gaveUp = true
		s.dirty = false
		return
	}
	if s.persisted && s.messageID != 0 {
		if err := s.t.sendEditHTML(s.ctx, s.messageID, html); err != nil {
			log.Printf("telegram editMessageText: %v; will fall back on close", err)
			s.gaveUp = true
			s.dirty = false
			return
		}
		s.lastHTML = html
		s.lastDraftAt = s.t.clk().Now()
		s.dirty = false
		return
	}
	if s.persisted {
		s.dirty = false
		return
	}
	if s.private && !s.drafted {
		if err := s.t.sendDraft(s.ctx, s.draftID, html, "HTML"); err != nil {
			if isNotPrivateDraftError(err) {
				s.private = false
				s.dirty = true
				return
			}
			log.Printf("telegram sendMessageDraft: %v; will fall back on close", err)
			s.gaveUp = true
			s.dirty = false
			return
		}
		s.drafted = true
		s.lastHTML = html
		s.lastDraftAt = s.t.clk().Now()
		s.dirty = false
		s.armRefreshLocked()
		return
	}
	id, err := s.t.sendMessage(s.ctx, html, "HTML")
	if err != nil {
		log.Printf("telegram sendMessage HTML: %v; will fall back on close", err)
		s.gaveUp = true
		s.dirty = false
		return
	}
	s.persisted = true
	s.messageID = id
	s.lastHTML = html
	s.lastDraftAt = s.t.clk().Now()
	s.dirty = false
	if s.stopRefresh != nil {
		s.stopRefresh()
		s.stopRefresh = nil
	}
}

func (s *Stream) armRefresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armRefreshLocked()
}

func (s *Stream) armRefreshLocked() {
	if s.stopRefresh != nil {
		s.stopRefresh()
		s.stopRefresh = nil
	}
	if !s.private || s.gaveUp || s.persisted {
		return
	}
	s.stopRefresh = s.t.clk().AfterFunc(draftRefresh, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed || s.gaveUp || !s.private || s.lastHTML == "" {
			return
		}
		if err := s.t.sendDraft(s.ctx, s.draftID, s.lastHTML, "HTML"); err != nil {
			if isNotPrivateDraftError(err) {
				s.private = false
				return
			}
			log.Printf("telegram draft refresh: %v", err)
			s.gaveUp = true
			return
		}
		s.lastDraftAt = s.t.clk().Now()
		s.armRefreshLocked()
	})
}

func (s *Stream) armAbandon() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armAbandonLocked()
}

func (s *Stream) armAbandonLocked() {
	if s.stopAbandon != nil {
		s.stopAbandon()
		s.stopAbandon = nil
	}
	s.stopAbandon = s.t.clk().AfterFunc(streamIdle, func() {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		s.abandoned = true
		s.mu.Unlock()
		log.Printf("telegram stream: abandoned after 60s idle; persisting")
		_ = s.Close()
	})
}

func (s *Stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.stopRefresh != nil {
		s.stopRefresh()
		s.stopRefresh = nil
	}
	if s.stopFlush != nil {
		s.stopFlush()
		s.stopFlush = nil
	}
	if s.stopAbandon != nil {
		s.stopAbandon()
		s.stopAbandon = nil
	}
	text := string(s.buf)
	gaveUp := s.gaveUp
	persisted := s.persisted
	messageID := s.messageID
	close(s.done)
	s.mu.Unlock()
	defer s.t.streamMu.Unlock()

	if text == "" {
		return nil
	}
	if gaveUp {
		log.Printf("telegram stream: falling back to plain sendMessage after draft failure")
		return s.t.sendPlain(s.ctx, text)
	}
	html, err := tghtml.HTML(text)
	if err != nil {
		log.Printf("telegram persist render: %v; falling back to plain", err)
		return s.t.sendPlain(s.ctx, text)
	}
	if persisted && messageID != 0 {
		if err := s.t.sendEditHTML(s.ctx, messageID, html); err != nil {
			log.Printf("telegram editMessageText: %v; falling back to plain", err)
			return s.t.sendPlain(s.ctx, text)
		}
		return nil
	}
	if persisted {
		return nil
	}
	if err := s.t.sendHTML(s.ctx, html); err != nil {
		log.Printf("telegram sendMessage HTML: %v; falling back to plain", err)
		return s.t.sendPlain(s.ctx, text)
	}
	return nil
}
