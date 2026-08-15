package richhtml

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxChars  = 32768
	maxBlocks = 500
)

// Options controls optional draft-only markup.
type Options struct {
	// Thinking appends <tg-thinking>…</tg-thinking>. Valid only on drafts.
	Thinking bool
}

var (
	ulItemRe = regexp.MustCompile(`^[-*] `)
	olItemRe = regexp.MustCompile(`^\d+[.)] `)
	fenceRe  = regexp.MustCompile("^```([A-Za-z0-9_+-]*)\\s*$")
	// Documented Telegram rich-html tags (Bot API "Rich HTML style").
	richHTMLTagRe = regexp.MustCompile(`(?i)<(p|br|h[1-6]|ul|ol|li|blockquote|aside|pre|hr|table|tr|td|th|caption|details|summary|figure|figcaption|footer|tg-collage|tg-slideshow|tg-math-block|tg-thinking|b|strong|i|em|u|ins|s|strike|del|code|mark|sub|sup|a|tg-spoiler|tg-emoji|tg-time|tg-math|tg-reference)(\s|/|>)`)
)

// LooksLikeRichHTML reports whether text already contains Telegram rich-html
// tags. If so, Send must pass it through as InputRichMessage.html and not
// run the plain-text renderer (which would escape the tags).
func LooksLikeRichHTML(text string) bool {
	return richHTMLTagRe.MatchString(text)
}

// HTML returns the InputRichMessage.html payload: passthrough if the
// body is already Telegram HTML, otherwise a deterministic render of
// plain text.
func HTML(text string) (string, error) {
	if LooksLikeRichHTML(text) {
		if utf8.RuneCountInString(text) > maxChars {
			return "", fmt.Errorf("rich html exceeds 32768 characters")
		}
		return text, nil
	}
	return Render(text, Options{})
}

var legalNamed = map[string]struct{}{
	"lt": {}, "gt": {}, "amp": {}, "quot": {}, "apos": {},
	"nbsp": {}, "hellip": {}, "mdash": {}, "ndash": {},
	"lsquo": {}, "rsquo": {}, "ldquo": {}, "rdquo": {},
}

// Render turns agent-written text with real newlines into Telegram rich HTML.
func Render(text string, opt Options) (string, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return "", nil
	}

	blocks, nBlocks := parseBlocks(text)
	var b strings.Builder
	for _, blk := range blocks {
		b.WriteString(blk)
	}
	if opt.Thinking {
		b.WriteString("<tg-thinking>…</tg-thinking>")
	}
	out := b.String()
	if nBlocks > maxBlocks {
		return "", fmt.Errorf("rich html exceeds 500 blocks (%d)", nBlocks)
	}
	if utf8.RuneCountInString(out) > maxChars {
		return "", fmt.Errorf("rich html exceeds 32768 characters")
	}
	return out, nil
}

func parseBlocks(text string) (blocks []string, n int) {
	lines := strings.Split(text, "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]
		if m := fenceRe.FindStringSubmatch(line); m != nil {
			lang := m[1]
			var body []string
			i++
			for i < len(lines) && !strings.HasPrefix(lines[i], "```") {
				body = append(body, lines[i])
				i++
			}
			if i < len(lines) {
				i++ // closing fence
			}
			blocks = append(blocks, renderFence(lang, strings.Join(body, "\n")))
			n++
			continue
		}
		if ulItemRe.MatchString(line) || olItemRe.MatchString(line) {
			ordered := olItemRe.MatchString(line)
			var items []string
			for i < len(lines) {
				l := lines[i]
				if ordered && olItemRe.MatchString(l) {
					items = append(items, olItemRe.ReplaceAllString(l, ""))
					i++
					continue
				}
				if !ordered && ulItemRe.MatchString(l) {
					items = append(items, ulItemRe.ReplaceAllString(l, ""))
					i++
					continue
				}
				break
			}
			tag := "ul"
			if ordered {
				tag = "ol"
			}
			var sb strings.Builder
			sb.WriteString("<")
			sb.WriteString(tag)
			sb.WriteString(">")
			for _, it := range items {
				sb.WriteString("<li>")
				sb.WriteString(renderText(it))
				sb.WriteString("</li>")
				n++
			}
			sb.WriteString("</")
			sb.WriteString(tag)
			sb.WriteString(">")
			blocks = append(blocks, sb.String())
			continue
		}
		if strings.TrimSpace(line) == "" {
			i++
			continue
		}
		var para []string
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" &&
			!ulItemRe.MatchString(lines[i]) && !olItemRe.MatchString(lines[i]) &&
			!fenceRe.MatchString(lines[i]) {
			para = append(para, lines[i])
			i++
		}
		var sb strings.Builder
		sb.WriteString("<p>")
		for j, p := range para {
			if j > 0 {
				sb.WriteString("<br>")
			}
			sb.WriteString(renderText(p))
		}
		sb.WriteString("</p>")
		blocks = append(blocks, sb.String())
		n++
	}
	return blocks, n
}

func renderFence(lang, body string) string {
	var b strings.Builder
	b.WriteString("<pre><code")
	if lang != "" {
		b.WriteString(` class="language-`)
		b.WriteString(escapeText(lang))
		b.WriteString(`"`)
	}
	b.WriteString(">")
	b.WriteString(escapeText(body))
	b.WriteString("</code></pre>")
	return b.String()
}

func renderText(s string) string {
	return applyInline(escapeText(s))
}

func escapeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '&' {
			if name, end, ok := namedEntityAt(s, i); ok {
				if _, legal := legalNamed[name]; legal {
					b.WriteString("&")
					b.WriteString(name)
					b.WriteString(";")
				} else {
					b.WriteString(rewriteNamed(name))
				}
				i = end
				continue
			}
			if end, ok := numericEntityAt(s, i); ok {
				b.WriteString(s[i:end])
				i = end
				continue
			}
			b.WriteString("&amp;")
			i++
			continue
		}
		if s[i] == '<' {
			b.WriteString("&lt;")
			i++
			continue
		}
		if s[i] == '>' {
			b.WriteString("&gt;")
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func namedEntityAt(s string, i int) (name string, end int, ok bool) {
	if i+2 >= len(s) || s[i] != '&' {
		return "", i, false
	}
	j := i + 1
	if j >= len(s) || !unicode.IsLetter(rune(s[j])) {
		return "", i, false
	}
	j++
	for j < len(s) {
		r := rune(s[j])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			j++
			continue
		}
		break
	}
	if j < len(s) && s[j] == ';' {
		return s[i+1 : j], j + 1, true
	}
	return "", i, false
}

func numericEntityAt(s string, i int) (end int, ok bool) {
	if !strings.HasPrefix(s[i:], "&#") {
		return i, false
	}
	j := i + 2
	if j < len(s) && (s[j] == 'x' || s[j] == 'X') {
		j++
		start := j
		for j < len(s) && isHex(s[j]) {
			j++
		}
		if j > start && j < len(s) && s[j] == ';' {
			return j + 1, true
		}
		return i, false
	}
	start := j
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j > start && j < len(s) && s[j] == ';' {
		return j + 1, true
	}
	return i, false
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func rewriteNamed(name string) string {
	unescaped := html.UnescapeString("&" + name + ";")
	if unescaped == "&"+name+";" || hasASCIILetter(unescaped) {
		// Unknown name, or html.UnescapeString matched a prefix entity
		// (&notanentity; → "¬anentity;").
		return "&amp;" + name + ";"
	}
	var b strings.Builder
	for _, r := range unescaped {
		fmt.Fprintf(&b, "&#%d;", r)
	}
	return b.String()
}

func hasASCIILetter(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			return true
		}
	}
	return false
}

func applyInline(s string) string {
	return applyBold(applyCode(s))
}

func applyCode(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '`' {
			b.WriteByte(s[i])
			i++
			continue
		}
		rest := s[i+1:]
		j := strings.IndexByte(rest, '`')
		if j <= 0 {
			b.WriteByte('`')
			i++
			continue
		}
		b.WriteString("<code>")
		b.WriteString(rest[:j])
		b.WriteString("</code>")
		i += j + 2
	}
	return b.String()
}

func applyBold(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if strings.HasPrefix(s[i:], "<code>") {
			end := strings.Index(s[i:], "</code>")
			if end < 0 {
				b.WriteString(s[i:])
				break
			}
			end += len("</code>")
			b.WriteString(s[i : i+end])
			i += end
			continue
		}
		if strings.HasPrefix(s[i:], "**") {
			rest := s[i+2:]
			j := strings.Index(rest, "**")
			if j > 0 && !strings.Contains(rest[:j], "*") && !strings.Contains(rest[:j], "<code>") {
				b.WriteString("<b>")
				b.WriteString(rest[:j])
				b.WriteString("</b>")
				i += j + 4
				continue
			}
			b.WriteString("**")
			i += 2
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
