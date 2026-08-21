package tghtml

import (
	"fmt"
	"strings"
	"unicode"
)

// chat tags that parse_mode=HTML accepts. Copied through as written.
var keepTag = map[string]bool{
	"b": true, "strong": true, "i": true, "em": true,
	"u": true, "ins": true, "s": true, "strike": true, "del": true,
	"code": true, "pre": true, "a": true, "blockquote": true,
	"tg-spoiler": true, "tg-emoji": true,
}

var voidTag = map[string]bool{
	"br": true, "hr": true,
}

type listCtx struct {
	ordered bool
	n       int
}

func downconvert(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	var lists []listCtx
	skip := 0
	i := 0
	for i < len(s) {
		if s[i] != '<' {
			if skip > 0 {
				i++
				continue
			}
			j := i
			for j < len(s) && s[j] != '<' {
				j++
			}
			b.WriteString(escapeText(s[i:j]))
			i = j
			continue
		}
		name, closing, selfClose, raw, end, ok := parseTag(s, i)
		if !ok {
			if skip == 0 {
				b.WriteString("&lt;")
			}
			i++
			continue
		}
		i = end
		if skip > 0 {
			if name == "tg-thinking" {
				if closing {
					skip--
					if skip < 0 {
						skip = 0
					}
				} else if !selfClose {
					skip++
				}
			}
			continue
		}
		switch {
		case name == "tg-thinking":
			if !closing && !selfClose {
				skip = 1
			}
		case name == "br":
			b.WriteByte('\n')
		case name == "hr":
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
				b.WriteByte('\n')
			}
			b.WriteString("———")
			b.WriteByte('\n')
		case name == "p":
			if closing {
				ensureBlank(&b)
			}
		case name == "li":
			if closing {
				continue
			}
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
				b.WriteByte('\n')
			}
			if len(lists) > 0 && lists[len(lists)-1].ordered {
				lists[len(lists)-1].n++
				fmt.Fprintf(&b, "%d. ", lists[len(lists)-1].n)
			} else {
				b.WriteString("• ")
			}
		case name == "ul":
			if !closing {
				lists = append(lists, listCtx{ordered: false})
			} else if len(lists) > 0 {
				lists = lists[:len(lists)-1]
			}
		case name == "ol":
			if !closing {
				lists = append(lists, listCtx{ordered: true})
			} else if len(lists) > 0 {
				lists = lists[:len(lists)-1]
			}
		case len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6':
			if closing {
				b.WriteString("</b>")
			} else {
				if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
					b.WriteByte('\n')
				}
				b.WriteString("<b>")
			}
		case name == "tr":
			if closing && b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
				b.WriteByte('\n')
			}
		case name == "td" || name == "th":
			if !closing && b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") && !strings.HasSuffix(b.String(), " ") {
				b.WriteByte(' ')
			}
		case name == "span":
			if keepSpan(raw) {
				b.WriteString(raw)
			}
		case keepTag[name]:
			b.WriteString(raw)
		default:
			// unwrap unknown / document-only tags
		}
	}
	out := b.String()
	out = strings.TrimSpace(out)
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return out
}

func ensureBlank(b *strings.Builder) {
	s := b.String()
	if s == "" || strings.HasSuffix(s, "\n\n") {
		return
	}
	if strings.HasSuffix(s, "\n") {
		b.WriteByte('\n')
		return
	}
	b.WriteString("\n\n")
}

func keepSpan(raw string) bool {
	low := strings.ToLower(raw)
	return strings.Contains(low, `class="tg-spoiler"`) ||
		strings.Contains(low, `class='tg-spoiler'`)
}

func parseTag(s string, i int) (name string, closing, selfClose bool, raw string, end int, ok bool) {
	if i >= len(s) || s[i] != '<' {
		return "", false, false, "", i, false
	}
	j := i + 1
	if j < len(s) && s[j] == '!' {
		// comment or doctype — skip to >
		for j < len(s) && s[j] != '>' {
			j++
		}
		if j >= len(s) {
			return "", false, false, "", i, false
		}
		return "!--", false, true, s[i : j+1], j + 1, true
	}
	if j < len(s) && s[j] == '/' {
		closing = true
		j++
	}
	start := j
	for j < len(s) && isTagNameChar(s[j]) {
		j++
	}
	if j == start {
		return "", false, false, "", i, false
	}
	name = strings.ToLower(s[start:j])
	for j < len(s) && s[j] != '>' {
		j++
	}
	if j >= len(s) {
		return "", false, false, "", i, false
	}
	raw = s[i : j+1]
	trimmed := strings.TrimSpace(raw[:len(raw)-1])
	selfClose = strings.HasSuffix(trimmed, "/") || voidTag[name]
	return name, closing, selfClose, raw, j + 1, true
}

func isTagNameChar(c byte) bool {
	return unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c)) || c == '-'
}
