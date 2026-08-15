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

func isHTMLSend(c apiCall) bool {
	return strings.HasSuffix(c.Path, "sendMessage") && c.Form["parse_mode"] == "HTML"
}

func isPlainSend(c apiCall) bool {
	return strings.HasSuffix(c.Path, "sendMessage") && c.Form["parse_mode"] == ""
}

func TestSend_OneShotNeverDrafts(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.Contains(path, "Draft") || strings.Contains(path, "Rich") {
			t.Errorf("unexpected path %s", path)
		}
		if strings.HasSuffix(path, "sendMessage") {
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
}

func TestSend_PersistFailFallsBackToPlain(t *testing.T) {
	n := 0
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if !strings.HasSuffix(path, "sendMessage") {
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

func TestStream_BuffersUntilClose(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.Contains(path, "Draft") || strings.Contains(path, "Rich") {
			t.Errorf("stream must not draft/rich: %s", path)
		}
		if strings.HasSuffix(path, "sendMessage") {
			return persistOK()
		}
		return apiResponse{OK: true}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL}
	s, err := tg.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("world\n")); err != nil {
		t.Fatal(err)
	}
	if n := len(*calls); n != 0 {
		t.Fatalf("writes must not hit the API, got %d calls", n)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var persists int
	for _, c := range *calls {
		if isHTMLSend(c) {
			persists++
			if !strings.Contains(c.Form["text"], "hello") || !strings.Contains(c.Form["text"], "world") {
				t.Fatalf("persist text = %q", c.Form["text"])
			}
		}
	}
	if persists != 1 {
		t.Fatalf("persists = %d, want 1 sendMessage on close", persists)
	}
}

func TestStream_Serialize(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(apiResponse{OK: true})
	}))
	defer srv.Close()
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL}
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

func TestSend_WaitsForOpenStream(t *testing.T) {
	var mu sync.Mutex
	var persistHTML []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if strings.HasSuffix(r.URL.Path, "sendMessage") && r.FormValue("parse_mode") == "HTML" {
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

	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL}
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

func TestSend_NeverCallsDraftOrRichAPI(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		if strings.Contains(path, "Draft") || strings.Contains(path, "Rich") {
			t.Errorf("forbidden API: %s", path)
		}
		return persistOK()
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL}
	if err := tg.Send(context.Background(), "hi"); err != nil {
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

func TestSendTyping_StillChatAction(t *testing.T) {
	srv, calls := recordAPI(t, func(path string, body map[string]any) any {
		return apiResponse{OK: true}
	})
	tg := &Telegram{Token: "TOKEN", ChatID: 1, BaseURL: srv.URL}
	if err := tg.SendTyping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || !strings.HasSuffix((*calls)[0].Path, "sendChatAction") {
		t.Fatalf("calls=%+v", *calls)
	}
	if (*calls)[0].Form["action"] != "typing" {
		t.Fatalf("action = %q", (*calls)[0].Form["action"])
	}
}
