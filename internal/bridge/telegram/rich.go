package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"h2/internal/bridge"
	"h2/internal/bridge/richhtml"
)

const (
	// skipEntityDetection is always set on InputRichMessage. Our bodies
	// contain file paths, /commands and @names that Telegram would
	// otherwise auto-link (and then prompt "Open this link?").
	skipEntityDetection = true

	draftMinInterval = 200 * time.Millisecond
	draftRefresh     = 20 * time.Second
	streamIdle       = 60 * time.Second

	// botAPITimeout bounds every Bot API call except getUpdates (long poll).
	botAPITimeout = 30 * time.Second

	// thinkingDraftID is reserved for the agent-active "Thinking..." preview.
	// Stream draft ids skip this value.
	thinkingDraftID int64 = 1
)

// Send renders text as rich HTML and persists it with sendRichMessage.
// One-shot never drafts. Any genuine render or persist error falls back
// to plain sendMessage of the original unmodified text.
func (t *Telegram) Send(ctx context.Context, text string) error {
	t.streamMu.Lock()
	defer t.streamMu.Unlock()
	html, err := richhtml.Render(text, richhtml.Options{})
	if err != nil {
		log.Printf("telegram rich render: %v; falling back to sendMessage", err)
		return t.sendPlain(ctx, text)
	}
	if _, err := t.sendRichMessage(ctx, html); err != nil {
		log.Printf("telegram sendRichMessage: %v; falling back to sendMessage", err)
		return t.sendPlain(ctx, text)
	}
	return nil
}

func (t *Telegram) sendPlain(ctx context.Context, text string) error {
	chunks := bridge.SplitMessage(text, maxMessageLen, maxPages)
	for _, chunk := range chunks {
		if err := t.sendChunk(ctx, chunk); err != nil {
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

func (t *Telegram) nextDraftID() int64 {
	for {
		id := t.draftSeq.Add(1)
		if id != 0 && id != thinkingDraftID {
			return id
		}
	}
}

// ShowThinking posts an ephemeral Telegram rich draft with
// <tg-thinking>Thinking...</tg-thinking>. Private chats only; otherwise
// it falls back to the normal typing chat action. Same draft_id so
// refreshes animate instead of stacking.
func (t *Telegram) ShowThinking(ctx context.Context) error {
	if !t.chatIsPrivate(ctx) {
		return t.SendTyping(ctx)
	}
	html := "<tg-thinking>Thinking...</tg-thinking>"
	if err := t.sendRichDraft(ctx, thinkingDraftID, html); err != nil {
		log.Printf("telegram thinking draft: %v; falling back to typing", err)
		return t.SendTyping(ctx)
	}
	return nil
}

type inputRichMessage struct {
	HTML                string `json:"html"`
	SkipEntityDetection bool   `json:"skip_entity_detection"`
}

type sendRichRequest struct {
	ChatID      int64            `json:"chat_id"`
	RichMessage inputRichMessage `json:"rich_message"`
}

type sendRichDraftRequest struct {
	ChatID      int64            `json:"chat_id"`
	DraftID     int64            `json:"draft_id"`
	RichMessage inputRichMessage `json:"rich_message"`
}

type sendRichResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
	Result      struct {
		MessageID int64 `json:"message_id"`
	} `json:"result"`
}

type editRichRequest struct {
	ChatID      int64            `json:"chat_id"`
	MessageID   int64            `json:"message_id"`
	RichMessage inputRichMessage `json:"rich_message"`
}

func (t *Telegram) sendEditRich(ctx context.Context, messageID int64, html string) error {
	body, err := json.Marshal(editRichRequest{
		ChatID:    t.ChatID,
		MessageID: messageID,
		RichMessage: inputRichMessage{
			HTML:                html,
			SkipEntityDetection: skipEntityDetection,
		},
	})
	if err != nil {
		return fmt.Errorf("telegram editMessageText: marshal: %w", err)
	}
	resp, err := t.postJSON(ctx, "editMessageText", body)
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

func (t *Telegram) sendRichMessage(ctx context.Context, html string) (messageID int64, err error) {
	body, err := json.Marshal(sendRichRequest{
		ChatID: t.ChatID,
		RichMessage: inputRichMessage{
			HTML:                html,
			SkipEntityDetection: skipEntityDetection,
		},
	})
	if err != nil {
		return 0, fmt.Errorf("telegram sendRichMessage: marshal: %w", err)
	}
	resp, err := t.postJSON(ctx, "sendRichMessage", body)
	if err != nil {
		return 0, fmt.Errorf("telegram sendRichMessage: %w", err)
	}
	defer resp.Body.Close()
	var result sendRichResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("telegram sendRichMessage: decode: %w", err)
	}
	if !result.OK {
		return 0, fmt.Errorf("telegram sendRichMessage: API error: %s", result.Description)
	}
	return result.Result.MessageID, nil
}

func (t *Telegram) sendRichDraft(ctx context.Context, draftID int64, html string) error {
	body, err := json.Marshal(sendRichDraftRequest{
		ChatID:  t.ChatID,
		DraftID: draftID,
		RichMessage: inputRichMessage{
			HTML:                html,
			SkipEntityDetection: skipEntityDetection,
		},
	})
	if err != nil {
		return fmt.Errorf("telegram sendRichMessageDraft: marshal: %w", err)
	}
	resp, err := t.postJSON(ctx, "sendRichMessageDraft", body)
	if err != nil {
		return fmt.Errorf("telegram sendRichMessageDraft: %w", err)
	}
	defer resp.Body.Close()
	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("telegram sendRichMessageDraft: decode: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("telegram sendRichMessageDraft: API error: %s", result.Description)
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
	drafted     bool // at least one successful sendRichMessageDraft
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
	private := t.chatIsPrivate(ctx)
	t.streamMu.Lock()
	s := &Stream{
		t:       t,
		ctx:     ctx,
		draftID: t.nextDraftID(),
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

// flushLocked: draft (preview) → sendRichMessage (persist, get id) →
// editMessageText on that same message. A draft is not a message and
// cannot be edited into permanence.
func (s *Stream) flushLocked() {
	if s.persisted && s.messageID != 0 {
		html, err := richhtml.Render(string(s.buf), richhtml.Options{})
		if err != nil {
			log.Printf("telegram edit render: %v; will fall back on close", err)
			s.gaveUp = true
			s.dirty = false
			return
		}
		if err := s.t.sendEditRich(s.ctx, s.messageID, html); err != nil {
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
		html, err := richhtml.Render(string(s.buf), richhtml.Options{Thinking: true})
		if err != nil {
			log.Printf("telegram draft render: %v; will persist or fall back on close", err)
			s.dirty = false
			return
		}
		if err := s.t.sendRichDraft(s.ctx, s.draftID, html); err != nil {
			if isNotPrivateDraftError(err) {
				s.private = false
				s.dirty = true
				return
			}
			log.Printf("telegram sendRichMessageDraft: %v; will fall back on close", err)
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
	html, err := richhtml.Render(string(s.buf), richhtml.Options{})
	if err != nil {
		log.Printf("telegram persist render: %v; will fall back on close", err)
		s.gaveUp = true
		s.dirty = false
		return
	}
	id, err := s.t.sendRichMessage(s.ctx, html)
	if err != nil {
		log.Printf("telegram sendRichMessage: %v; will fall back on close", err)
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
		if err := s.t.sendRichDraft(s.ctx, s.draftID, s.lastHTML); err != nil {
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
		log.Printf("telegram stream: falling back to sendMessage after draft failure")
		return s.t.sendPlain(s.ctx, text)
	}
	html, err := richhtml.Render(text, richhtml.Options{})
	if err != nil {
		log.Printf("telegram persist render: %v; falling back to sendMessage", err)
		return s.t.sendPlain(s.ctx, text)
	}
	if persisted && messageID != 0 {
		if err := s.t.sendEditRich(s.ctx, messageID, html); err != nil {
			log.Printf("telegram editMessageText: %v; falling back to sendMessage", err)
			return s.t.sendPlain(s.ctx, text)
		}
		return nil
	}
	if persisted {
		return nil
	}
	if _, err := s.t.sendRichMessage(s.ctx, html); err != nil {
		log.Printf("telegram sendRichMessage: %v; falling back to sendMessage", err)
		return s.t.sendPlain(s.ctx, text)
	}
	return nil
}
