package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	colorful "github.com/lucasb-eyer/go-colorful"

	"github.com/krish-vd/berth/internal/metrics"
)

const headerHeight = 4

func (m Model) bodyHeight() int { return m.h - headerHeight - 1 }

func (m Model) listWidth() int {
	switch {
	case m.view == VEvents || m.zoom == 2:
		return 0
	case m.zoom == 1:
		return clamp(m.w*28/100, 30, 40)
	}
	return clamp(m.w*40/100, 44, 66)
}

func (m Model) View() string {
	StatViews.Add(1)
	if m.w < 70 || m.h < 18 {
		return sMuted.Render("berth needs at least 70×18 — enlarge the window")
	}
	if !m.gotSnap {
		return m.splash()
	}
	bh := m.bodyHeight()
	lw := m.listWidth()
	var body string
	switch m.view {
	case VHosts:
		if lw > 0 {
			body = lipgloss.JoinHorizontal(lipgloss.Top, m.viewHostList(lw, bh), m.viewHostDetail(m.w-lw, bh))
		} else {
			body = m.viewHostDetail(m.w, bh)
		}
	case VDoctor:
		if lw > 0 {
			body = lipgloss.JoinHorizontal(lipgloss.Top, m.viewDoctorList(lw, bh), m.viewDoctorDetail(m.w-lw, bh))
		} else {
			body = m.viewDoctorDetail(m.w, bh)
		}
	case VEvents:
		body = m.viewEvents(m.w, bh)
	default:
		var left, right string
		switch m.view {
		case VContainers:
			if lw > 0 {
				left = m.viewList(lw, bh)
			}
			right = m.viewDetail(m.w-lw, bh)
		default:
			if lw > 0 {
				left = m.viewResList(lw, bh)
			}
			right = m.viewResDetail(m.w-lw, bh)
		}
		if left == "" {
			body = right
		} else {
			body = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
		}
	}
	out := lipgloss.JoinVertical(lipgloss.Left, m.viewHeader(), body, m.viewFooter())
	switch {
	case m.confirm != nil:
		out = overlay(out, m.viewConfirm(), m.w, m.h)
	case m.hosts.form != nil:
		out = overlay(out, m.viewHostForm(), m.w, m.h)
	case m.palette != nil:
		out = overlay(out, m.viewPalette(), m.w, m.h)
	case m.alertsOpen:
		out = overlay(out, m.viewAlerts(), m.w, m.h)
	case m.help:
		out = overlay(out, m.viewHelp(), m.w, m.h)
	}
	return out
}

func (m Model) splash() string {
	sp := string(spinner[m.frame%len(spinner)])
	msg := sAccent.Render(sp) + "  " + sText.Render("connecting to ") + sBold.Render(m.cli.Host)
	switch err := m.snap.Err; {
	case err == nil:
	case strings.Contains(err.Error(), "deadline exceeded") || strings.Contains(err.Error(), "Timeout"):
		// Docker answered the connection but is slow — usually a busy host
		// (e.g. a stack that is still starting). Not an error worth a red ✗.
		msg = fg(cYellow).Render(sp) + "  " + sText.Render("Docker at ") + sBold.Render(m.cli.Host) + sText.Render(" is busy — still connecting…") +
			"\n\n" + sMuted.Render("the host is under heavy load (containers starting?) · berth keeps trying · q to quit")
	default:
		msg = fg(cRed).Render("✗ ") + sText.Render("cannot reach Docker at ") + sBold.Render(m.cli.Host) +
			"\n\n" + sMuted.Render(err.Error()) + "\n\n" + sMuted.Render("retrying every second · q to quit")
	}
	logo := fg(cAccent).Bold(true).Render("◆ berth")
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, logo+"\n\n"+msg)
}

// ---------------------------------------------------------------- primitives

// panel draws a rounded box with an inline title and optional right label.
func panel(title, right string, lines []string, w, h int, focused bool) string {
	bc := cBorder
	if focused {
		bc = cBorderHi
	}
	bs := fg(bc)
	iw := w - 2
	t := " " + title + " "
	r := ""
	if right != "" {
		r = " " + right + " "
	}
	fill := iw - 1 - lipgloss.Width(t) - lipgloss.Width(r) - 1
	if fill < 0 {
		r = ""
		fill = iw - 1 - lipgloss.Width(t) - 1
		if fill < 0 {
			t = ansi.Truncate(t, max(0, iw-2), "…")
			fill = max(0, iw-1-lipgloss.Width(t)-1)
		}
	}
	var b strings.Builder
	b.WriteString(bs.Render("╭─") + t + bs.Render(strings.Repeat("─", fill)) + r + bs.Render("─╮"))
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		b.WriteString("\n" + bs.Render("│") + fit(l, iw) + bs.Render("│"))
	}
	b.WriteString("\n" + bs.Render("╰"+strings.Repeat("─", iw)+"╯"))
	return b.String()
}

// fit pads or truncates an ANSI string to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := lipgloss.Width(s)
	if sw > w {
		return ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-sw)
}

func spread(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

func bgSeq(c lipgloss.Color) string {
	col, err := colorful.Hex(string(c))
	if err != nil {
		return ""
	}
	r, g, b := col.RGB255()
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
}

// withBg paints a full-width background behind an already-styled line,
// re-applying it after every inner reset so it never "breaks".
func withBg(s string, bg lipgloss.Color, w int) string {
	seq := bgSeq(bg)
	s = fit(s, w)
	return seq + strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+seq) + "\x1b[0m"
}

// selRow renders a selected list row: glowing edge bar + tinted band.
func (m Model) selRow(content string, w int, focused bool) string {
	bar, bg := cAccent, cSelBg
	if !focused {
		bar, bg = cMuted, lipgloss.Color("#1f2335")
	}
	edge := fg(bar).Render("▌")
	return withBg(edge+content, bg, w)
}

func heatText(s string, t float64) string {
	return fg(Heat(t)).Bold(true).Render(s)
}

var styleCache sync.Map // lipgloss.Color → lipgloss.Style

// fg returns a cached foreground style; building styles per cell per frame
// was the single biggest CPU cost.
func fg(c lipgloss.Color) lipgloss.Style {
	if s, ok := styleCache.Load(c); ok {
		return s.(lipgloss.Style)
	}
	s := lipgloss.NewStyle().Foreground(c)
	styleCache.Store(c, s)
	return s
}

// overlay centres box over base.
func overlay(base, box string, w, h int) string {
	bl := strings.Split(base, "\n")
	ol := strings.Split(box, "\n")
	bw := 0
	for _, l := range ol {
		bw = max(bw, lipgloss.Width(l))
	}
	x := max(0, (w-bw)/2)
	y := max(0, (h-len(ol))/2)
	for i, l := range ol {
		if y+i >= len(bl) {
			break
		}
		line := bl[y+i]
		left := ansi.Truncate(line, x, "")
		left += strings.Repeat(" ", max(0, x-lipgloss.Width(left)))
		right := ansi.TruncateLeft(line, x+bw, "")
		bl[y+i] = left + "\x1b[0m" + fit(l, bw) + "\x1b[0m" + right
	}
	return strings.Join(bl, "\n")
}

// ---------------------------------------------------------------- header

func (m Model) viewHeader() string {
	info := m.snap.Info
	var run, unh, stopped int
	for _, c := range m.snap.Containers {
		switch {
		case c.Health() == "unhealthy":
			unh++
		case c.State == "running":
			run++
		default:
			stopped++
		}
	}
	iw := m.w - 2
	sep := sSubtle.Render("  │  ")
	counts := fg(cGreen).Render("●") + " " + sBold.Render(fmt.Sprint(run)) + " " + sMuted.Render("running")
	if unh > 0 {
		counts += "   " + fg(Pulse(cRed, m.frame)).Render("●") + " " + sBold.Render(fmt.Sprint(unh)) + " " + sMuted.Render("unhealthy")
	}
	counts += "   " + fg(cMuted).Render("○") + " " + sBold.Render(fmt.Sprint(stopped)) + " " + sMuted.Render("stopped")
	where := sText.Render(m.cli.Host)
	if HostLabel != "" {
		where = fg(cAccent2).Bold(true).Render(HostLabel) + " " + sMuted.Render(m.cli.Host)
	}
	l1 := " " + where + sep + sMuted.Render("engine ") + sText.Render(info.ServerVersion) +
		sep + sText.Render(fmt.Sprint(info.NCPU)) + sMuted.Render(" cpu ") + sText.Render(HumanBytes(float64(info.MemTotal))) +
		sep + counts
	right := sMuted.Render(time.Now().Format("15:04:05")) + " "
	if n := m.recentAlerts(); n > 0 {
		right = fg(Pulse(cRed, m.frame)).Bold(true).Render(fmt.Sprintf("▲ %d alert%s", n, plural(n))) + sSubtle.Render("  │  ") + right
	}
	l1 = spread(l1, right, iw)

	memT := 0.0
	if info.MemTotal > 0 {
		memT = m.hostMem.Cur / float64(info.MemTotal)
	}
	rx := last(m.smp.HostHistory(metrics.NetRx, 1))
	tx := last(m.smp.HostHistory(metrics.NetTx, 1))
	br := last(m.smp.HostHistory(metrics.BlkRead, 1))
	bw := last(m.smp.HostHistory(metrics.BlkWrite, 1))
	l2 := " " + sMuted.Render("CPU ") + Sparkline(m.smp.HostHistory(metrics.CPU, 24), 24, 100) + " " + heatText(fmt.Sprintf("%5.1f%%", m.hostCPU.Cur), m.hostCPU.Cur/100) +
		sep + sMuted.Render("MEM ") + Bar(memT, 14) + " " + sText.Render(HumanBytes(m.hostMem.Cur)) + sMuted.Render(" / "+HumanBytes(float64(info.MemTotal))) +
		sep + sMuted.Render("NET ") + fg(cCyan).Render("↓") + sText.Render(HumanRate(rx)) + " " + fg(cAccent2).Render("↑") + sText.Render(HumanRate(tx)) +
		sep + sMuted.Render("DISK ") + sText.Render("r "+HumanRate(br)+"  w "+HumanRate(bw))

	logo := fg(cAccent).Bold(true).Render("◆ berth")
	tabs := ""
	for i, n := range viewNames {
		k := viewKey(View(i))
		if View(i) == m.view {
			tabs += " " + withBg(fg(cAccent2).Bold(true).Render(k)+" "+sBold.Render(n), cSelBg, len(n)+2) + " "
		} else {
			tabs += " " + sSubtle.Render(k) + " " + sMuted.Render(n) + " "
		}
	}
	return panel(logo+"  "+tabs, sMuted.Render("container observatory"), []string{l1, l2}, m.w, headerHeight, false)
}

// ---------------------------------------------------------------- footer

func (m Model) viewFooter() string {
	k := func(key, label string) string { return sKey.Render(key) + " " + sMuted.Render(label) }
	var hints []string
	switch {
	case m.filtering:
		hints = []string{m.filter.View(), k("enter", "keep"), k("esc", "clear")}
	case m.logs.searching:
		hints = []string{m.logs.input.View(), k("enter", "apply"), k("esc", "cancel")}
	case m.view == VContainers && m.focus == 1 && m.tab == TLogs:
		hints = []string{k("↑↓", "scroll"), k("/", "search"), k("f", "follow"), k("z", "wrap"), k("i", "timestamps"), k("m", "raw logs"), k("[ ]", "tabs"), k("esc", "back")}
	case m.view == VContainers && m.focus == 1:
		hints = []string{k("↑↓", "scroll"), k("[ ]", "tabs"), k("t", "window"), k("v", "reveal env"), k("esc", "back"), k("x", "menu")}
	case m.view == VContainers:
		hints = []string{k("x", "menu"), k("enter", "focus"), k("[ ]", "tabs"), k("s", "stop/start"), k("r", "restart"), k("E", "shell"), k("m", "logs"), k("d", "remove"), k("/", "filter"), k("C", "compose"), k("c", "custom"), k("b", "bulk"), k("?", "help")}
	case m.view == VHosts:
		hints = []string{k("↑↓", "select"), k("enter", "switch to it"), k("a", "add"), k("e", "edit"), k("d", "remove"), k("r", "probe again"), k("1-6", "views"), k("?", "help")}
	case m.view == VDoctor:
		hints = []string{k("↑↓", "select"), k("r", "re-run checks"), k("f", "apply fix"), k("enter", "details"), k("1-6", "views"), k("?", "help")}
	case m.view == VEvents:
		hints = []string{k("↑↓", "scroll"), k("c", "clear"), k("1-6", "views"), k("?", "help")}
	default:
		hints = []string{k("↑↓", "select"), k("d", "remove"), k("P", "prune"), k("J", "json"), k("x", "menu"), k("1-6", "views"), k("?", "help")}
	}
	left := " " + strings.Join(hints, sSubtle.Render("  "))
	right := ""
	if m.toast != "" && time.Now().Before(m.toastUntil) {
		col := cGreen
		if m.toastErr {
			col = cRed
		}
		right = fg(col).Bold(true).Render(m.toast) + " "
	} else if m.snap.Err != nil {
		right = fg(cRed).Render("▲ "+ansi.Truncate(m.snap.Err.Error(), 50, "…")) + " "
	} else if n := len(m.pending); n > 0 {
		right = sAccent.Render(string(spinner[m.frame%len(spinner)])) + sMuted.Render(fmt.Sprintf(" %d running", n)) + " "
	}
	if lipgloss.Width(left)+lipgloss.Width(right) > m.w {
		left = ansi.Truncate(left, max(0, m.w-lipgloss.Width(right)-1), "…")
	}
	return spread(left, right, m.w)
}

// ---------------------------------------------------------------- helpers

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func last(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return v[len(v)-1]
}

func dedupe(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 730*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
	return fmt.Sprintf("%dy", int(d.Hours()/24/365))
}

// window renders a scrolled slice of lines, clamping the offset.
func window(lines []string, off *int, h int) []string {
	maxOff := max(0, len(lines)-h)
	if *off > maxOff {
		*off = maxOff
	}
	end := min(len(lines), *off+h)
	return lines[*off:end]
}
