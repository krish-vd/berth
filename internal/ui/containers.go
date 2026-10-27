package ui

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/krish-vd/berth/internal/docker"
	"github.com/krish-vd/berth/internal/metrics"
)

// ---------------------------------------------------------------- status glyphs

type glyph struct {
	r     string
	color lipgloss.Color
}

func (m Model) status(c *docker.Container) glyph {
	if _, ok := m.pending[c.Name()]; ok {
		return glyph{string(spinner[m.frame%len(spinner)]), cAccent}
	}
	switch c.State {
	case "running":
		switch c.Health() {
		case "healthy":
			return glyph{"●", cGreen}
		case "unhealthy":
			return glyph{"●", Pulse(cRed, m.frame)}
		case "starting":
			return glyph{string([]rune("◐◓◑◒")[(m.frame/3)%4]), cYellow}
		}
		return glyph{"●", cTeal}
	case "paused":
		return glyph{"‖", cYellow}
	case "restarting":
		return glyph{string([]rune("◐◓◑◒")[(m.frame/2)%4]), cYellow}
	case "dead":
		return glyph{"✗", cRed}
	}
	return glyph{"○", cMuted}
}

var stateColor = map[string]lipgloss.Color{
	"healthy": cGreen, "running": cTeal, "starting": cYellow, "unhealthy": cRed,
	"restarting": cYellow, "paused": cAccent2, "exited": cMuted, "created": cMuted, "dead": cRed, "removing": cYellow,
}

// ---------------------------------------------------------------- list

// viewList is cached: it only changes with new samples, selection, layout
// or an animation frame (spinners/pulses), not on every redraw.
func (m Model) viewList(w, h int) string {
	frame := 0 // only spinners/pulses in the list need per-frame redraws
	if len(m.pending) > 0 {
		frame = m.frame
	} else {
		for _, c := range m.snap.Containers {
			if c.State == "restarting" || c.Health() == "starting" || c.Health() == "unhealthy" {
				frame = m.frame
				break
			}
		}
	}
	key := fmt.Sprint(m.snap.At.UnixNano(), m.cursor, m.offset, m.focus, w, h, m.zoom, m.filter.Value(),
		m.sortMode, m.hideStopped, len(m.pending), len(m.alerts), frame, m.selID)
	if m.cache.listKey == key {
		return m.cache.list
	}
	out := m.renderList(w, h)
	m.cache.listKey, m.cache.list = key, out
	return out
}

func (m Model) renderList(w, h int) string {
	iw := w - 2
	vis := h - 2
	compact := m.zoom == 1
	tailW := 22 // "100.0% ▁▂▃▄▅▆▇█   1.2G"
	if compact {
		tailW = 7
	}
	nameW := iw - 4 - tailW // edge + glyph + 2 spaces
	focused := m.focus == 0
	var lines []string
	for i := m.offset; i < len(m.rows) && len(lines) < vis; i++ {
		r := m.rows[i]
		if r.c == nil {
			head := " " + fg(cAccent2).Bold(true).Render("▾ "+r.group) + " " + sMuted.Render(fmt.Sprint(r.count)) + " "
			lines = append(lines, head+sSubtle.Render(strings.Repeat("─", max(0, iw-lipgloss.Width(head)-1))))
			continue
		}
		c := r.c
		sel := i == m.cursor
		g := m.status(c)
		ns := sText
		if sel {
			ns = fg(lipgloss.Color("#e6ecff")).Bold(true)
		}
		name := ns.Render(ansi.Truncate(c.Name(), nameW, "…"))
		if n := m.recentCrashes(c.ID); n > 0 && lipgloss.Width(name)+4 <= nameW {
			name += " " + fg(cRed).Render(fmt.Sprintf("✗%d", n))
		}
		name = fit(name, nameW)
		gl := fg(g.color).Render(g.r)
		var tail string
		running := c.State == "running" || c.State == "paused"
		a := m.anims[c.ID]
		cpu, mem := 0.0, 0.0
		if a != nil {
			cpu, mem = a.cpu.Cur, a.mem.Cur
		}
		switch {
		case running && compact:
			tail = heatText(fmt.Sprintf("%6.1f%%", cpu), cpu/100)
		case running:
			hist := m.smp.History(c.ID, metrics.CPU, 8)
			tail = heatText(fmt.Sprintf("%5.1f%%", cpu), cpu/100) + " " + Sparkline(hist, 8, math.Max(100, maxOf(hist))) + " " +
				sText.Render(fmt.Sprintf("%6s", HumanBytes(mem)))
		default:
			tail = sMuted.Render(fmt.Sprintf("%*s", tailW, ansi.Truncate(shortStatus(c.Status), tailW, "…")))
		}
		content := gl + " " + name + " " + tail
		if sel {
			lines = append(lines, m.selRow(content, iw, focused))
		} else {
			lines = append(lines, " "+content)
		}
	}
	if len(m.rows) == 0 {
		lines = append(lines, "", sMuted.Render("  no containers match"))
	}
	title := sBold.Render("Containers")
	right := sMuted.Render(fmt.Sprint(len(m.snap.Containers)))
	if q := m.filter.Value(); q != "" {
		right = fg(cYellow).Render("/"+q) + sSubtle.Render(" · ") + right
	}
	if m.hideStopped {
		right = sMuted.Render("running only") + sSubtle.Render(" · ") + right
	}
	if m.sortMode != 0 {
		right = sMuted.Render("↓"+sortNames[m.sortMode]) + sSubtle.Render(" · ") + right
	}
	return panel(title, right, lines, w, h, focused)
}

func shortStatus(s string) string {
	s = strings.Replace(s, " ago", "", 1)
	return strings.Replace(s, "Exited ", "exited ", 1)
}

// ---------------------------------------------------------------- detail chrome

// tabBar returns the rendered tab strip plus each tab's [start,end) cell
// offsets relative to the strip start (used for mouse hit-testing).
func (m Model) tabBar() (string, [][2]int) {
	var b strings.Builder
	var zones [][2]int
	x := 0
	for i, n := range tabNames {
		if i > 0 {
			b.WriteString(sSubtle.Render(" · "))
			x += 3
		}
		w := lipgloss.Width(n)
		if Tab(i) == m.tab {
			b.WriteString(withBg(sAccent.Render(n), cSelBg, w))
		} else {
			b.WriteString(sMuted.Render(n))
		}
		zones = append(zones, [2]int{x, x + w})
		x += w
	}
	return b.String(), zones
}

func (m Model) windowBar() (string, [][2]int) {
	var b strings.Builder
	var zones [][2]int
	x := 0
	for i, wv := range windows {
		if i > 0 {
			b.WriteString(sSubtle.Render(" · "))
			x += 3
		}
		w := len(wv.label)
		if i == m.winIdx {
			b.WriteString(withBg(sAccent.Render(wv.label), cSelBg, w))
		} else {
			b.WriteString(sMuted.Render(wv.label))
		}
		zones = append(zones, [2]int{x, x + w})
		x += w
	}
	return b.String(), zones
}

func (m Model) detailTitle(c *docker.Container) (title, right string) {
	tabs, _ := m.tabBar()
	title = sBold.Render(ansi.Truncate(c.Name(), 28, "…")) + "  " + tabs
	if m.tab == TMetrics {
		right, _ = m.windowBar()
	}
	return
}

// hitDetailChrome handles clicks on the detail panel's top border.
func (m *Model) hitDetailChrome(x int) bool {
	c := m.selected()
	if c == nil || m.view != VContainers {
		return false
	}
	lw := m.listWidth()
	w := m.w - lw
	name := lipgloss.Width(ansi.Truncate(c.Name(), 28, "…"))
	start := lw + 3 + name + 2 // "╭─ " + name + "  "
	_, tz := m.tabBar()
	for i, z := range tz {
		if x >= start+z[0] && x < start+z[1] {
			m.setTab(Tab(i))
			m.focus = 1
			return true
		}
	}
	if m.tab == TMetrics {
		bar, wz := m.windowBar()
		rstart := lw + w - 2 - 1 - lipgloss.Width(bar) // " right " then "─╮"
		for i, z := range wz {
			if x >= rstart+z[0] && x < rstart+z[1] {
				m.winIdx = i
				return true
			}
		}
	}
	return false
}

// hitHeaderTabs handles clicks on the header's view tabs.
func (m *Model) hitHeaderTabs(x int) bool {
	pos := 3 + lipgloss.Width("◆ berth") + 2
	for i, n := range viewNames {
		w := 1 + 1 + 1 + len(n) + 1 // " k name "
		if i == int(m.view) {
			w = 1 + len(n) + 2 + 1
		}
		if x >= pos && x < pos+w {
			m.setView(View(i))
			return true
		}
		pos += w
	}
	return false
}

func (m Model) viewDetail(w, h int) string {
	c := m.selected()
	if c == nil {
		return panel(sBold.Render("—"), "", []string{"", sMuted.Render("  no container selected")}, w, h, false)
	}
	iw, ih := w-2, h-2
	var lines []string
	switch m.tab {
	case TMetrics:
		lines = m.viewMetrics(c, iw, ih)
	case TLogs:
		lines = m.viewLogs(c, iw, ih)
	case TEnv:
		lines = m.viewEnv(c, iw, ih)
	case TConfig:
		lines = m.viewConfig(c, iw, ih)
	case TInspect:
		lines = m.viewRaw("container", c.ID, iw, ih)
	case TTop:
		lines = m.viewTop(c, iw, ih)
	}
	title, right := m.detailTitle(c)
	return panel(title, right, lines, w, h, m.focus == 1)
}

// ---------------------------------------------------------------- metrics tab

func (m Model) viewMetrics(c *docker.Container, iw, ih int) []string {
	win := windows[m.winIdx]
	cur := m.smp.Current(c.ID)
	a := m.anims[c.ID]
	if a == nil {
		a = &anim{}
	}
	g := m.status(c)
	badge := fg(g.color).Render(g.r + " " + stateOf(c))
	lines := []string{
		" " + badge + sSubtle.Render("  │  ") + sText.Render(ansi.Truncate(c.Image, max(10, iw/2), "…")) + sSubtle.Render("  │  ") + sMuted.Render(c.Status),
		"",
	}
	// top(2) + cpu/mem label+axis(4) + io label(1) + blank(1) + state(1) + info(1)
	ioH := clamp((ih-10)/6, 1, 4)
	chartH := max(2, (ih-10-ioH)/2)
	axisW := 7
	cw := iw - axisW - 1

	// Charts only change when new samples land (1/s) — render them once per
	// snapshot and reuse across animation frames.
	key := fmt.Sprint(c.ID, m.winIdx, iw, ih, m.snap.At.UnixNano(), len(m.timeline[c.ID]))
	if m.cache.key != key {
		m.cache.key = key
		m.cache.blk = m.chartBlock(c, cur, win.secs, win.label, iw, cw, chartH, ioH, axisW)
	}
	b := m.cache.blk
	// copy chart rows so hover decorations never touch the cache
	cpuRows := append([]string(nil), b.cpu...)
	memRows := append([]string(nil), b.mem...)
	ioRows := append([]string(nil), b.io...)

	cpuNow := heatText(fmt.Sprintf("%.1f%%", a.cpu.Cur), a.cpu.Cur/100) + b.cpuStats
	memNow := heatText(HumanBytes(a.mem.Cur), cur.MemPct()/100) + b.memStats

	// hover readouts — geometry mirrors the layout below
	px, py := m.listWidth()+1, headerHeight+1 // first inner cell of this panel
	cpuTop := py + 3
	memTop := cpuTop + chartH + 2
	ioTop := memTop + chartH + 1 // mini label row
	hx, hy := m.hoverX-px, m.hoverY
	pct := func(v float64) string { return fmt.Sprintf("%.1f%%", v) }
	if t, v, ok := hoverAt(b.cpuHist, cw, hx-(axisW+1), hy-cpuTop, chartH, win.secs); ok {
		cpuNow = tooltip(t, pct(v), v/100)
		crosshair(cpuRows[:chartH], axisW+1+hx-(axisW+1))
	}
	if t, v, ok := hoverAt(b.memHist, cw, hx-(axisW+1), hy-memTop, chartH, win.secs); ok {
		cpuMemT := 0.0
		if cur.MemLimit > 0 {
			cpuMemT = v / float64(cur.MemLimit)
		}
		memNow = tooltip(t, HumanBytes(v), cpuMemT)
		crosshair(memRows[:chartH], hx)
	}
	for j, hist := range b.minis {
		x0 := j*(b.qw+1) + 1
		if t, v, ok := hoverAt(hist, b.qw-1, hx-x0, hy-(ioTop+1), ioH, win.secs); ok {
			tip := " " + fg(cAccent).Render("⌖") + sText.Render(t.Format(" 15:04:05 ")) + heatText(HumanRate(v), 0.35)
			ioRows[0] = spliceCells(ioRows[0], j*(b.qw+1), b.qw, fit(tip, b.qw))
			crosshair(ioRows[1:], hx)
		}
	}

	lines = append(lines, spread(" "+sMuted.Render("CPU"), cpuNow+" ", iw))
	lines = append(lines, cpuRows...)
	lines = append(lines, spread(" "+sMuted.Render("MEMORY"), memNow+" ", iw))
	lines = append(lines, memRows...)
	lines = append(lines, ioRows...)
	lines = append(lines, "", b.state)
	return append(lines, m.infoLine(c, cur))
}

// hoverAt maps a pointer cell (col,row relative to a chart's top-left) to the
// sample under it: its wall-clock time and value. Charts are right-aligned,
// newest sample at the right edge; each cell holds two braille dots.
func hoverAt(hist []float64, w, col, row, h, secs int) (time.Time, float64, bool) {
	if col < 0 || col >= w || row < 0 || row >= h || len(hist) == 0 {
		return time.Time{}, 0, false
	}
	dw := w * 2
	d := col*2 + 1 // right-hand dot of the cell
	idx := d - (dw - len(hist))
	if idx < 0 {
		return time.Time{}, 0, false // no data that far back yet
	}
	idx = min(idx, len(hist)-1)
	ago := time.Duration(float64(dw-1-d) * float64(secs) / float64(dw) * float64(time.Second))
	return time.Now().Add(-ago), hist[idx], true
}

func tooltip(t time.Time, val string, heat float64) string {
	return fg(cAccent).Render("⌖ ") + sText.Render(t.Format("15:04:05")) + sSubtle.Render("  ·  ") + heatText(val, heat)
}

var crossStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Bold(true)

// crosshair brightens column x in every row: data cells glow white, empty
// cells get a faint vertical guide.
func crosshair(rows []string, x int) {
	for i, r := range rows {
		cell := ansi.Strip(ansi.TruncateLeft(ansi.Truncate(r, x+1, ""), x, ""))
		mark := fg(Dim(cAccent, 0.45)).Render("┊")
		if cell != "" && cell != " " && cell != "⠀" {
			mark = crossStyle.Render(cell)
		}
		rows[i] = spliceCells(r, x, 1, mark)
	}
}

// spliceCells replaces w cells at x in an ANSI string with repl.
func spliceCells(s string, x, w int, repl string) string {
	return ansi.Truncate(s, x, "") + "\x1b[0m" + repl + "\x1b[0m" + ansi.TruncateLeft(s, x+w, "")
}

type chartBlock struct {
	cpu, mem, io       []string
	cpuStats, memStats string
	state              string

	// raw series behind each chart, for hover readouts
	cpuHist, memHist []float64
	minis            [][]float64
	qw               int
}

type renderCache struct {
	key string
	blk chartBlock
	sb  scrollInfo // detail-pane scrollbar geometry from the last frame

	listKey, list string // rendered container list

	matchKey string // log search match-count cache
	matches  int
	logMax   int // furthest the log view can scroll up
}

func (m Model) chartBlock(c *docker.Container, cur metrics.Current, secs int, label string, iw, cw, chartH, ioH, axisW int) chartBlock {
	var b chartBlock
	cpuHist := resample(m.smp.History(c.ID, metrics.CPU, secs), secs, cw*2)
	b.cpuHist = cpuHist
	cpuCeil := niceMax(math.Max(maxOf(cpuHist)*1.15, 5))
	b.cpuStats = sMuted.Render(fmt.Sprintf("  avg %.1f%%  peak %.1f%%", mean(cpuHist), maxOf(cpuHist)))
	b.cpu = append(chartWithAxis(cpuHist, cw, chartH, cpuCeil, math.Min(1, cpuCeil/100), func(v float64) string { return fmt.Sprintf("%.0f%%", v) }), timeAxis(label, axisW, cw))

	memHist := resample(m.smp.History(c.ID, metrics.Mem, secs), secs, cw*2)
	b.memHist = memHist
	memCeil := niceMax(math.Max(maxOf(memHist)*1.2, 1<<20))
	heat := 0.0
	if cur.MemLimit > 0 {
		heat = math.Min(1, memCeil/float64(cur.MemLimit))
	}
	b.memStats = sMuted.Render(" / " + HumanBytes(float64(cur.MemLimit)) + fmt.Sprintf("  %.1f%%  peak %s", cur.MemPct(), HumanBytes(maxOf(memHist))))
	b.mem = append(chartWithAxis(memHist, cw, chartH, memCeil, heat, func(v float64) string { return HumanBytes(v) }), timeAxis(label, axisW, cw))

	qw := (iw - 3) / 4
	b.qw = qw
	mini := func(label string, met metrics.Metric, col lipgloss.Color) []string {
		hist := resample(m.smp.History(c.ID, met, secs), secs, (qw-1)*2)
		b.minis = append(b.minis, hist)
		v := last(m.smp.History(c.ID, met, 1))
		out := []string{fit(" "+fg(col).Render(label)+" "+sText.Render(HumanRate(v)), qw)}
		ceil := niceMax(math.Max(maxOf(hist)*1.2, 1024))
		for _, l := range AreaChart(hist, qw-1, ioH, ceil, 0.35) {
			out = append(out, " "+l)
		}
		return out
	}
	cols := [][]string{
		mini("↓ net rx", metrics.NetRx, cCyan),
		mini("↑ net tx", metrics.NetTx, cAccent2),
		mini("◇ disk read", metrics.BlkRead, cTeal),
		mini("◆ disk write", metrics.BlkWrite, cYellow),
	}
	for i := 0; i <= ioH; i++ {
		parts := make([]string, len(cols))
		for j, col := range cols {
			if i < len(col) {
				parts[j] = fit(col[i], qw)
			} else {
				parts[j] = strings.Repeat(" ", qw)
			}
		}
		b.io = append(b.io, strings.Join(parts, " "))
	}
	b.state = sMuted.Render(fmt.Sprintf("%6s ", "state")) + " " + m.stateStrip(c.ID, secs, cw)
	return b
}

func (m Model) infoLine(c *docker.Container, cur metrics.Current) string {
	ports := []string{}
	for _, p := range c.Ports {
		if p.PublicPort > 0 {
			ports = append(ports, fmt.Sprintf("%d→%d", p.PublicPort, p.PrivatePort))
		}
	}
	sort.Strings(ports)
	ports = dedupe(ports)
	ps := "none"
	if len(ports) > 0 {
		ps = strings.Join(ports, " ")
	}
	restarts := "—"
	if ins := m.insp[c.ID]; ins != nil {
		restarts = fmt.Sprint(ins.RestartCount)
	}
	sep := sSubtle.Render("  │  ")
	return " " + sMuted.Render("ports ") + sText.Render(ps) + sep + sMuted.Render("pids ") + sText.Render(fmt.Sprint(cur.Pids)) +
		sep + sMuted.Render("restarts ") + sText.Render(restarts) + sep + sMuted.Render("id ") + sText.Render(c.ID[:12])
}

// stateStrip colours each cell by the container's state at that moment.
func (m Model) stateStrip(id string, secs, w int) string {
	tl := m.timeline[id]
	now := time.Now()
	var b strings.Builder
	prev := ""
	var run strings.Builder
	flush := func() {
		if run.Len() == 0 {
			return
		}
		if prev == "" {
			b.WriteString(sSubtle.Render(run.String()))
		} else {
			b.WriteString(fg(stateColor[prev]).Render(run.String()))
		}
		run.Reset()
	}
	for i := 0; i < w; i++ {
		t := now.Add(-time.Duration(float64(secs)*(1-(float64(i)+0.5)/float64(w))) * time.Second)
		st := ""
		for _, s := range tl {
			if !s.at.After(t) {
				st = s.state
			}
		}
		if st != prev {
			flush()
			prev = st
		}
		switch st {
		case "":
			run.WriteString("·")
		case "exited", "created", "dead":
			run.WriteString("─")
		default:
			run.WriteString("━")
		}
	}
	flush()
	return b.String()
}

func chartWithAxis(vals []float64, w, h int, ceil, heat float64, f func(float64) string) []string {
	rows := AreaChart(vals, w, h, ceil, heat)
	out := make([]string, len(rows))
	for i, r := range rows {
		lab, tick := "", "│"
		switch {
		case i == 0:
			lab, tick = f(ceil), "┤"
		case i == len(rows)/2 && h >= 4:
			lab, tick = f(ceil/2), "┤"
		}
		out[i] = sMuted.Render(fmt.Sprintf("%6s ", lab)) + sSubtle.Render(tick) + r
	}
	return out
}

func timeAxis(label string, axisW, w int) string {
	l := "-" + label
	gap := max(1, w-len(l)-3)
	return strings.Repeat(" ", axisW) + sSubtle.Render("╰"+l+strings.Repeat("─", gap)+"now")
}

// resample maps a history (1 sample/sec) onto n dots across a window of
// `secs` seconds. Partial history fills only the right-hand portion.
func resample(vals []float64, secs, n int) []float64 {
	if n <= 0 || len(vals) == 0 {
		return nil
	}
	target := clamp(int(math.Round(float64(n)*float64(len(vals))/float64(secs))), 1, n)
	if target == len(vals) {
		return vals
	}
	out := make([]float64, target)
	if len(vals) > target {
		for i := range out {
			a := i * len(vals) / target
			b := max(a+1, (i+1)*len(vals)/target)
			out[i] = maxOf(vals[a:b])
		}
		return out
	}
	for i := range out {
		if target == 1 {
			out[i] = vals[len(vals)-1]
			continue
		}
		p := float64(i) * float64(len(vals)-1) / float64(target-1)
		j := int(p)
		if j >= len(vals)-1 {
			out[i] = vals[len(vals)-1]
			continue
		}
		f := p - float64(j)
		out[i] = vals[j]*(1-f) + vals[j+1]*f
	}
	return out
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// ---------------------------------------------------------------- env tab

var secretWords = []string{"PASS", "SECRET", "TOKEN", "KEY", "CREDENTIAL", "PRIVATE", "AUTH"}

func isSecret(k string) bool {
	u := strings.ToUpper(k)
	for _, w := range secretWords {
		if strings.Contains(u, w) {
			return true
		}
	}
	return false
}

func (m Model) viewEnv(c *docker.Container, iw, ih int) []string {
	ins := m.insp[c.ID]
	if ins == nil {
		return []string{"", sMuted.Render("  loading…")}
	}
	env := append([]string(nil), ins.Config.Env...)
	sort.Strings(env)
	kw := 0
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		kw = max(kw, len(k))
	}
	kw = min(kw, iw/2)
	hidden := 0
	var body []string
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		val := sText.Render(v)
		switch {
		case m.revealEnv || v == "":
		case isSecret(k):
			val = fg(cYellow).Render("••••••••")
			hidden++
		case urlCreds.MatchString(v): // passwords inside connection strings
			parts := urlCreds.FindStringSubmatchIndex(v)
			val = sText.Render(v[:parts[4]]) + fg(cYellow).Render("••••") + sText.Render(v[parts[5]:])
			hidden++
		}
		body = append(body, " "+fg(cCyan).Render(fmt.Sprintf("%-*s", kw, ansi.Truncate(k, kw, "…")))+sSubtle.Render(" = ")+val)
	}
	head := " " + sBold.Render(fmt.Sprintf("%d variables", len(env)))
	if hidden > 0 {
		head += sMuted.Render(fmt.Sprintf("  ·  %d secrets masked — press ", hidden)) + sKey.Render("v") + sMuted.Render(" to reveal")
	} else if m.revealEnv {
		head += fg(cYellow).Render("  ·  secrets visible — press v to mask")
	}
	return append([]string{head, ""}, m.scroll(body, m.dScroll, iw, ih-2, 2)...)
}

// ---------------------------------------------------------------- config tab

func (m Model) viewConfig(c *docker.Container, iw, ih int) []string {
	ins := m.insp[c.ID]
	if ins == nil {
		return []string{"", sMuted.Render("  loading…")}
	}
	var l []string
	sec := func(t string) {
		if len(l) > 0 {
			l = append(l, "")
		}
		l = append(l, " "+fg(cAccent2).Bold(true).Render(t)+" "+sSubtle.Render(strings.Repeat("─", max(0, iw-len(t)-3))))
	}
	kv := func(k, v string) {
		if v == "" {
			v = sSubtle.Render("—")
		} else {
			v = sText.Render(v)
		}
		l = append(l, "   "+sMuted.Render(fmt.Sprintf("%-14s", k))+v)
	}
	ts := func(s string) string {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil || t.Year() < 2000 {
			return ""
		}
		return t.Local().Format("2006-01-02 15:04:05") + "  (" + ago(t) + " ago)"
	}

	sec("Overview")
	kv("image", ins.Config.Image)
	kv("command", strings.Join(append(append([]string{}, ins.Config.Entrypoint...), ins.Config.Cmd...), " "))
	kv("workdir", ins.Config.WorkingDir)
	kv("user", ins.Config.User)
	kv("hostname", ins.Config.Hostname)
	kv("created", ts(ins.Created))
	kv("started", ts(ins.State.StartedAt))
	if !ins.State.Running {
		kv("finished", ts(ins.State.FinishedAt))
		kv("exit code", strconv.Itoa(ins.State.ExitCode))
	}
	rp := ins.HostConfig.RestartPolicy.Name
	if ins.HostConfig.RestartPolicy.MaximumRetryCount > 0 {
		rp += fmt.Sprintf(" (max %d)", ins.HostConfig.RestartPolicy.MaximumRetryCount)
	}
	kv("restart", rp)
	kv("restarts", strconv.Itoa(ins.RestartCount))
	if ins.State.OOMKilled {
		l = append(l, "   "+fg(cRed).Bold(true).Render("▲ last exit was an out-of-memory kill"))
	}
	lim := "unlimited"
	if ins.HostConfig.Memory > 0 {
		lim = HumanBytes(float64(ins.HostConfig.Memory))
	}
	cpus := "unlimited"
	if ins.HostConfig.NanoCpus > 0 {
		cpus = fmt.Sprintf("%.2f cpus", float64(ins.HostConfig.NanoCpus)/1e9)
	}
	kv("memory limit", lim)
	kv("cpu limit", cpus)

	if h := ins.State.Health; h != nil {
		sec("Health")
		col := stateColor[h.Status]
		l = append(l, "   "+sMuted.Render(fmt.Sprintf("%-14s", "status"))+fg(col).Bold(true).Render(h.Status)+sMuted.Render(fmt.Sprintf("   failing streak %d", h.FailingStreak)))
		if hc := ins.Config.Healthcheck; hc != nil {
			kv("test", strings.Join(hc.Test, " "))
			kv("interval", time.Duration(hc.Interval).String()+"  timeout "+time.Duration(hc.Timeout).String()+fmt.Sprintf("  retries %d", hc.Retries))
		}
		for i := len(h.Log) - 1; i >= 0; i-- {
			e := h.Log[i]
			mark := fg(cGreen).Render("✓")
			if e.ExitCode != 0 {
				mark = fg(cRed).Render("✗")
			}
			t, _ := time.Parse(time.RFC3339Nano, e.Start)
			out := lastLine(ansi.Strip(e.Output))
			l = append(l, "   "+mark+" "+sMuted.Render(t.Local().Format("15:04:05"))+"  "+sText.Render(ansi.Truncate(out, max(10, iw-18), "…")))
		}
	}

	if len(c.Ports) > 0 {
		sec("Ports")
		seen := map[string]bool{}
		for _, p := range c.Ports {
			s := fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
			if p.PublicPort > 0 {
				ip := p.IP
				if ip == "" || ip == "0.0.0.0" || ip == "::" {
					ip = "*"
				}
				s = fmt.Sprintf("%s:%d → %d/%s", ip, p.PublicPort, p.PrivatePort, p.Type)
			}
			if !seen[s] {
				seen[s] = true
				l = append(l, "   "+sText.Render(s))
			}
		}
	}
	if len(ins.Mounts) > 0 {
		sec("Mounts")
		for _, mt := range ins.Mounts {
			src := mt.Source
			if mt.Type == "volume" {
				src = mt.Name
			}
			mode := "rw"
			if !mt.RW {
				mode = "ro"
			}
			l = append(l, "   "+fg(cTeal).Render(fmt.Sprintf("%-7s", mt.Type))+sText.Render(ansi.Truncate(src, max(10, iw/2-8), "…"))+sMuted.Render(" → ")+sText.Render(mt.Destination)+sMuted.Render(" "+mode))
		}
	}
	if len(ins.NetworkSettings.Networks) > 0 {
		sec("Networks")
		names := make([]string, 0)
		for n := range ins.NetworkSettings.Networks {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			nw := ins.NetworkSettings.Networks[n]
			l = append(l, "   "+fg(cCyan).Render(fmt.Sprintf("%-20s", n))+sText.Render(nw.IPAddress)+sMuted.Render("  gw "+nw.Gateway))
		}
	}
	if len(ins.Config.Labels) > 0 {
		sec("Labels")
		keys := make([]string, 0, len(ins.Config.Labels))
		for k := range ins.Config.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			l = append(l, "   "+sMuted.Render(k)+sSubtle.Render(" = ")+sText.Render(ins.Config.Labels[k]))
		}
	}
	return m.scroll(l, m.dScroll, iw, ih, 0)
}

// ---------------------------------------------------------------- top tab

func (m Model) viewTop(c *docker.Container, iw, ih int) []string {
	if c.State != "running" {
		return []string{"", sMuted.Render("  container is not running")}
	}
	t := m.top
	if t == nil || m.topID != c.ID {
		return []string{"", sMuted.Render("  loading processes…")}
	}
	cols := len(t.Titles)
	widths := make([]int, cols)
	for i, h := range t.Titles {
		widths[i] = len(h)
	}
	for _, p := range t.Processes {
		for i := 0; i < cols-1 && i < len(p); i++ {
			widths[i] = max(widths[i], len(p[i]))
		}
	}
	// sort by %CPU desc when present
	cpuCol := -1
	for i, h := range t.Titles {
		if h == "%CPU" {
			cpuCol = i
		}
	}
	procs := append([][]string(nil), t.Processes...)
	if cpuCol >= 0 {
		sort.SliceStable(procs, func(i, j int) bool {
			a, _ := strconv.ParseFloat(at(procs[i], cpuCol), 64)
			b, _ := strconv.ParseFloat(at(procs[j], cpuCol), 64)
			return a > b
		})
	}
	render := func(cells []string, head bool) string {
		var b strings.Builder
		b.WriteString(" ")
		for i := 0; i < cols; i++ {
			v := at(cells, i)
			if i < cols-1 {
				v = fmt.Sprintf("%*s", widths[i], v)
				if i == cols-2 || t.Titles[i] == "USER" {
					v = fmt.Sprintf("%-*s", widths[i], at(cells, i))
				}
			}
			switch {
			case head:
				b.WriteString(sMuted.Bold(true).Render(v))
			case t.Titles[i] == "%CPU" || t.Titles[i] == "%MEM":
				f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
				b.WriteString(heatText(v, f/100))
			case i == cols-1:
				b.WriteString(sText.Render(v))
			default:
				b.WriteString(sMuted.Render(v))
			}
			b.WriteString("  ")
		}
		return b.String()
	}
	lines := []string{" " + sBold.Render(fmt.Sprintf("%d processes", len(procs))) + sMuted.Render("  ·  sorted by cpu  ·  refreshes every 2s"), "", render(t.Titles, true)}
	var body []string
	for _, p := range procs {
		body = append(body, render(p, false))
	}
	return append(lines, m.scroll(body, m.dScroll, iw, ih-3, 3)...)
}

// lastLine returns the last non-empty line (healthcheck output often starts
// with progress noise such as curl's transfer table).
func lastLine(s string) string {
	ls := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	for i := len(ls) - 1; i >= 0; i-- {
		if t := strings.Join(strings.Fields(ls[i]), " "); t != "" {
			return t
		}
	}
	return "(no output)"
}

var urlCreds = regexp.MustCompile(`(://[^:/@\s]+:)([^@\s]+)(@)`)

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}
