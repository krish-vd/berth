package ui

import (
	"math"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// scrollInfo is the geometry of the scrollbar drawn in the last frame, so
// mouse clicks/drags on it can be mapped back to an offset.
type scrollInfo struct {
	active     bool
	total, vis int // content lines, visible lines
	y0         int // first scrollable row inside the panel
	fromBottom bool
}

// scrollbar returns the bar glyph for row i of a vis-row viewport.
func scrollbar(i, total, vis, offTop int) string {
	thumb := max(1, int(math.Round(float64(vis)*float64(vis)/float64(total))))
	pos := 0
	if total > vis {
		pos = int(math.Round(float64(offTop) / float64(total-vis) * float64(vis-thumb)))
	}
	if i >= pos && i < pos+thumb {
		return fg(cAccent).Render("┃")
	}
	return sSubtle.Render("│")
}

// scroll windows body to vis rows at off (from the top), appends a
// scrollbar in the last column when content overflows, and records the
// geometry for mouse handling. y0 is the number of fixed header rows above.
func (m Model) scroll(body []string, off, iw, vis, y0 int) []string {
	if vis <= 0 {
		return nil
	}
	total := len(body)
	maxOff := max(0, total-vis)
	off = clamp(off, 0, maxOff)
	end := min(total, off+vis)
	out := append([]string(nil), body[off:end]...)
	if total <= vis {
		m.cache.sb = scrollInfo{}
		return out
	}
	m.cache.sb = scrollInfo{active: true, total: total, vis: vis, y0: y0}
	for len(out) < vis {
		out = append(out, "")
	}
	for i := range out {
		out[i] = fit(out[i], iw-1) + scrollbar(i, total, vis, off)
	}
	return out
}

// withScrollbar decorates already-windowed rows (used by the bottom-anchored
// log view, which never materialises the whole buffer).
func (m Model) withScrollbar(rows []string, iw, vis, total, offFromBottom, y0 int) []string {
	if total <= vis {
		m.cache.sb = scrollInfo{}
		return rows
	}
	m.cache.sb = scrollInfo{active: true, total: total, vis: vis, y0: y0, fromBottom: true}
	for len(rows) < vis {
		rows = append([]string{""}, rows...)
	}
	offTop := max(0, total-vis-offFromBottom)
	for i := range rows {
		rows[i] = fit(rows[i], iw-1) + scrollbar(i, total, vis, offTop)
	}
	return rows
}

// scrollMouse handles clicks and drags on the detail scrollbar column.
func (m *Model) scrollMouse(msg tea.MouseMsg) bool {
	sb := m.cache.sb
	if !sb.active || msg.Button != tea.MouseButtonLeft || msg.X != m.w-2 {
		return false
	}
	if msg.Action != tea.MouseActionPress && msg.Action != tea.MouseActionMotion {
		return false
	}
	row := msg.Y - (headerHeight + 1 + sb.y0)
	if row < 0 || row >= sb.vis {
		return false
	}
	frac := float64(row) / float64(max(1, sb.vis-1))
	span := sb.total - sb.vis
	if sb.fromBottom {
		m.logs.scroll = int(math.Round((1 - frac) * float64(span)))
	} else {
		m.dScroll = int(math.Round(frac * float64(span)))
	}
	return true
}

// clampDetail keeps dScroll inside the content so scrolling back up responds
// immediately after overshooting the end.
func (m *Model) clampDetail() {
	if sb := m.cache.sb; sb.active && !sb.fromBottom {
		m.dScroll = clamp(m.dScroll, 0, sb.total-sb.vis)
	} else if !sb.active {
		m.dScroll = 0
	}
}

func padRows(rows []string, n int) []string {
	for len(rows) < n {
		rows = append(rows, "")
	}
	return rows
}

var _ = strings.Repeat
