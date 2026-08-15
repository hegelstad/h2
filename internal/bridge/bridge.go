package bridge

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Bridge is the base interface all bridges implement.
type Bridge interface {
	Name() string
	Close() error
}

// Sender is the capability interface for bridges that can send messages.
type Sender interface {
	Send(ctx context.Context, text string) error
}

// InboundHandler is called when a message arrives from an external platform.
// targetAgent is empty string if no prefix was parsed (un-addressed).
type InboundHandler func(targetAgent string, body string)

// Receiver is the capability interface for bridges that can receive messages.
type Receiver interface {
	Start(ctx context.Context, handler InboundHandler) error
	Stop()
}

// TypingIndicator is the capability interface for bridges that can show a
// typing indicator (e.g. Telegram's "typing..." status).
type TypingIndicator interface {
	SendTyping(ctx context.Context) error
}

// FormattedSender is the capability interface for bridges that support
// rich-text formatting on outbound messages (e.g. HTML or MarkdownV2
// parse modes on Telegram). Bridges that implement this receive a
// parse-mode hint from `h2 send --format`.
type FormattedSender interface {
	SendFormatted(ctx context.Context, text, format string) error
}

// RichSender is the capability interface for bridges that support structured
// "rich messages" — headings, lists, tables, block quotations, collapsible
// blocks, formulas, and inline media — that go beyond a simple parse mode
// (e.g. Telegram Bot API 10.1 sendRichMessage). markup selects how the body
// string is interpreted: "markdown" or "html".
type RichSender interface {
	SendRich(ctx context.Context, text, markup string) error
}

// IsRichFormat reports whether a `--format` value targets the RichSender
// capability rather than a plain parse-mode FormattedSender. It returns the
// markup encoding ("markdown" or "html") the bridge should use, and ok=true
// when the format is a rich format. Centralizing this keeps the CLI validator
// and the bridge dispatcher in agreement on the accepted values.
func IsRichFormat(format string) (markup string, ok bool) {
	switch format {
	case "rich", "rich-markdown":
		return "markdown", true
	case "rich-html":
		return "html", true
	default:
		return "", false
	}
}

// richHTMLBlockTagRe matches an opening block-level HTML tag (case-insensitive).
// Telegram sendRichMessage collapses raw newlines to spaces, so a rich-html
// body needs at least one of these to keep line structure. This is a
// substring/opening-tag check, not a full HTML parse: a tag-lookalike in
// prose such as "use <br> to break" counts as a block tag.
var richHTMLBlockTagRe = regexp.MustCompile(`(?i)<(p|br|h[1-6]|ul|ol|li|blockquote|table|tr|td|th|pre|div|hr)(\s|/|>)`)

// ValidateRichHTML reports whether a rich-html body will keep its line
// structure when Telegram parses it as HTML. If text contains a newline
// and no block-level opening tag, it returns a descriptive error.
// Otherwise it returns nil. The CLI calls this before delivery; there is
// no silent rewrite of the body.
func ValidateRichHTML(text string) error {
	if !strings.Contains(text, "\n") {
		return nil
	}
	if richHTMLBlockTagRe.MatchString(text) {
		return nil
	}
	return fmt.Errorf("rich-html body has line breaks but no block tags: raw newlines collapse to spaces in Telegram rich messages. Use <p>, <br>, <ul><li> or <blockquote> for structure")
}

var agentTagRe = regexp.MustCompile(`^\[([a-zA-Z0-9_-]+)\]\s*`)

// ParseAgentTag extracts an "[agent-name]" tag from the start of text.
// Returns the agent name, or empty string if no tag found.
func ParseAgentTag(text string) string {
	m := agentTagRe.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return m[1]
}

// FormatAgentTag prepends an "[agent-name] " tag to text.
func FormatAgentTag(agent, text string) string {
	return "[" + agent + "] " + text
}

var agentPrefixRe = regexp.MustCompile(`(?s)^([a-zA-Z0-9_-]+):\s*(.*)$`)

// ParseAgentPrefix extracts an "agent-name: body" prefix from text.
// The agent name is lowercased to match socket naming conventions.
// Returns empty agent if no valid prefix found.
func ParseAgentPrefix(text string) (agent, body string) {
	m := agentPrefixRe.FindStringSubmatch(text)
	if m == nil {
		return "", text
	}
	return strings.ToLower(m[1]), m[2]
}

// ParseSlashCommand checks if text starts with /<command> where command
// is in the allowed list. Returns the command name and args string,
// or empty command if not matched.
func ParseSlashCommand(text string, allowed []string) (command, args string) {
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	rest := text[1:] // strip leading /
	parts := strings.SplitN(rest, " ", 2)
	cmd := strings.TrimSpace(parts[0])
	if cmd == "" {
		return "", ""
	}
	for _, a := range allowed {
		if cmd == a {
			if len(parts) > 1 {
				args := strings.TrimSpace(parts[1])
				if args != "" {
					return cmd, args
				}
			}
			return cmd, ""
		}
	}
	return "", ""
}

var envelopeRe = regexp.MustCompile(`^\[[^\]]+\]\s*`)

// StripH2Envelope strips a leading "[...]" header (e.g. "[h2 message from: X]",
// "[h2 trigger (...)]") from text. Returns the body unchanged if no envelope is present.
func StripH2Envelope(text string) string {
	loc := envelopeRe.FindStringIndex(text)
	if loc == nil {
		return text
	}
	return strings.TrimSpace(text[loc[1]:])
}
