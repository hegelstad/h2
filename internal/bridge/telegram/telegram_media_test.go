package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestInboundPhotoDownloaded verifies that a photo message is downloaded (largest
// size), saved under $H2_DIR/media/telegram, and handed to the handler as a body
// containing the caption and the saved path.
func TestInboundPhotoDownloaded(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("H2_DIR", tmp)

	var mu sync.Mutex
	var served int
	var received []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/botTOKEN/getUpdates"):
			mu.Lock()
			n := served
			served++
			mu.Unlock()
			if n == 0 {
				json.NewEncoder(w).Encode(getUpdatesResponse{OK: true, Result: []update{{
					UpdateID: 1,
					Message: &message{
						Chat:    chat{ID: 42},
						Caption: "se her",
						Photo:   []photoSize{{FileID: "small"}, {FileID: "big"}},
					},
				}}})
			} else {
				<-r.Context().Done()
			}
		case r.URL.Path == "/botTOKEN/getFile":
			if got := r.URL.Query().Get("file_id"); got != "big" {
				t.Errorf("getFile asked for %q, want largest %q", got, "big")
			}
			json.NewEncoder(w).Encode(getFileResponse{OK: true, Result: struct {
				FilePath string `json:"file_path"`
			}{FilePath: "photos/img.jpg"}})
		case r.URL.Path == "/file/botTOKEN/photos/img.jpg":
			w.Write([]byte("JPEGDATA"))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tg := &Telegram{Token: "TOKEN", ChatID: 42, BaseURL: srv.URL}
	ctx, cancel := context.WithCancel(context.Background())
	tg.Start(ctx, func(agent, body string) {
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	tg.Stop()

	if len(received) == 0 {
		t.Fatal("handler received no message")
	}
	body := received[0]
	if !strings.Contains(body, "se her") {
		t.Errorf("body missing caption: %q", body)
	}
	if !strings.Contains(body, filepath.Join(tmp, "media", "telegram")) {
		t.Errorf("body missing saved path: %q", body)
	}

	files, err := os.ReadDir(filepath.Join(tmp, "media", "telegram"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected 1 saved file, got %v (err %v)", files, err)
	}
	if filepath.Ext(files[0].Name()) != ".jpg" {
		t.Errorf("saved file extension = %s, want .jpg", files[0].Name())
	}
	data, _ := os.ReadFile(filepath.Join(tmp, "media", "telegram", files[0].Name()))
	if string(data) != "JPEGDATA" {
		t.Errorf("saved file contents = %q, want JPEGDATA", data)
	}
}
