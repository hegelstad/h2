package richhtml

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRender(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		opt     Options
		want    string
		wantErr bool
	}{
		{
			name: "single line paragraph",
			in:   "hello telegram",
			want: "<p>hello telegram</p>",
		},
		{
			name: "blank-line paragraphs",
			in:   "hello\n\nworld",
			want: "<p>hello</p><p>world</p>",
		},
		{
			name: "single newline becomes br",
			in:   "hello\nworld",
			want: "<p>hello<br>world</p>",
		},
		{
			name: "unordered list dash",
			in:   "- one\n- two",
			want: "<ul><li>one</li><li>two</li></ul>",
		},
		{
			name: "unordered list star",
			in:   "* one\n* two",
			want: "<ul><li>one</li><li>two</li></ul>",
		},
		{
			name: "ordered list dotted",
			in:   "1. one\n2. two",
			want: "<ol><li>one</li><li>two</li></ol>",
		},
		{
			name: "ordered list paren",
			in:   "1) one\n2) two",
			want: "<ol><li>one</li><li>two</li></ol>",
		},
		{
			name: "digit without marker is not a list",
			in:   "1foo stays a paragraph",
			want: "<p>1foo stays a paragraph</p>",
		},
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
		{
			name: "escape text runs",
			in:   "a < b & c > d",
			want: "<p>a &lt; b &amp; c &gt; d</p>",
		},
		{
			name: "legal named entity passes through",
			in:   "foo &nbsp; bar",
			want: "<p>foo &nbsp; bar</p>",
		},
		{
			name: "unknown named entity becomes numeric",
			in:   "copy &copy; right",
			want: "<p>copy &#169; right</p>",
		},
		{
			name: "totally unknown entity escapes amp",
			in:   "x &notanentity; y",
			want: "<p>x &amp;notanentity; y</p>",
		},
		{
			name: "inline code",
			in:   "run `h2 send` now",
			want: "<p>run <code>h2 send</code> now</p>",
		},
		{
			name: "inline bold",
			in:   "this is **bold** text",
			want: "<p>this is <b>bold</b> text</p>",
		},
		{
			name: "code then bold",
			in:   "use `x` and **y**",
			want: "<p>use <code>x</code> and <b>y</b></p>",
		},
		{
			name: "bold does not apply inside code",
			in:   "see `**notbold**`",
			want: "<p>see <code>**notbold**</code></p>",
		},
		{
			name: "odd number of backticks stays literal",
			in:   "see `oops",
			want: "<p>see `oops</p>",
		},
		{
			name: "lone double-star stays literal",
			in:   "see ** oops",
			want: "<p>see ** oops</p>",
		},
		{
			name: "backtick inside a fence is not inline code",
			in:   "```\n`raw`\n```",
			want: "<pre><code>`raw`</code></pre>",
		},
		{
			name: "atx heading is a paragraph",
			in:   "# not a heading",
			want: "<p># not a heading</p>",
		},
		{
			name: "blockquote is a paragraph",
			in:   "> quote",
			want: "<p>&gt; quote</p>",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			name: "thinking suffix on drafts",
			in:   "hello",
			opt:  Options{Thinking: true},
			want: "<p>hello</p><tg-thinking>…</tg-thinking>",
		},
		{
			name: "no div ever",
			in:   "hello",
			want: "<p>hello</p>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render(tt.in, tt.opt)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Render(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render(%q) = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("Render(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, "<div") {
				t.Fatalf("Render emitted <div>: %q", got)
			}
		})
	}
}

func TestRender_OverCharLimit(t *testing.T) {
	in := strings.Repeat("x", maxChars+1)
	_, err := Render(in, Options{})
	if err == nil {
		t.Fatal("expected error over char limit")
	}
	if !strings.Contains(err.Error(), "32768") {
		t.Fatalf("error = %v, want 32768", err)
	}
}

func TestRender_OverBlockLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxBlocks+1; i++ {
		b.WriteString("p\n\n")
	}
	_, err := Render(b.String(), Options{})
	if err == nil {
		t.Fatal("expected error over block limit")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want 500", err)
	}
}

func TestRender_EscapedLegalEntitiesRoundTrip(t *testing.T) {
	in := "a < b & c > d &nbsp; `x` **y**"
	got, err := Render(in, Options{})
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
}
