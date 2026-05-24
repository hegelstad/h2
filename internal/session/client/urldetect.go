package client

import "regexp"

// urlSpan describes one auto-detected URL on a rendered row.
// startCol is inclusive, endCol exclusive (one past the last URL cell).
type urlSpan struct {
	startCol int
	endCol   int
	url      string
}

// urlStartRegex matches URLs beginning with http:// or https://. The
// character class is the conservative URI set (RFC 3986 unreserved+reserved
// plus '%'), with brackets escaped for regex.
var urlStartRegex = regexp.MustCompile(`https?://[A-Za-z0-9\-._~:/?#\[\]@!$&'()*+,;=%]+`)

// urlContRegex matches a URL-character prefix at the start of a row, used to
// extend a URL whose first row reached the end-of-row boundary (soft-wrap
// candidate).
var urlContRegex = regexp.MustCompile(`^[A-Za-z0-9\-._~:/?#\[\]@!$&'()*+,;=%]+`)

// detectURLSpans scans the given rune rows for URL-shaped substrings and
// returns per-row spans suitable for the renderer to wrap in OSC 8. cols is
// the terminal width: a URL on row i that fills the row to col cols-1 is
// treated as a soft-wrap candidate and extended into row i+1's leading URL
// chars (and further, as long as continuation rows are also full-width).
//
// When a URL spans rows, every row's span carries the FULL concatenated URL,
// so clicking either half opens the complete link rather than a truncated
// prefix.
//
// Heuristic note: midterm doesn't expose per-row soft-wrap state, so "URL
// touches the last col of a full-width row" is the only signal we have. A
// row of plain text that happens to be exactly full-width and ends with a
// URL-shaped tail, followed by more URL-shaped chars, would be over-joined.
// In practice this is rare — URLs almost never land exactly at a cell
// boundary by accident.
func detectURLSpans(rows [][]rune, cols int) [][]urlSpan {
	n := len(rows)
	out := make([][]urlSpan, n)
	for i := 0; i < n; i++ {
		if len(rows[i]) == 0 {
			continue
		}
		rowStr := string(rows[i])
		for _, m := range urlStartRegex.FindAllStringIndex(rowStr, -1) {
			startCol, endCol := m[0], m[1]
			url := rowStr[startCol:endCol]

			type cont struct {
				row, endCol int
			}
			var conts []cont
			// Soft-wrap continuation: URL extends to the last col of a
			// full-width row, and the next row starts with URL-chars.
			if endCol == len(rows[i]) && len(rows[i]) >= cols {
				for j := i + 1; j < n; j++ {
					if len(rows[j]) == 0 {
						break
					}
					nextStr := string(rows[j])
					cm := urlContRegex.FindStringIndex(nextStr)
					if cm == nil {
						break
					}
					url += nextStr[:cm[1]]
					conts = append(conts, cont{j, cm[1]})
					// URL terminates within this row if the prefix didn't
					// fill the row, or if the row isn't full-width.
					if cm[1] < len(rows[j]) || len(rows[j]) < cols {
						break
					}
				}
			}

			out[i] = append(out[i], urlSpan{startCol, endCol, url})
			for _, c := range conts {
				out[c.row] = append(out[c.row], urlSpan{0, c.endCol, url})
			}
		}
	}
	return out
}
