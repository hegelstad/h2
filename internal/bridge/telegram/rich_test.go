package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type apiCall struct {
	Path string
	Body map[string]any
	Form urlValues
}

type urlValues map[string]string

func recordAPI(t *testing.T, handler func(path string, body map[string]any) any) (*httptest.Server, *[]apiCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []apiCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		_ = r.ParseForm()
		form := urlValues{}
		for k := range r.Form {
			form[k] = r.Form.Get(k)
		}
		mu.Lock()
		calls = append(calls, apiCall{Path: r.URL.Path, Body: body, Form: form})
		mu.Unlock()
		out := handler(r.URL.Path, body)
		if out == nil {
			json.NewEncoder(w).Encode(apiResponse{OK: true})
			return
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func persistOK() sendMessageResponse {
	var r sendMessageResponse
	r.OK = true
	r.Result.MessageID = 77
	return r
}

func isDraft(c apiCall) bool {
	return strings.HasSuffix(c.Path, "sendMessageDraft")
}

func isHTMLSend(c apiCall) bool {
	return strings.HasSuffix(c.Path, "sendMessage") &&
		!strings.HasSuffix(c.Path, "sendMessageDraft") &&
		c.Form["parse_mode"] == "HTML"
}

func isPlainSend(c apiCall) bool {
	return strings.HasSuffix(c.Path, "sendMessage") &&
		!strings.HasSuffix(c.Path, "sendMessageDraft") &&
		c.Form["parse_mode"] == ""
}

func TestSend_OneShotNeverDrafts(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.HasSuffix(path, "sendMessage") && !strings.HasSuffix(path, "sendMessageDraft") {
			return persistOK()
		}
		t.Errorf("unexpected path %s", path)
		return apiResponse{OK: false, Description: "unexpected"}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL}
	if err := tg.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(*calls))
	}
	c := (*calls)[0]
	if !isHTMLSend(c) {
		t.Fatalf("call = %+v, want sendMessage parse_mode=HTML", c)
	}
	if c.Form["text"] != "hi" {
		t.Fatalf("text = %q", c.Form["text"])
	}
	if isDraft(c) {
		t.Fatal("one-shot must not draft")
	}
}

func TestSend_PersistFailFallsBackToPlain(t *testing.T) {
	n := 0
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if !strings.HasSuffix(path, "sendMessage") || strings.HasSuffix(path, "sendMessageDraft") {
			return apiResponse{OK: true}
		}
		n++
		if n == 1 {
			return apiResponse{OK: false, Description: "boom"}
		}
		var r sendMessageResponse
		r.OK = true
		return r
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL}
	if err := tg.Send(context.Background(), "keep me"); err != nil {
		t.Fatal(err)
	}
	var sawPlain bool
	for _, c := range *calls {
		if isPlainSend(c) && c.Form["text"] == "keep me" {
			sawPlain = true
		}
	}
	if !sawPlain {
		t.Fatalf("expected sendMessage fallback with original text, calls=%+v", *calls)
	}
}

func TestStream_DraftThenPersist(t *testing.T) {
	clk := newManualClock(time.Unix(1000, 0))
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		switch {
		case strings.HasSuffix(path, "getChat"):
			return getChatResponse{OK: true, Result: struct {
				Type string `json:"type"`
			}{Type: "private"}}
		case strings.HasSuffix(path, "sendMessageDraft"):
			return apiResponse{OK: true}
		case strings.HasSuffix(path, "sendMessage"):
			return persistOK()
		case strings.HasSuffix(path, "editMessageText"):
			return apiResponse{OK: true}
		}
		return apiResponse{OK: false, Description: path}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL, clock: clk, chatType: "private"}
	s, err := tg.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(draftMinInterval)
	if _, err := s.Write([]byte("world\n")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(draftMinInterval)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var drafts, persists, edits int
	var draftID float64
	for _, c := range *calls {
		switch {
		case isDraft(c):
			drafts++
			id, _ := c.Body["draft_id"].(float64)
			if id == 0 {
				t.Fatal("draft_id must be non-zero")
			}
			if draftID == 0 {
				draftID = id
			} else if id != draftID {
				t.Fatalf("draft_id changed %v -> %v", draftID, id)
			}
			if id != float64(previewDraftID) {
				t.Fatalf("draft_id = %v, want %d", id, previewDraftID)
			}
			if c.Body["parse_mode"] != "HTML" {
				t.Fatalf("draft parse_mode = %v", c.Body["parse_mode"])
			}
		case isHTMLSend(c):
			persists++
		case strings.HasSuffix(c.Path, "editMessageText"):
			edits++
			if c.Form["message_id"] != "77" {
				t.Fatalf("edit message_id = %q, want 77", c.Form["message_id"])
			}
			if c.Form["parse_mode"] != "HTML" {
				t.Fatalf("edit parse_mode = %q", c.Form["parse_mode"])
			}
		}
	}
	if drafts == 0 {
		t.Fatal("expected at least one draft")
	}
	if persists != 1 {
		t.Fatalf("persists = %d, want 1", persists)
	}
	if edits != 1 {
		t.Fatalf("edits = %d, want 1", edits)
	}
}

func TestStream_BurstBoundedDrafts(t *testing.T) {
	clk := newManualClock(time.Unix(1000, 0))
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.HasSuffix(path, "sendMessage") && !strings.HasSuffix(path, "sendMessageDraft") {
			return persistOK()
		}
		return apiResponse{OK: true}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL, clock: clk, chatType: "private"}
	s, err := tg.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := s.Write([]byte("line\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var drafts int
	for _, c := range *calls {
		if isDraft(c) {
			drafts++
		}
	}
	if drafts > 5 {
		t.Fatalf("burst produced %d drafts, want a bounded number (floor 200ms)", drafts)
	}
	if drafts < 1 {
		t.Fatal("expected at least the first draft")
	}
}

func TestStream_NonPrivateSkipsDraftsAndPersists(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.HasSuffix(path, "sendMessageDraft") {
			t.Error("must not draft in a non-private chat")
		}
		if strings.HasSuffix(path, "sendMessage") {
			return persistOK()
		}
		return apiResponse{OK: true}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL, chatType: "supergroup"}
	s, err := tg.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.Write([]byte("hello\nworld\n"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var persists int
	for _, c := range *calls {
		if isHTMLSend(c) {
			persists++
		}
	}
	if persists != 1 {
		t.Fatalf("persists = %d, want 1", persists)
	}
}

func TestStream_DraftAPIErrorFallsBackToPlain(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.HasSuffix(path, "sendMessageDraft") {
			return apiResponse{OK: false, Description: "rate limited"}
		}
		if strings.HasSuffix(path, "sendMessage") && !strings.HasSuffix(path, "sendMessageDraft") {
			// persist-with-HTML must not run after genuine draft error
			return persistOK()
		}
		return apiResponse{OK: true}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL, chatType: "private"}
	s, err := tg.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.Write([]byte("hello\n"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var sawPlain, sawHTML bool
	for _, c := range *calls {
		if isPlainSend(c) && c.Form["text"] == "hello\n" {
			sawPlain = true
		}
		if isHTMLSend(c) {
			sawHTML = true
		}
	}
	if !sawPlain {
		t.Fatalf("expected plain fallback, calls=%+v", *calls)
	}
	if sawHTML {
		t.Fatal("should not persist HTML after genuine draft error")
	}
}

func TestStream_RefreshSameDraftID(t *testing.T) {
	clk := newManualClock(time.Unix(1000, 0))
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.HasSuffix(path, "sendMessage") && !strings.HasSuffix(path, "sendMessageDraft") {
			return persistOK()
		}
		return apiResponse{OK: true}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL, clock: clk, chatType: "private"}
	s, err := tg.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.Write([]byte("hello\n"))
	clk.Advance(draftRefresh + time.Second)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var ids []float64
	for _, c := range *calls {
		if isDraft(c) {
			ids = append(ids, c.Body["draft_id"].(float64))
		}
	}
	if len(ids) < 2 {
		t.Fatalf("expected refresh draft, got %d draft calls", len(ids))
	}
	for _, id := range ids {
		if id == 0 {
			t.Fatal("zero draft_id")
		}
		if id != ids[0] {
			t.Fatalf("draft_id changed across refresh: %v", ids)
		}
	}
}

func TestStream_Serialize(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(apiResponse{OK: true})
	}))
	defer srv.Close()
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL, chatType: "private"}
	s1, err := tg.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		close(started)
		s2, err := tg.OpenStream(context.Background())
		if err != nil {
			t.Error(err)
			return
		}
		close(release)
		_ = s2.Close()
	}()
	<-started
	select {
	case <-release:
		t.Fatal("second OpenStream did not wait")
	case <-time.After(50 * time.Millisecond):
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-release:
	case <-time.After(time.Second):
		t.Fatal("second OpenStream did not proceed after Close")
	}
}

func TestStream_AbandonPersists(t *testing.T) {
	clk := newManualClock(time.Unix(1000, 0))
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.HasSuffix(path, "sendMessage") && !strings.HasSuffix(path, "sendMessageDraft") {
			return persistOK()
		}
		return apiResponse{OK: true}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL, clock: clk, chatType: "private"}
	s, err := tg.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("left hanging\n")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(streamIdle + time.Second)
	var persists int
	for _, c := range *calls {
		if isHTMLSend(c) && strings.Contains(c.Form["text"], "left hanging") {
			persists++
		}
	}
	if persists != 1 {
		t.Fatalf("abandon should persist, got %d HTML sendMessage", persists)
	}
}

func TestOpenStream_HangingGetChatDoesNotHoldLock(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getChat") {
			close(started)
			<-r.Context().Done()
			return
		}
		json.NewEncoder(w).Encode(apiResponse{OK: true})
	}))
	defer srv.Close()

	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = tg.OpenStream(ctx)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("getChat never started")
	}
	tg.mu.Lock()
	tg.chatType = "private"
	tg.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s, err := tg.OpenStream(context.Background())
		if err != nil {
			t.Error(err)
		} else {
			_ = s.Close()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("streamMu held during hanging getChat")
	}
}

func TestSend_WaitsForOpenStream(t *testing.T) {
	var mu sync.Mutex
	var persistHTML []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if strings.HasSuffix(r.URL.Path, "sendMessage") && !strings.HasSuffix(r.URL.Path, "sendMessageDraft") &&
			r.FormValue("parse_mode") == "HTML" {
			mu.Lock()
			persistHTML = append(persistHTML, r.FormValue("text"))
			mu.Unlock()
		}
		var out sendMessageResponse
		out.OK = true
		out.Result.MessageID = 1
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL, chatType: "private"}
	s, err := tg.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("stream body\n")); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	sendDone := make(chan error, 1)
	go func() {
		close(started)
		sendDone <- tg.Send(context.Background(), "one shot")
	}()
	<-started
	select {
	case err := <-sendDone:
		t.Fatalf("Send completed while stream open: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Send did not proceed after stream close")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(persistHTML) < 2 {
		t.Fatalf("persists = %v, want stream then one-shot", persistHTML)
	}
	if !strings.Contains(persistHTML[0], "stream body") {
		t.Fatalf("first persist = %q, want stream body", persistHTML[0])
	}
	if !strings.Contains(persistHTML[len(persistHTML)-1], "one shot") {
		t.Fatalf("last persist = %q, want one shot", persistHTML[len(persistHTML)-1])
	}
}

func TestSend_HTMLDownconvert(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		return persistOK()
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL}
	in := "<p>hello <b>world</b></p>"
	if err := tg.Send(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || !isHTMLSend((*calls)[0]) {
		t.Fatalf("calls=%+v", *calls)
	}
	if (*calls)[0].Form["text"] != "hello <b>world</b>" {
		t.Fatalf("text = %q", (*calls)[0].Form["text"])
	}
}

func TestShowThinking_SendsDraft(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		return apiResponse{OK: true}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL, chatType: "private"}
	if err := tg.ShowThinking(context.Background()); err != nil {
		t.Fatal(err)
	}
	var drafts int
	for _, c := range *calls {
		if !isDraft(c) {
			continue
		}
		drafts++
		if c.Body["draft_id"] != float64(previewDraftID) {
			t.Fatalf("draft_id = %v, want %d", c.Body["draft_id"], previewDraftID)
		}
		if c.Body["text"] != "" {
			t.Fatalf("thinking text = %v, want empty", c.Body["text"])
		}
	}
	if drafts != 1 {
		t.Fatalf("drafts = %d, want 1", drafts)
	}
}

func TestShowThinking_NonPrivateFallsBackToTyping(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.HasSuffix(path, "sendMessageDraft") {
			t.Error("must not draft thinking in a non-private chat")
		}
		return apiResponse{OK: true}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL, chatType: "supergroup"}
	if err := tg.ShowThinking(context.Background()); err != nil {
		t.Fatal(err)
	}
	var typing int
	for _, c := range *calls {
		if strings.HasSuffix(c.Path, "sendChatAction") {
			typing++
		}
	}
	if typing != 1 {
		t.Fatalf("typing = %d, want 1", typing)
	}
}

func TestSend_NeverCallsRichAPI(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.Contains(path, "Rich") {
			t.Errorf("rich API called: %s", path)
		}
		return persistOK()
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL, chatType: "private"}
	if err := tg.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if err := tg.ShowThinking(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err := tg.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Write([]byte("x\n"))
	_ = s.Close()
	if len(*calls) == 0 {
		t.Fatal("no calls")
	}
}
