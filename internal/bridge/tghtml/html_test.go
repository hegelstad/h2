package tghtml

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRender(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "single line", in: "hello telegram", want: "hello telegram"},
		{name: "blank-line paragraphs", in: "hello\n\nworld", want: "hello\n\nworld"},
		{name: "single newline stays newline", in: "hello\nworld", want: "hello\nworld"},
		{name: "unordered list dash", in: "- one\n- two", want: "• one\n• two"},
		{name: "unordered list star", in: "* one\n* two", want: "• one\n• two"},
		{name: "ordered list dotted", in: "1. one\n2. two", want: "1. one\n2. two"},
		{name: "ordered list paren", in: "1) one\n2) two", want: "1. one\n2. two"},
		{name: "digit without marker is not a list", in: "1foo stays a paragraph", want: "1foo stays a paragraph"},
		{
			name: "fenced code with language",
			in:   "```go\nfmt.Println(1)\n```",
			want: "<pre><code class=\"language-go\">fmt.Println(1)</code></pre>",
		},
		{
			name: "fenced code without language",
			in:   "```\nplain\n```",
			want: "<pre><code>plain</code></pre>",
		},
		{name: "escape text runs", in: "a < b & c > d", want: "a &lt; b &amp; c &gt; d"},
		{name: "legal named entity passes through", in: "foo &nbsp; bar", want: "foo &nbsp; bar"},
		{name: "unknown named entity becomes numeric", in: "copy &copy; right", want: "copy &#169; right"},
		{name: "totally unknown entity escapes amp", in: "x &notanentity; y", want: "x &amp;notanentity; y"},
		{name: "inline code", in: "run `h2 send` now", want: "run <code>h2 send</code> now"},
		{name: "inline bold", in: "this is **bold** text", want: "this is <b>bold</b> text"},
		{name: "code then bold", in: "use `x` and **y**", want: "use <code>x</code> and <b>y</b>"},
		{name: "bold does not apply inside code", in: "see `**notbold**`", want: "see <code>**notbold**</code>"},
		{name: "odd number of backticks stays literal", in: "see `oops", want: "see `oops"},
		{name: "lone double-star stays literal", in: "see ** oops", want: "see ** oops"},
		{name: "backtick inside a fence is not inline code", in: "```\n`raw`\n```", want: "<pre><code>`raw`</code></pre>"},
		{name: "atx heading is a paragraph", in: "# not a heading", want: "# not a heading"},
		{name: "blockquote is escaped", in: "> quote", want: "&gt; quote"},
		{name: "empty input", in: "", want: ""},
		{name: "no p or br or div", in: "hello", want: "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render(tt.in)
			if err != nil {
				t.Fatalf("Render(%q) = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("Render(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, "<p>") || strings.Contains(got, "<p ") ||
				strings.Contains(got, "<br") || strings.Contains(got, "<div") {
				t.Fatalf("Render emitted document tags: %q", got)
			}
		})
	}
}

func TestLooksLikeHTML(t *testing.T) {
	if !LooksLikeHTML("<p>hello</p>") {
		t.Fatal("expected <p> to count as html")
	}
	if !LooksLikeHTML("<b>bold</b>") {
		t.Fatal("expected <b> to count as html")
	}
	if !LooksLikeHTML("<br/>") {
		t.Fatal("expected <br/> to count as html")
	}
	if LooksLikeHTML("hello **bold**") {
		t.Fatal("plain text must not look like html")
	}
	if LooksLikeHTML("see <b foo") {
		t.Fatal("unclosed <b foo must not look like html")
	}
	if LooksLikeHTML("<b foo") {
		t.Fatal("bare <b foo must not look like html")
	}
}

func TestHTML_RendersPlain(t *testing.T) {
	got, err := HTML("hello\nworld")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello\nworld" {
		t.Fatalf("got %q", got)
	}
}

func TestHTML_DownconvertsRich(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "p unwrap", in: "<p>hello</p>", want: "hello"},
		{name: "two paragraphs", in: "<p>hello</p><p>world</p>", want: "hello\n\nworld"},
		{name: "keep bold inside p", in: "<p>hello <b>world</b></p>", want: "hello <b>world</b>"},
		{name: "br becomes newline", in: "a<br>b", want: "a\nb"},
		{name: "br slash", in: "a<br/>b", want: "a\nb"},
		{name: "heading to bold", in: "<h2>Title</h2>", want: "<b>Title</b>"},
		{name: "ul", in: "<ul><li>one</li><li>two</li></ul>", want: "• one\n• two"},
		{name: "ol", in: "<ol><li>one</li><li>two</li></ol>", want: "1. one\n2. two"},
		{name: "pre kept", in: "<pre><code>x</code></pre>", want: "<pre><code>x</code></pre>"},
		{name: "blockquote kept", in: "<blockquote>q</blockquote>", want: "<blockquote>q</blockquote>"},
		{name: "strip thinking", in: "<tg-thinking>Thinking...</tg-thinking>hi", want: "hi"},
		{name: "already chat html", in: "<b>bold</b> and <i>i</i>", want: "<b>bold</b> and <i>i</i>"},
		{name: "raw amp inside tagged html is escaped", in: "<b>ok</b> a & b", want: "<b>ok</b> a &amp; b"},
		{name: "existing amp entity inside tagged html is kept", in: "<b>ok</b> a &amp; b", want: "<b>ok</b> a &amp; b"},
		{name: "raw lt inside tagged html is escaped", in: "<b>ok</b> a < b", want: "<b>ok</b> a &lt; b"},
		{name: "hr", in: "a<hr>b", want: "a\n———\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := HTML(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("HTML(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, "<p>") || strings.Contains(got, "<p ") ||
				strings.Contains(got, "<ul") || strings.Contains(got, "<tg-thinking") {
				t.Fatalf("left document tags in output: %q", got)
			}
		})
	}
}

func TestRender_EscapedLegalEntitiesRoundTrip(t *testing.T) {
	in := "a < b & c > d &nbsp; `x` **y**"
	got, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(got) == 0 {
		t.Fatal("empty render")
	}
	if !strings.Contains(got, "&lt;") || !strings.Contains(got, "&amp;") || !strings.Contains(got, "&gt;") {
		t.Fatalf("missing escaped specials: %q", got)
	}
	if !strings.Contains(got, "&nbsp;") {
		t.Fatalf("legal entity was rewritten: %q", got)
	}
	if !strings.Contains(got, "<code>x</code>") || !strings.Contains(got, "<b>y</b>") {
		t.Fatalf("inline missing: %q", got)
	}
}
