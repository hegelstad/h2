package tghtml

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"
)

// LooksLikeHTML reports whether text already contains Telegram HTML tags
// (chat or rich). If so, HTML downconverts instead of running the
// plain-text renderer (which would escape the tags).
func LooksLikeHTML(text string) bool {
	return htmlTagRe.MatchString(text)
}

// Require a closing '>' so a prose fragment like "see <b foo" is not
// classified as HTML (which would skip Render's escaping).
var htmlTagRe = regexp.MustCompile(`(?i)<(p|br|h[1-6]|ul|ol|li|blockquote|aside|pre|hr|table|tr|td|th|caption|details|summary|figure|figcaption|footer|tg-collage|tg-slideshow|tg-math-block|tg-thinking|b|strong|i|em|u|ins|s|strike|del|code|mark|sub|sup|a|span|tg-spoiler|tg-emoji|tg-time|tg-math|tg-reference)(\s[^>]*>|/>|>)`)

// HTML returns a parse_mode=HTML body for sendMessage: passthrough /
// downconvert if the body is already HTML, otherwise a deterministic
// render of plain text.
func HTML(text string) (string, error) {
	if LooksLikeHTML(text) {
		return downconvert(text), nil
	}
	return Render(text)
}

var (
	ulItemRe = regexp.MustCompile(`^[-*] `)
	olItemRe = regexp.MustCompile(`^\d+[.)] `)
	fenceRe  = regexp.MustCompile("^```([A-Za-z0-9_+-]*)\\s*$")
)

var legalNamed = map[string]struct{}{
	"lt": {}, "gt": {}, "amp": {}, "quot": {}, "apos": {},
	"nbsp": {}, "hellip": {}, "mdash": {}, "ndash": {},
	"lsquo": {}, "rsquo": {}, "ldquo": {}, "rdquo": {},
}

// Render turns agent-written text with real newlines into Telegram chat HTML.
func Render(text string) (string, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return "", nil
	}

	var b strings.Builder
	lines := strings.Split(text, "\n")
	i := 0
	needBlank := false
	for i < len(lines) {
		line := lines[i]
		if m := fenceRe.FindStringSubmatch(line); m != nil {
			if needBlank {
				b.WriteString("\n\n")
			}
			lang := m[1]
			var body []string
			i++
			for i < len(lines) && !strings.HasPrefix(lines[i], "```") {
				body = append(body, lines[i])
				i++
			}
			if i < len(lines) {
				i++
			}
			b.WriteString(renderFence(lang, strings.Join(body, "\n")))
			needBlank = true
			continue
		}
		if ulItemRe.MatchString(line) || olItemRe.MatchString(line) {
			if needBlank {
				b.WriteString("\n\n")
			}
			ordered := olItemRe.MatchString(line)
			n := 0
			first := true
			for i < len(lines) {
				l := lines[i]
				var item string
				if ordered && olItemRe.MatchString(l) {
					item = olItemRe.ReplaceAllString(l, "")
					n++
				} else if !ordered && ulItemRe.MatchString(l) {
					item = ulItemRe.ReplaceAllString(l, "")
				} else {
					break
				}
				if !first {
					b.WriteByte('\n')
				}
				first = false
				if ordered {
					fmt.Fprintf(&b, "%d. %s", n, renderText(item))
				} else {
					b.WriteString("• ")
					b.WriteString(renderText(item))
				}
				i++
			}
			needBlank = true
			continue
		}
		if strings.TrimSpace(line) == "" {
			i++
			if b.Len() > 0 {
				needBlank = true
			}
			continue
		}
		var para []string
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" &&
			!ulItemRe.MatchString(lines[i]) && !olItemRe.MatchString(lines[i]) &&
			!fenceRe.MatchString(lines[i]) {
			para = append(para, lines[i])
			i++
		}
		if needBlank {
			b.WriteString("\n\n")
		}
		for j, p := range para {
			if j > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(renderText(p))
		}
		needBlank = true
	}
	return b.String(), nil
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
