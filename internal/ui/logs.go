package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/krish-vd/berth/internal/docker"
)

const maxLogLines = 5000

type logBatchMsg struct {
	gen    int
	lines  []docker.LogLine
	err    error
	closed bool
}

type logState struct {
	id     string
	gen    int
	cancel context.CancelFunc
	ch     chan logBatchMsg
	lines  []docker.LogLine
	norm   []normLine // parsed once on arrival, parallel to lines
	total  int        // lines received this stream (for cache keys)
	err    error

	jumpTo time.Time // pending "scroll to this moment" (from an alert)
	mark   time.Time // lines within ±2s of this get a ▶ marker

	scroll    int // display lines up from the bottom; 0 = following
	wrap      bool
	ts        bool
	query     string
	searching bool
	input     textinput.Model
}

func newLogState() logState {
	in := textinput.New()
	in.Prompt = "search ❯ "
	in.Placeholder = "text or regex"
	return logState{wrap: true, ts: true, input: in}
}

func (l *logState) stop() {
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	l.id = ""
}

func (l *logState) start(cli *docker.Client, id string, tty bool) tea.Cmd {
	l.stop()
	l.gen++
	l.id, l.lines, l.norm, l.total, l.err, l.scroll = id, nil, nil, 0, nil, 0
	l.mark = time.Time{}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	ch := make(chan logBatchMsg, 16)
	l.ch = ch
	gen := l.gen

	var mu sync.Mutex
	var buf []docker.LogLine
	done := make(chan error, 1)
	go func() {
		done <- cli.Logs(ctx, id, 500, tty, func(ll docker.LogLine) { mu.Lock(); buf = append(buf, ll); mu.Unlock() })
	}()
	go func() { // batch at ~12Hz so a chatty container can't flood the UI
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case err := <-done:
				mu.Lock()
				b := buf
				mu.Unlock()
				if ctx.Err() == nil {
					ch <- logBatchMsg{gen: gen, lines: b, err: err, closed: true}
				}
				close(ch)
				return
			case <-t.C:
				mu.Lock()
				b := buf
				buf = nil
				mu.Unlock()
				if len(b) > 0 {
					select {
					case ch <- logBatchMsg{gen: gen, lines: b}:
					case <-ctx.Done():
					}
				}
			}
		}
	}()
	return waitLogs(ch)
}

func waitLogs(ch chan logBatchMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (l *logState) receive(msg logBatchMsg) tea.Cmd {
	if msg.gen != l.gen {
		return nil
	}
	added := len(msg.lines)
	l.lines = append(l.lines, msg.lines...)
	for _, ln := range msg.lines {
		lv, text := normalize(ln.Text)
		l.norm = append(l.norm, normLine{lv, text})
	}
	l.total += added
	if len(l.lines) > maxLogLines {
		l.lines = append([]docker.LogLine(nil), l.lines[len(l.lines)-maxLogLines:]...)
		l.norm = append([]normLine(nil), l.norm[len(l.norm)-maxLogLines:]...)
	}
	if l.scroll > 0 {
		l.scroll += added // keep the viewport anchored while reading history
	}
	if !l.jumpTo.IsZero() {
		l.applyJump()
	}
	if msg.closed {
		if msg.err != nil && !strings.Contains(msg.err.Error(), "EOF") {
			l.err = msg.err
		}
		return nil
	}
	return waitLogs(l.ch)
}

// applyJump scrolls so the lines around jumpTo sit in view, with a few
// lines of what happened next below them, and marks them.
func (l *logState) applyJump() {
	if len(l.lines) == 0 {
		return
	}
	after := 0
	for i := len(l.lines) - 1; i >= 0 && l.lines[i].Time.After(l.jumpTo.Add(2*time.Second)); i-- {
		after++
	}
	l.scroll = max(0, after-6)
	l.query = ""
	l.mark, l.jumpTo = l.jumpTo, time.Time{}
}

func (l *logState) beginSearch() {
	l.searching = true
	l.input.SetValue(l.query)
	l.input.CursorEnd()
	l.input.Focus()
}

func (l *logState) searchKey(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		l.searching = false
		l.input.Blur()
		return nil
	case "enter":
		l.searching = false
		l.query = strings.TrimSpace(l.input.Value())
		l.scroll = 0
		l.input.Blur()
		return nil
	}
	var cmd tea.Cmd
	l.input, cmd = l.input.Update(k)
	l.query = strings.TrimSpace(l.input.Value()) // live filter while typing
	return cmd
}

// ---------------------------------------------------------------- rendering

var levelRe = regexp.MustCompile(`(?i)\b(fatal|panic|critical|crit|error|err|warning|warn|info|debug|trace)\b`)

type level int

const (
	lvNone level = iota
	lvTrace
	lvDebug
	lvInfo
	lvWarn
	lvError
)

func parseLevel(s string) level {
	switch strings.ToLower(s) {
	case "fatal", "panic", "critical", "crit", "error", "err", "e":
		return lvError
	case "warning", "warn", "w":
		return lvWarn
	case "info", "notice", "i":
		return lvInfo
	case "debug", "d":
		return lvDebug
	case "trace", "t":
		return lvTrace
	}
	return lvNone
}

var levelBadge = map[level]struct {
	text string
	col  lipgloss.Color
}{
	lvError: {"ERR", cRed}, lvWarn: {"WRN", cYellow}, lvInfo: {"INF", cTeal}, lvDebug: {"DBG", cMuted}, lvTrace: {"TRC", cSubtle}, lvNone: {"   ", cSubtle},
}

type normLine struct {
	lv   level
	text string
}

// normalize turns a raw line into (level, display text). JSON logs are
// flattened to "message  key=value …".
func normalize(raw string) (level, string) {
	s := ansi.Strip(raw)
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
		var obj map[string]any
		if json.Unmarshal([]byte(t), &obj) == nil {
			lv := lvNone
			for _, k := range []string{"level", "lvl", "severity", "log.level"} {
				if v, ok := obj[k].(string); ok {
					lv = parseLevel(v)
					delete(obj, k)
					break
				}
			}
			msg := ""
			for _, k := range []string{"msg", "message", "event"} {
				if v, ok := obj[k]; ok {
					msg = fmt.Sprint(v)
					delete(obj, k)
					break
				}
			}
			for _, k := range []string{"time", "ts", "timestamp", "@timestamp"} {
				delete(obj, k)
			}
			keys := make([]string, 0, len(obj))
			for k := range obj {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var b strings.Builder
			b.WriteString(msg)
			for _, k := range keys {
				v, _ := json.Marshal(obj[k])
				fmt.Fprintf(&b, "  %s=%s", k, strings.Trim(string(v), `"`))
			}
			return lv, b.String()
		}
	}
	if mt := levelRe.FindStringSubmatch(s); mt != nil && levelRe.FindStringIndex(s)[0] < 48 {
		return parseLevel(mt[1]), s
	}
	return lvNone, s
}

func (m Model) viewLogs(c *docker.Container, iw, ih int) []string {
	l := &m.logs
	var q *regexp.Regexp
	if l.query != "" {
		var err error
		q, err = regexp.Compile("(?i)" + l.query)
		if err != nil {
			q = regexp.MustCompile("(?i)" + regexp.QuoteMeta(l.query))
		}
	}

	gutter := 4
	if l.ts {
		gutter += 13
	}
	tw := max(10, iw-gutter-1)

	var out []string
	walked := 0
	need := ih - 2 + l.scroll
	// walk from newest to oldest, building only what's visible
	for i := len(l.lines) - 1; i >= 0 && len(out) < need; i-- {
		ln := l.lines[i]
		lv, text := l.norm[i].lv, l.norm[i].text
		if q != nil && !q.MatchString(text) {
			continue
		}
		walked++
		badge := levelBadge[lv]
		if ln.Stderr && lv == lvNone { // stderr ≠ error (Django, nginx… log there)
			badge.text, badge.col = " ▏ ", Dim(cRed, 0.45)
		}
		base := sText
		switch lv {
		case lvError:
			base = fg(lipgloss.Color("#f5a3b3"))
		case lvWarn:
			base = fg(lipgloss.Color("#ecd2a2"))
		case lvDebug, lvTrace:
			base = sMuted
		}
		var pieces []string
		if l.wrap {
			pieces = strings.Split(ansi.Hardwrap(text, tw, true), "\n")
		} else {
			pieces = []string{ansi.Truncate(text, tw, "…")}
		}
		var block []string
		for j, p := range pieces {
			prefix := strings.Repeat(" ", gutter)
			if j == 0 {
				prefix = " " + fg(badge.col).Bold(true).Render(badge.text)
				if l.ts {
					ts := "            "
					if !ln.Time.IsZero() {
						ts = ln.Time.Local().Format("15:04:05.000")
					}
					prefix = " " + sSubtle.Render(ts) + prefix
				}
				prefix += " "
				if !l.mark.IsZero() && !ln.Time.IsZero() && absDur(ln.Time.Sub(l.mark)) <= 2*time.Second {
					prefix = fg(cRed).Bold(true).Render("▶") + ansi.TruncateLeft(prefix, 1, "")
				}
			} else {
				prefix += sSubtle.Render("↳")
			}
			block = append(block, prefix+highlight(p, q, base))
		}
		for j := len(block) - 1; j >= 0; j-- {
			out = append(out, block[j])
		}
	}
	// how many entries match overall (cached — only recount when data or query change)
	matches := len(l.lines)
	if q != nil {
		key := fmt.Sprint(l.gen, len(l.lines), l.total, l.query)
		if m.cache.matchKey != key {
			n := 0
			for _, nl := range l.norm {
				if q.MatchString(nl.text) {
					n++
				}
			}
			m.cache.matchKey, m.cache.matches = key, n
		}
		matches = m.cache.matches
	}
	// estimated total display rows (entries × average wrapped height)
	total := matches
	if walked > 0 {
		total = int(float64(matches) * float64(len(out)) / float64(walked))
	}
	vh := ih - 2
	m.cache.logMax = max(0, total-vh)

	// out is newest-first; apply scroll and flip
	scroll := min(l.scroll, max(0, len(out)-vh))
	end := min(len(out), scroll+vh)
	vis := append([]string(nil), out[scroll:end]...)
	for i, j := 0, len(vis)-1; i < j; i, j = i+1, j-1 {
		vis[i], vis[j] = vis[j], vis[i]
	}

	state := fg(cGreen).Render("● live")
	if l.scroll > 0 {
		state = fg(cYellow).Render("‖ paused") + sMuted.Render(" · f to follow")
	}
	if l.id != c.ID {
		state = sMuted.Render(string(spinner[m.frame%len(spinner)]) + " connecting")
	}
	head := " " + state + sSubtle.Render("  │  ") + sMuted.Render(fmt.Sprintf("%d lines", len(l.lines)))
	if q != nil {
		head += sSubtle.Render("  │  ") + fg(cYellow).Render("/"+l.query) + sMuted.Render(fmt.Sprintf(" · %d matches", matches))
	}
	right := ""
	if l.wrap {
		right += sMuted.Render("wrap ")
	}
	if l.ts {
		right += sMuted.Render("time ")
	}
	lines := []string{spread(head, right, iw), sSubtle.Render(" " + strings.Repeat("─", iw-2))}
	if l.err != nil {
		lines = append(lines, fg(cRed).Render(" ✗ "+l.err.Error()))
	}
	if len(vis) == 0 && l.id == c.ID {
		msg := "  no output yet"
		if q != nil {
			msg = "  nothing matches /" + l.query
		}
		lines = append(lines, sMuted.Render(msg))
	}
	return append(lines, m.withScrollbar(vis, iw, ih-len(lines), total, scroll, len(lines))...)
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

var hlStyle = fg(lipgloss.Color("#1a1b26")).Background(cYellow).Bold(true)

func highlight(s string, q *regexp.Regexp, base lipgloss.Style) string {
	if q == nil {
		return base.Render(s)
	}
	idx := q.FindAllStringIndex(s, -1)
	if len(idx) == 0 {
		return base.Render(s)
	}
	var b strings.Builder
	p := 0
	for _, r := range idx {
		if r[1] == r[0] {
			continue
		}
		b.WriteString(base.Render(s[p:r[0]]))
		b.WriteString(hlStyle.Render(s[r[0]:r[1]]))
		p = r[1]
	}
	b.WriteString(base.Render(s[p:]))
	return b.String()
}
