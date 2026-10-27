package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var alertIcon = map[string]struct {
	glyph string
	col   lipgloss.Color
}{
	"crash":     {"✗", cRed},
	"oom":       {"▲", cRed},
	"unhealthy": {"●", cYellow},
}

func (m *Model) openAlerts() {
	m.alertsOpen = true
	m.alertCur = 0
}

// alertAt returns the i-th alert, newest first.
func (m Model) alertAt(i int) alert { return m.alerts[len(m.alerts)-1-i] }

func (m Model) alertWindow() (vis, start int) {
	vis = clamp(m.h-12, 4, 16)
	if m.alertCur >= vis {
		start = m.alertCur - vis + 1
	}
	return
}

func (m *Model) alertKey(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc", "q", "!":
		m.alertsOpen = false
	case "up", "k":
		m.alertCur = max(0, m.alertCur-1)
	case "down", "j":
		m.alertCur = min(len(m.alerts)-1, m.alertCur+1)
	case "c":
		m.alerts = nil
		m.alertCur = 0
	case "enter":
		if len(m.alerts) > 0 {
			m.jumpToAlert(m.alertAt(m.alertCur))
		}
	}
	return nil
}

func (m *Model) alertMouse(msg tea.MouseMsg) tea.Cmd {
	x0, y0, w, h := m.boxOrigin(m.viewAlerts())
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.alertCur = max(0, m.alertCur-1)
		return nil
	case tea.MouseButtonWheelDown:
		m.alertCur = min(len(m.alerts)-1, m.alertCur+1)
		return nil
	}
	inside := msg.X >= x0 && msg.X < x0+w && msg.Y >= y0 && msg.Y < y0+h
	vis, start := m.alertWindow()
	idx := -1
	if j := msg.Y - (y0 + 4); inside && j >= 0 && j < vis && start+j < len(m.alerts) { // border, pad, header, rule
		idx = start + j
	}
	switch {
	case msg.Action == tea.MouseActionMotion:
		if idx >= 0 {
			m.alertCur = idx
		}
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		if !inside {
			m.alertsOpen = false
		} else if idx >= 0 {
			m.jumpToAlert(m.alertAt(idx))
		}
	}
	return nil
}

// jumpToAlert selects the container and opens its logs scrolled to the
// moment the alert fired.
func (m *Model) jumpToAlert(a alert) {
	m.alertsOpen = false
	if m.containerByID(a.id) == nil {
		m.flash(a.name+" no longer exists", true)
		return
	}
	m.filter.SetValue("")
	if m.hideStopped {
		m.hideStopped = false
	}
	m.setView(VContainers)
	m.selID = a.id
	m.rebuildRows()
	m.setTab(TLogs)
	m.focus = 1
	m.logs.jumpTo = a.at
	if m.logs.id == a.id {
		m.logs.applyJump()
	}
}

func (m Model) viewAlerts() string {
	w := clamp(m.w*62/100, 64, 104)
	iw := w - 4
	n := len(m.alerts)
	head := sBold.Render(fmt.Sprintf("%d alert%s this session", n, plural(n))) + sMuted.Render("  ·  newest first")
	if r := m.recentAlerts(); r > 0 {
		head += sMuted.Render("  ·  ") + fg(cRed).Render(fmt.Sprintf("%d in the last 15 min", r))
	}
	lines := []string{head, sSubtle.Render(strings.Repeat("─", iw))}
	vis, start := m.alertWindow()
	for i := start; i < n && i < start+vis; i++ {
		a := m.alertAt(i)
		ic := alertIcon[a.kind]
		left := fg(ic.col).Bold(true).Render(ic.glyph) + " " + sMuted.Render(a.at.Format("15:04:05")) + "  " +
			sBold.Render(fmt.Sprintf("%-24s", ansi.Truncate(a.name, 24, "…"))) + " " + sText.Render(a.text)
		right := sMuted.Render(ago(a.at) + " ago")
		if i == m.alertCur {
			lines = append(lines, withBg(fg(cAccent).Render("▌")+spread(left, right+" "+sKey.Render("↵ logs"), iw-1), cSelBg, iw))
		} else {
			lines = append(lines, " "+spread(left, right+"       ", iw-1))
		}
	}
	if n == 0 {
		lines = append(lines, fg(cGreen).Render("✓ no alerts — all calm"))
	}
	lines = append(lines, "",
		sKey.Render("↵/click")+sMuted.Render(" jump to logs at that moment   ")+sKey.Render("c")+sMuted.Render(" clear   ")+sKey.Render("esc")+sMuted.Render(" close"))
	return modal("Alerts", lines, w, cRed)
}

// alertBadgeHit reports whether a header click at (x,y) landed on the badge.
func (m Model) alertBadgeHit(x, y int) bool {
	n := m.recentAlerts()
	if y != 1 || n == 0 {
		return false
	}
	badge := fmt.Sprintf("▲ %d alert%s", n, plural(n))
	right := lipgloss.Width(badge) + 5 + len("15:04:05") + 1 // badge + "  │  " + clock + " "
	start := m.w - 1 - right
	return x >= start && x < start+lipgloss.Width(badge)
}

var _ = time.Second
