package bridge

import (
	"strings"
	"testing"
)

func TestValidateRichHTML(t *testing.T) {
	const wantErrSubstr = "rich-html body has line breaks but no block tags"

	tests := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{
			name:    "single-line body with no tags",
			text:    "hello telegram",
			wantErr: false,
		},
		{
			name:    "newlines with no tags",
			text:    "hello\nworld",
			wantErr: true,
		},
		{
			name:    "newlines plus br",
			text:    "hello<br>\nworld",
			wantErr: false,
		},
		{
			name:    "newlines plus ul li",
			text:    "<ul>\n<li>one</li>\n<li>two</li>\n</ul>",
			wantErr: false,
		},
		{
			name:    "newlines plus only inline tags",
			text:    "<b>bold</b>\n<code>code</code>\n<a href=\"x\">link</a>",
			wantErr: true,
		},
		{
			name:    "uppercase BR",
			text:    "hello<BR>\nworld",
			wantErr: false,
		},
		{
			// Substring / opening-tag rule, not a full HTML parse: a
			// tag-lookalike in prose still counts as a block tag.
			name:    "tag-lookalike in prose",
			text:    "use <br> to break\nlines",
			wantErr: false,
		},
		{
			name:    "self-closing br",
			text:    "hello<br/>\nworld",
			wantErr: false,
		},
		{
			name:    "closing tag only is not a block tag",
			text:    "hello</p>\nworld",
			wantErr: true,
		},
		{
			name:    "empty body",
			text:    "",
			wantErr: false,
		},
		{
			name:    "single line with inline tags",
			text:    "<b>bold</b> and <code>code</code>",
			wantErr: false,
		},
		{
			// <div> is not a supported rich-html tag. A body whose only
			// structure is <div> would still render as a wall of text.
			name:    "newlines plus unsupported div",
			text:    "<div>hello</div>\n<div>world</div>",
			wantErr: true,
		},
		{
			// Hyphenated tg-* tags must match as a whole name. A word-
			// boundary regex would split on the hyphen and miss these.
			name:    "newlines plus tg-collage",
			text:    "<tg-collage>\n<img src=\"https://example.com/a.jpg\"/>\n</tg-collage>",
			wantErr: false,
		},
		{
			name:    "newlines plus tg-slideshow",
			text:    "<tg-slideshow>\n<img src=\"https://example.com/a.jpg\"/>\n</tg-slideshow>",
			wantErr: false,
		},
		{
			name:    "newlines plus tg-math-block",
			text:    "<tg-math-block>\nE = mc^2\n</tg-math-block>",
			wantErr: false,
		},
		{
			name:    "newlines plus aside",
			text:    "<aside>pull quote</aside>\nmore",
			wantErr: false,
		},
		{
			name:    "newlines plus details summary",
			text:    "<details>\n<summary>title</summary>\ncontent\n</details>",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRichHTML(tt.text)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateRichHTML(%q) = nil, want error", tt.text)
				}
				if !strings.Contains(err.Error(), wantErrSubstr) {
					t.Fatalf("ValidateRichHTML(%q) error = %v, want substring %q", tt.text, err, wantErrSubstr)
				}
			} else if err != nil {
				t.Fatalf("ValidateRichHTML(%q) = %v, want nil", tt.text, err)
			}
		})
	}
}

func TestParseAgentPrefix(t *testing.T) {
	tests := []struct {
		input     string
		wantAgent string
		wantBody  string
	}{
		{"running-deer: check build", "running-deer", "check build"},
		{"agent_1: hello", "agent_1", "hello"},
		{"MyAgent: test", "myagent", "test"},
		{"Concierge: hello", "concierge", "hello"},
		{"no prefix here", "", "no prefix here"},
		{"", "", ""},
		{"agent: body: with: colons", "agent", "body: with: colons"},
		{"agent:no space", "agent", "no space"},
		{"agent:  extra spaces", "agent", "extra spaces"},
		{": empty agent", "", ": empty agent"},
		{"misc-sand: first line\n\nsecond paragraph", "misc-sand", "first line\n\nsecond paragraph"},
		{"agent: line1\nline2\nline3", "agent", "line1\nline2\nline3"},
	}

	for _, tt := range tests {
		agent, body := ParseAgentPrefix(tt.input)
		if agent != tt.wantAgent || body != tt.wantBody {
			t.Errorf("ParseAgentPrefix(%q) = (%q, %q), want (%q, %q)",
				tt.input, agent, body, tt.wantAgent, tt.wantBody)
		}
	}
}

func TestParseAgentTag(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"[researcher] here are the results", "researcher"},
		{"[my-agent] hello", "my-agent"},
		{"[agent_1] test", "agent_1"},
		{"no tag here", ""},
		{"", ""},
		{"[researcher]no space", "researcher"},
		{"[researcher]  extra spaces", "researcher"},
		{"[] empty tag", ""},
		{"plain [researcher] not at start", ""},
	}

	for _, tt := range tests {
		got := ParseAgentTag(tt.input)
		if got != tt.want {
			t.Errorf("ParseAgentTag(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatAgentTag(t *testing.T) {
	got := FormatAgentTag("researcher", "here are the results")
	want := "[researcher] here are the results"
	if got != want {
		t.Errorf("FormatAgentTag = %q, want %q", got, want)
	}
}

func TestParseSlashCommand(t *testing.T) {
	tests := []struct {
		text    string
		allowed []string
		wantCmd string
		wantArg string
	}{
		{"/h2 list", []string{"h2"}, "h2", "list"},
		{"/bd create \"my issue\"", []string{"h2", "bd"}, "bd", "create \"my issue\""},
		{"/h2", []string{"h2"}, "h2", ""},
		{"/notallowed foo", []string{"h2"}, "", ""},
		{"hello", []string{"h2"}, "", ""},
		{"concierge: /h2 list", []string{"h2"}, "", ""},
		{"/h2 list", nil, "", ""},
		{"/H2 list", []string{"h2"}, "", ""},
		{"/h2   ", []string{"h2"}, "h2", ""},
	}

	for _, tt := range tests {
		cmd, args := ParseSlashCommand(tt.text, tt.allowed)
		if cmd != tt.wantCmd || args != tt.wantArg {
			t.Errorf("ParseSlashCommand(%q, %v) = (%q, %q), want (%q, %q)",
				tt.text, tt.allowed, cmd, args, tt.wantCmd, tt.wantArg)
		}
	}
}

func TestStripH2Envelope(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			"[h2 message from: concierge] build complete",
			"build complete",
		},
		{
			"[URGENT h2 message from: running-deer] server down",
			"server down",
		},
		{
			"no envelope here",
			"no envelope here",
		},
		{
			"",
			"",
		},
		{
			"[h2 message from: agent] Read /some/path",
			"Read /some/path",
		},
		{
			"[h2 message from: agent]   extra whitespace  ",
			"extra whitespace",
		},
		{
			"[h2 trigger (on state_change, firing 2 of 5)] check status",
			"check status",
		},
		{
			"[h2 schedule (daily-check)] run diagnostics",
			"run diagnostics",
		},
	}

	for _, tt := range tests {
		got := StripH2Envelope(tt.input)
		if got != tt.want {
			t.Errorf("StripH2Envelope(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
