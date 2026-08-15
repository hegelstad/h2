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

func TestSend_OneShotNeverDrafts(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if path == "/botTOKEN/sendRichMessage" {
			return sendRichResponse{OK: true}
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
	if (*calls)[0].Path != "/botTOKEN/sendRichMessage" {
		t.Fatalf("path = %s", (*calls)[0].Path)
	}
	rm := (*calls)[0].Body["rich_message"].(map[string]any)
	if rm["skip_entity_detection"] != true {
		t.Fatalf("skip_entity_detection = %v", rm["skip_entity_detection"])
	}
	if rm["html"] != "<p>hi</p>" {
		t.Fatalf("html = %v", rm["html"])
	}
}

func TestSend_PersistFailFallsBackToPlain(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.HasSuffix(path, "sendRichMessage") {
			return apiResponse{OK: false, Description: "boom"}
		}
		return apiResponse{OK: true}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL}
	if err := tg.Send(context.Background(), "keep me"); err != nil {
		t.Fatal(err)
	}
	var sawPlain bool
	for _, c := range *calls {
		if strings.HasSuffix(c.Path, "sendMessage") && c.Form["text"] == "keep me" {
			sawPlain = true
		}
	}
	if !sawPlain {
		t.Fatalf("expected sendMessage fallback with original text, calls=%+v", *calls)
	}
}

func TestSend_OverLimitFallsBackToPlain(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.HasSuffix(path, "sendRichMessage") {
			t.Error("should not persist over-limit html")
		}
		return apiResponse{OK: true}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL}
	if err := tg.Send(context.Background(), strings.Repeat("x", 40000)); err != nil {
		t.Fatal(err)
	}
	var sawPlain bool
	for _, c := range *calls {
		if strings.HasSuffix(c.Path, "sendMessage") {
			sawPlain = true
		}
	}
	if !sawPlain {
		t.Fatal("expected plain fallback for over-limit body")
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
		case strings.HasSuffix(path, "sendRichMessageDraft"):
			return apiResponse{OK: true}
		case strings.HasSuffix(path, "sendRichMessage"):
			var r sendRichResponse
			r.OK = true
			r.Result.MessageID = 77
			return r
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
		case strings.HasSuffix(c.Path, "sendRichMessageDraft"):
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
			rm := c.Body["rich_message"].(map[string]any)
			if !strings.Contains(rm["html"].(string), "<tg-thinking>") {
				t.Fatalf("draft missing thinking: %v", rm["html"])
			}
			if rm["skip_entity_detection"] != true {
				t.Fatal("draft skip_entity_detection")
			}
		case strings.HasSuffix(c.Path, "sendRichMessage") && !strings.HasSuffix(c.Path, "sendRichMessageDraft"):
			persists++
			rm := c.Body["rich_message"].(map[string]any)
			if strings.Contains(rm["html"].(string), "<tg-thinking>") {
				t.Fatal("persist must not include tg-thinking")
			}
		case strings.HasSuffix(c.Path, "editMessageText"):
			edits++
			if c.Body["message_id"] != float64(77) {
				t.Fatalf("edit message_id = %v, want 77", c.Body["message_id"])
			}
			rm := c.Body["rich_message"].(map[string]any)
			if strings.Contains(rm["html"].(string), "<tg-thinking>") {
				t.Fatal("edit must not include tg-thinking")
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
		t.Fatalf("edits = %d, want 1 (same message stays rich)", edits)
	}
}

func TestStream_BurstBoundedDrafts(t *testing.T) {
	clk := newManualClock(time.Unix(1000, 0))
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
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
		if strings.HasSuffix(c.Path, "sendRichMessageDraft") {
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
		if strings.HasSuffix(path, "sendRichMessageDraft") {
			t.Error("must not draft in a non-private chat")
		}
		if strings.HasSuffix(path, "sendMessage") {
			t.Error("must not fall back to sendMessage for non-private")
		}
		return sendRichResponse{OK: true}
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
		if strings.HasSuffix(c.Path, "sendRichMessage") {
			persists++
		}
	}
	if persists != 1 {
		t.Fatalf("persists = %d, want 1", persists)
	}
}

func TestStream_DraftAPIErrorFallsBackToPlain(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.HasSuffix(path, "sendRichMessageDraft") {
			return apiResponse{OK: false, Description: "rate limited"}
		}
		if strings.HasSuffix(path, "sendRichMessage") {
			t.Error("should not persist after genuine draft error")
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
	var sawPlain bool
	for _, c := range *calls {
		if strings.HasSuffix(c.Path, "sendMessage") && c.Form["text"] == "hello\n" {
			sawPlain = true
		}
	}
	if !sawPlain {
		t.Fatalf("expected plain fallback, calls=%+v", *calls)
	}
}

func TestStream_RefreshSameDraftID(t *testing.T) {
	clk := newManualClock(time.Unix(1000, 0))
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
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
		if strings.HasSuffix(c.Path, "sendRichMessageDraft") {
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
		if strings.HasSuffix(c.Path, "sendRichMessage") {
			persists++
			rm := c.Body["rich_message"].(map[string]any)
			if !strings.Contains(rm["html"].(string), "left hanging") {
				t.Fatalf("persist html = %v", rm["html"])
			}
		}
	}
	if persists != 1 {
		t.Fatalf("abandon should persist, got %d sendRichMessage", persists)
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
		if strings.HasSuffix(r.URL.Path, "sendRichMessage") {
			var req sendRichRequest
			json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			persistHTML = append(persistHTML, req.RichMessage.HTML)
			mu.Unlock()
		}
		json.NewEncoder(w).Encode(sendRichResponse{OK: true})
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

func TestDraftIDSkipsZero(t *testing.T) {
	tg := &Telegram{}
	tg.draftSeq.Store(-1) // next Add(1) == 0, must skip
	id := tg.nextDraftID()
	if id == 0 {
		t.Fatal("draft_id was 0")
	}
	id2 := tg.nextDraftID()
	if id2 == 0 || id2 == id {
		t.Fatalf("id2 = %d after %d", id2, id)
	}
}
