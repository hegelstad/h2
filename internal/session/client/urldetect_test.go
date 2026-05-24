package client

import (
	"strings"
	"testing"
)

// padRow returns a []rune of exactly cols length: content + trailing spaces.
func padRow(s string, cols int) []rune {
	r := []rune(s)
	if len(r) < cols {
		pad := make([]rune, cols-len(r))
		for i := range pad {
			pad[i] = ' '
		}
		r = append(r, pad...)
	}
	return r
}

func TestDetectURLSpans_BasicSingleRow(t *testing.T) {
	rows := [][]rune{padRow("see https://example.com", 40)}
	spans := detectURLSpans(rows, 40)
	if len(spans[0]) != 1 {
		t.Fatalf("want 1 span, got %d: %+v", len(spans[0]), spans[0])
	}
	if got := spans[0][0].url; got != "https://example.com" {
		t.Errorf("url = %q, want https://example.com", got)
	}
	if spans[0][0].startCol != 4 || spans[0][0].endCol != 23 {
		t.Errorf("cols = (%d,%d), want (4,23)", spans[0][0].startCol, spans[0][0].endCol)
	}
}

func TestDetectURLSpans_MultipleURLsOnRow(t *testing.T) {
	rows := [][]rune{padRow("a https://x.com b https://y.com c", 60)}
	spans := detectURLSpans(rows, 60)
	if len(spans[0]) != 2 {
		t.Fatalf("want 2 spans, got %d", len(spans[0]))
	}
	if spans[0][0].url != "https://x.com" || spans[0][1].url != "https://y.com" {
		t.Errorf("urls = %v", spans[0])
	}
}

func TestDetectURLSpans_NoURL(t *testing.T) {
	rows := [][]rune{padRow("just some text", 40)}
	spans := detectURLSpans(rows, 40)
	if len(spans[0]) != 0 {
		t.Fatalf("want 0 spans, got %+v", spans[0])
	}
}

func TestDetectURLSpans_WrapsAcrossTwoRows(t *testing.T) {
	// Build a URL that fills row 0 exactly to the last col and continues.
	// cols=30. URL = "https://example.com/path/that-is-long-enough-to-wrap"
	url := "https://example.com/path/that-is-long-enough-to-wrap"
	cols := 30
	first := url[:cols]  // fills row 0 exactly
	second := url[cols:] // continues at start of row 1
	rows := [][]rune{
		[]rune(first),                            // row 0: exactly cols wide, last char is URL char
		padRow(second+" trailing text", cols+50), // row 1: continues then breaks on space
	}
	spans := detectURLSpans(rows, cols)
	if len(spans[0]) != 1 || len(spans[1]) != 1 {
		t.Fatalf("expected 1 span per row, got %+v / %+v", spans[0], spans[1])
	}
	if spans[0][0].url != url {
		t.Errorf("row0 url = %q, want full %q", spans[0][0].url, url)
	}
	if spans[1][0].url != url {
		t.Errorf("row1 url = %q, want full %q", spans[1][0].url, url)
	}
	if spans[0][0].startCol != 0 || spans[0][0].endCol != cols {
		t.Errorf("row0 cols = (%d,%d), want (0,%d)", spans[0][0].startCol, spans[0][0].endCol, cols)
	}
	if spans[1][0].startCol != 0 || spans[1][0].endCol != len(second) {
		t.Errorf("row1 cols = (%d,%d), want (0,%d)", spans[1][0].startCol, spans[1][0].endCol, len(second))
	}
}

func TestDetectURLSpans_WrapsAcrossThreeRows(t *testing.T) {
	cols := 20
	url := "https://example.com/" + strings.Repeat("a", cols*2) + "/end"
	first := url[:cols]
	second := url[cols : cols*2]
	third := url[cols*2:]
	rows := [][]rune{
		[]rune(first),
		[]rune(second),
		padRow(third+" tail", cols+20),
	}
	spans := detectURLSpans(rows, cols)
	for i, rowSpans := range spans {
		if len(rowSpans) != 1 {
			t.Fatalf("row %d: want 1 span, got %+v", i, rowSpans)
		}
		if rowSpans[0].url != url {
			t.Errorf("row %d: url = %q, want full %q", i, rowSpans[0].url, url)
		}
	}
}

func TestDetectURLSpans_NoFalseJoinOnSpacesBetweenRows(t *testing.T) {
	// URL on row 0 ends at last col, but row 1 starts with space → no extend.
	cols := 30
	rows := [][]rune{
		[]rune("https://x.com/" + strings.Repeat("a", cols-14)), // fills row exactly
		padRow(" continued text not part of url", cols+10),
	}
	if len(rows[0]) != cols {
		t.Fatalf("row 0 setup wrong: len=%d cols=%d", len(rows[0]), cols)
	}
	spans := detectURLSpans(rows, cols)
	if len(spans[0]) != 1 {
		t.Fatalf("want 1 span on row 0, got %+v", spans[0])
	}
	// Row 1 starts with space so no continuation
	if len(spans[1]) != 0 {
		t.Errorf("want 0 spans on row 1 (space starts), got %+v", spans[1])
	}
	// Row 0 URL should NOT include any row-1 content.
	wantPrefix := "https://x.com/"
	if !strings.HasPrefix(spans[0][0].url, wantPrefix) || strings.Contains(spans[0][0].url, "continued") {
		t.Errorf("row0 url leaked into row1: %q", spans[0][0].url)
	}
}

func TestDetectURLSpans_NoFalseJoinWhenRow0NotFullWidth(t *testing.T) {
	// URL ends before last col → no continuation attempted even if row1 starts
	// with URL-like chars.
	cols := 40
	rows := [][]rune{
		padRow("https://x.com short", cols),
		padRow("continuation-looking-text", cols),
	}
	spans := detectURLSpans(rows, cols)
	if len(spans[0]) != 1 {
		t.Fatalf("want 1 span on row 0, got %+v", spans[0])
	}
	if spans[0][0].url != "https://x.com" {
		t.Errorf("row0 url over-extended: %q", spans[0][0].url)
	}
	if len(spans[1]) != 0 {
		t.Errorf("row1 should have no spans, got %+v", spans[1])
	}
}

func TestDetectURLSpans_EmptyRows(t *testing.T) {
	rows := [][]rune{nil, nil, padRow("see https://a.com", 40), nil}
	spans := detectURLSpans(rows, 40)
	if len(spans[2]) != 1 {
		t.Fatalf("row 2 should have 1 span, got %+v", spans[2])
	}
	for _, i := range []int{0, 1, 3} {
		if len(spans[i]) != 0 {
			t.Errorf("row %d should be empty, got %+v", i, spans[i])
		}
	}
}
