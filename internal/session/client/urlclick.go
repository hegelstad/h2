package client

import (
	"os/exec"
	"runtime"
	"strconv"
)

// tryOpenLinkAt opens a URL if the cell at the given SGR mouse coordinates
// carries an explicit OSC 8 link or matches an auto-detected URL span.
// Returns true if a URL was found and an open was attempted.
func (c *Client) tryOpenLinkAt(cxStr, cyStr string) bool {
	cx, err1 := strconv.Atoi(cxStr)
	cy, err2 := strconv.Atoi(cyStr)
	if err1 != nil || err2 != nil {
		return false
	}
	url := c.lookupURLAt(cy-1, cx-1) // SGR coords are 1-indexed
	if url == "" {
		return false
	}
	openURL(url)
	return true
}

// lookupURLAt returns the URL associated with the cell at the given terminal
// (row, col) — 0-indexed, where row 0 is the first rendered row of the live
// VT. Returns "" if the cell carries no link. Checks explicit OSC 8 first,
// then falls back to auto-detected URL spans on the visible window.
func (c *Client) lookupURLAt(termRow, col int) string {
	if c.VT == nil || c.VT.Vt == nil {
		return ""
	}
	if termRow < 0 || termRow >= c.VT.ChildRows || col < 0 {
		return ""
	}
	startRow := c.VT.Vt.Cursor.Y - c.VT.ChildRows + 1
	if startRow < 0 {
		startRow = 0
	}
	vtRow := startRow + termRow
	if vtRow < 0 || vtRow >= len(c.VT.Vt.Content) {
		return ""
	}

	// Explicit OSC 8 (URLID in midterm's region) wins.
	pos := 0
	for region := range c.VT.Vt.Format.Regions(vtRow) {
		end := pos + region.Size
		if col >= pos && col < end {
			if region.URLID != 0 {
				if url := c.VT.Vt.URL(region.URLID); url != "" {
					return url
				}
			}
			break
		}
		pos = end
	}

	// Fall back to auto-detected URL spans over the visible window — covers
	// plain-text URLs and the wrap-spanning case where both halves report the
	// full URL.
	rows := collectVTRows(c.VT.Vt, startRow, c.VT.ChildRows)
	spans := detectURLSpans(rows, c.VT.Cols)
	if termRow < len(spans) {
		for _, span := range spans[termRow] {
			if col >= span.startCol && col < span.endCol {
				return span.url
			}
		}
	}
	return ""
}

// handleHover updates HoveredURL based on the cell under the mouse and
// triggers a re-render only when the URL actually changes. Called from the
// SGR motion event path; cheap when the mouse is moving over non-link cells
// (no work past the lookup).
func (c *Client) handleHover(cxStr, cyStr string) {
	cx, err1 := strconv.Atoi(cxStr)
	cy, err2 := strconv.Atoi(cyStr)
	if err1 != nil || err2 != nil {
		return
	}
	url := c.lookupURLAt(cy-1, cx-1)
	if url == c.HoveredURL {
		return
	}
	c.HoveredURL = url
	c.writePointerShape(url != "")
	c.RenderScreen()
}

// writePointerShape emits OSC 22 to set the terminal's mouse pointer to the
// finger ("pointer") when hovering a link cell, or back to the default arrow
// otherwise. Modern terminals (Ghostty, iTerm2, kitty, …) honor OSC 22.
func (c *Client) writePointerShape(asPointer bool) {
	var seq []byte
	if asPointer {
		seq = []byte("\x1b]22;pointer\x1b\\")
	} else {
		seq = []byte("\x1b]22;default\x1b\\")
	}
	c.OutputMu.Lock()
	c.Output.Write(seq)
	c.OutputMu.Unlock()
}

// openURL shells out to the platform's URL opener. Detaches stdio so the
// child doesn't hold the parent's tty.
func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Start()
}
