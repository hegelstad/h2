package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"h2/internal/bridge"
)

var (
	// initialBackoff is the starting backoff after a poll error.
	// Var so tests can override it.
	initialBackoff = 1 * time.Second
)

const (
	maxBackoff = 60 * time.Second

	// maxMessageLen is Telegram's maximum message length.
	maxMessageLen = 4096
	// maxRichMessageLen is the Bot API 10.1 rich-message text limit (UTF-8
	// characters), far higher than a plain sendMessage's 4096.
	maxRichMessageLen = 32768
	// maxPages is the maximum number of messages to send for a single response.
	maxPages = 3
)

// Telegram implements bridge.Bridge, bridge.Sender, bridge.FormattedSender,
// bridge.RichSender, and bridge.Receiver using the Telegram Bot API. Standard
// library only — no external Telegram SDK.
type Telegram struct {
	Token           string
	ChatID          int64
	AllowedCommands []string

	// BaseURL overrides the Telegram API base for testing.
	// If empty, defaults to "https://api.telegram.org".
	BaseURL string

	client http.Client
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	offset int64
}

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Close() error {
	t.Stop()
	return nil
}

func (t *Telegram) apiURL(method string) string {
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	return fmt.Sprintf("%s/bot%s/%s", base, t.Token, method)
}

// Send posts a text message to the configured chat. Messages longer than
// Telegram's 4096-character limit are split into multiple messages at line
// boundaries when possible, up to maxPages messages.
func (t *Telegram) Send(ctx context.Context, text string) error {
	return t.sendWithFormat(ctx, text, "")
}

// SendFormatted posts a formatted message (HTML or MarkdownV2) to the
// configured chat, setting Telegram's parse_mode parameter.
func (t *Telegram) SendFormatted(ctx context.Context, text, format string) error {
	return t.sendWithFormat(ctx, text, format)
}

// SendRich posts a Bot API 10.1 "rich message" (sendRichMessage) to the
// configured chat. markup selects the body encoding: "markdown" (default) or
// "html". Rich messages support headings, lists, tables, block quotations,
// collapsible blocks, formulas, and inline media — well beyond sendMessage's
// parse_mode set. Bodies longer than the 32768-character rich limit are split
// at line boundaries, up to maxPages messages; splitting may break a structure
// (e.g. a table) that straddles the boundary, but typical messages fit in one.
func (t *Telegram) SendRich(ctx context.Context, text, markup string) error {
	chunks := bridge.SplitMessage(text, maxRichMessageLen, maxPages)
	for _, chunk := range chunks {
		if err := t.sendRichChunk(ctx, chunk, markup); err != nil {
			return err
		}
	}
	return nil
}

func (t *Telegram) sendRichChunk(ctx context.Context, text, markup string) error {
	richMessage := map[string]string{}
	if markup == "html" {
		richMessage["html"] = text
	} else {
		richMessage["markdown"] = text
	}
	payload := map[string]any{
		"chat_id":      t.ChatID,
		"rich_message": richMessage,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram send rich: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.apiURL("sendRichMessage"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram send rich: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram send rich: %w", err)
	}
	defer resp.Body.Close()

	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("telegram send rich: decode response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("telegram send rich: API error: %s", result.Description)
	}
	return nil
}

func (t *Telegram) sendWithFormat(ctx context.Context, text, format string) error {
	chunks := bridge.SplitMessage(text, maxMessageLen, maxPages)
	for _, chunk := range chunks {
		if err := t.sendChunk(ctx, chunk, format); err != nil {
			return err
		}
	}
	return nil
}

func (t *Telegram) sendChunk(ctx context.Context, text, format string) error {
	params := url.Values{
		"chat_id": {strconv.FormatInt(t.ChatID, 10)},
		"text":    {text},
	}
	if format != "" {
		params.Set("parse_mode", format)
	}
	resp, err := t.client.PostForm(t.apiURL("sendMessage"), params)
	if err != nil {
		return fmt.Errorf("telegram send: %w", err)
	}
	defer resp.Body.Close()

	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("telegram send: decode response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("telegram send: API error: %s", result.Description)
	}
	return nil
}

// Start begins long-polling for incoming messages. It spawns a goroutine
// that polls getUpdates and calls handler for each message from the
// configured ChatID.
func (t *Telegram) Start(ctx context.Context, handler bridge.InboundHandler) error {
	ctx, cancel := context.WithCancel(ctx)
	t.mu.Lock()
	t.cancel = cancel
	t.mu.Unlock()

	t.wg.Add(1)
	go t.poll(ctx, handler)
	return nil
}

// Stop cancels the polling goroutine and waits for it to exit.
func (t *Telegram) Stop() {
	t.mu.Lock()
	cancel := t.cancel
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	t.wg.Wait()
}

func (t *Telegram) poll(ctx context.Context, handler bridge.InboundHandler) {
	defer t.wg.Done()

	backoff := initialBackoff

	for {
		if ctx.Err() != nil {
			return
		}

		updates, err := t.getUpdates(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		backoff = initialBackoff

		for _, u := range updates {
			if u.UpdateID >= t.offset {
				t.offset = u.UpdateID + 1
			}
			if u.Message == nil || u.Message.Chat.ID != t.ChatID {
				continue
			}
			// Check for slash commands before agent routing.
			cmd, args := bridge.ParseSlashCommand(u.Message.Text, t.AllowedCommands)
			if cmd != "" {
				log.Printf("bridge: telegram: executing command /%s %s", cmd, args)
				go t.execAndReply(ctx, cmd, args)
				continue
			}
			// Photo/document attachments: download and hand the agent a
			// local file path it can read, plus any caption.
			if len(u.Message.Photo) > 0 || u.Message.Document != nil {
				body := t.handleMedia(ctx, u.Message)
				agent := ""
				if u.Message.ReplyToMessage != nil {
					agent = bridge.ParseAgentTag(u.Message.ReplyToMessage.Text)
				}
				handler(agent, body)
				continue
			}
			agent, body := bridge.ParseAgentPrefix(u.Message.Text)
			// If no explicit prefix, check reply-to message for agent tag.
			if agent == "" && u.Message.ReplyToMessage != nil {
				agent = bridge.ParseAgentTag(u.Message.ReplyToMessage.Text)
			}
			handler(agent, body)
		}
	}
}

func (t *Telegram) execAndReply(ctx context.Context, cmd, args string) {
	result := bridge.ExecCommand(cmd, args)
	tagged := fmt.Sprintf("[%s result]\n%s", cmd, result)
	if err := t.Send(ctx, tagged); err != nil {
		log.Printf("bridge: telegram: send command result: %v", err)
	}
}

func (t *Telegram) getUpdates(ctx context.Context) ([]update, error) {
	params := url.Values{
		"offset":  {strconv.FormatInt(t.offset, 10)},
		"timeout": {"30"},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", t.apiURL("getUpdates")+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result getUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("getUpdates: API error: %s", result.Description)
	}
	return result.Result, nil
}

// handleMedia downloads a photo or document attachment and returns a body
// string containing the caption (if any) plus the saved local file path so the
// receiving agent can read the file with its Read tool.
func (t *Telegram) handleMedia(ctx context.Context, m *message) string {
	var fileID, hintName string
	switch {
	case len(m.Photo) > 0:
		fileID = m.Photo[len(m.Photo)-1].FileID // last entry is the largest size
		hintName = "photo.jpg"
	case m.Document != nil:
		fileID = m.Document.FileID
		hintName = m.Document.FileName
	}
	caption := strings.TrimSpace(m.Caption)
	path, err := t.downloadFile(ctx, fileID, hintName)
	if err != nil {
		log.Printf("bridge: telegram: media download failed: %v", err)
		note := "[Bilde/fil mottatt via Telegram, men nedlasting feilet.]"
		if caption != "" {
			return caption + "\n" + note
		}
		return note
	}
	log.Printf("bridge: telegram: saved inbound media to %s", path)
	note := fmt.Sprintf("[Bilde/fil mottatt via Telegram. Lagret på serveren: %s — bruk Read-verktøyet på denne stien for å se innholdet.]", path)
	if caption != "" {
		return caption + "\n" + note
	}
	return note
}

// downloadFile resolves a Telegram file_id via getFile and downloads the bytes
// to $H2_DIR/media/telegram, returning the saved path.
func (t *Telegram) downloadFile(ctx context.Context, fileID, hintName string) (string, error) {
	if fileID == "" {
		return "", fmt.Errorf("empty file_id")
	}
	gfURL := t.apiURL("getFile") + "?file_id=" + url.QueryEscape(fileID)
	req, err := http.NewRequestWithContext(ctx, "GET", gfURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var gf getFileResponse
	if err := json.NewDecoder(resp.Body).Decode(&gf); err != nil {
		return "", err
	}
	if !gf.OK || gf.Result.FilePath == "" {
		return "", fmt.Errorf("getFile: %s", gf.Description)
	}

	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	fileURL := fmt.Sprintf("%s/file/bot%s/%s", base, t.Token, gf.Result.FilePath)
	freq, err := http.NewRequestWithContext(ctx, "GET", fileURL, nil)
	if err != nil {
		return "", err
	}
	fresp, err := t.client.Do(freq)
	if err != nil {
		return "", err
	}
	defer fresp.Body.Close()
	if fresp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: status %d", fresp.StatusCode)
	}

	dir := os.Getenv("H2_DIR")
	if dir == "" {
		dir = "."
	}
	mediaDir := filepath.Join(dir, "media", "telegram")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		return "", err
	}
	ext := filepath.Ext(gf.Result.FilePath)
	if ext == "" {
		ext = filepath.Ext(hintName)
	}
	dest := filepath.Join(mediaDir, fmt.Sprintf("%d%s", time.Now().UnixNano(), ext))
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, fresp.Body); err != nil {
		return "", err
	}
	return dest, nil
}

// SendTyping sends a "typing" chat action to the configured chat.
// The indicator is shown for ~5 seconds by Telegram.
func (t *Telegram) SendTyping(ctx context.Context) error {
	resp, err := t.client.PostForm(t.apiURL("sendChatAction"), url.Values{
		"chat_id": {strconv.FormatInt(t.ChatID, 10)},
		"action":  {"typing"},
	})
	if err != nil {
		return fmt.Errorf("telegram sendChatAction: %w", err)
	}
	defer resp.Body.Close()

	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("telegram sendChatAction: decode response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("telegram sendChatAction: API error: %s", result.Description)
	}
	return nil
}

// Unexported types for JSON parsing.

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
}

type getUpdatesResponse struct {
	OK          bool     `json:"ok"`
	Description string   `json:"description,omitempty"`
	Result      []update `json:"result"`
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message,omitempty"`
}

type message struct {
	Text           string      `json:"text"`
	Caption        string      `json:"caption,omitempty"`
	Photo          []photoSize `json:"photo,omitempty"`
	Document       *document   `json:"document,omitempty"`
	Chat           chat        `json:"chat"`
	ReplyToMessage *message    `json:"reply_to_message,omitempty"`
}

type photoSize struct {
	FileID string `json:"file_id"`
}

type document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

type getFileResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
	Result      struct {
		FilePath string `json:"file_path"`
	} `json:"result"`
}

type chat struct {
	ID int64 `json:"id"`
}
