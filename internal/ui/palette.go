package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/krish-vd/berth/internal/docker"
)

type item struct {
	key, label, hint string
	danger           bool
	run              func(m *Model) tea.Cmd
}

type palette struct {
	title    string
	items    []item
	input    textinput.Model
	cur      int
	filtered []int
}

type confirm struct {
	title  string
	body   []string
	danger bool
	yes    func(m *Model) tea.Cmd
}

func (m *Model) openPalette(title string, items []item) {
	in := textinput.New()
	in.Prompt = "❯ "
	in.Placeholder = "type to search actions…"
	in.Focus()
	p := &palette{title: title, items: items, input: in}
	p.refilter()
	m.palette = p
}

// fuzzy scores a subsequence match; higher is better, -1 means no match.
func fuzzy(q, s string) int {
	if q == "" {
		return 0
	}
	q, s = strings.ToLower(q), strings.ToLower(s)
	if i := strings.Index(s, q); i >= 0 {
		return 1000 - i
	}
	score, j, streak := 0, 0, 0
	for i := 0; i < len(s) && j < len(q); i++ {
		if s[i] == q[j] {
			j++
			streak++
			score += 10 * streak
			if i == 0 || s[i-1] == ' ' {
				score += 20
			}
		} else {
			streak = 0
		}
	}
	if j < len(q) {
		return -1
	}
	return score
}

func (p *palette) refilter() {
	q := strings.TrimSpace(p.input.Value())
	type sc struct{ i, s int }
	var res []sc
	for i, it := range p.items {
		s := fuzzy(q, it.label+" "+it.hint)
		if k := strings.ToLower(q); k != "" && strings.ToLower(it.key) == k {
			s += 2000
		}
		if s >= 0 {
			res = append(res, sc{i, s})
		}
	}
	if q != "" {
		sort.SliceStable(res, func(a, b int) bool { return res[a].s > res[b].s })
	}
	p.filtered = p.filtered[:0]
	for _, r := range res {
		p.filtered = append(p.filtered, r.i)
	}
	p.cur = clamp(p.cur, 0, max(0, len(p.filtered)-1))
}

func (m Model) paletteKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.palette
	switch k.String() {
	case "esc", "ctrl+c":
		m.palette = nil
		return m, nil
	case "up", "ctrl+k", "shift+tab":
		p.cur = max(0, p.cur-1)
		return m, nil
	case "down", "ctrl+j", "tab":
		p.cur = min(len(p.filtered)-1, p.cur+1)
		return m, nil
	case "enter":
		if len(p.filtered) == 0 {
			return m, nil
		}
		it := p.items[p.filtered[p.cur]]
		m.palette = nil
		return m, it.run(&m)
	}
	// when the box is empty, a bare key runs its action directly (lazydocker muscle memory)
	if p.input.Value() == "" && len(k.Runes) == 1 {
		for _, it := range p.items {
			if it.key == string(k.Runes) {
				m.palette = nil
				return m, it.run(&m)
			}
		}
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(k)
	p.refilter()
	return m, cmd
}

// ---------------------------------------------------------------- item sets

func (m Model) contextItems() []item {
	switch m.view {
	case VContainers:
		c := m.selected()
		if c == nil {
			return nil
		}
		running := c.State == "running"
		startStop := item{key: "s", label: "Stop", hint: c.Name(), run: func(m *Model) tea.Cmd { return m.act(c, "stop") }}
		if !running && c.State != "paused" {
			startStop = item{key: "s", label: "Start", hint: c.Name(), run: func(m *Model) tea.Cmd { return m.act(c, "start") }}
		}
		pause := item{key: "p", label: "Pause", hint: "freeze all processes", run: func(m *Model) tea.Cmd { return m.act(c, "pause") }}
		if c.State == "paused" {
			pause = item{key: "p", label: "Unpause", hint: "resume processes", run: func(m *Model) tea.Cmd { return m.act(c, "unpause") }}
		}
		its := []item{
			startStop,
			{key: "r", label: "Restart", hint: c.Name(), run: func(m *Model) tea.Cmd { return m.act(c, "restart") }},
			pause,
			{key: "E", label: "Exec shell", hint: "bash, falling back to sh", run: func(m *Model) tea.Cmd { return m.shell(c) }},
			{key: "a", label: "Attach", hint: "attach to main process (ctrl+c detaches)", run: func(m *Model) tea.Cmd { return m.attach(c) }},
			{key: "m", label: "View logs", hint: "full terminal · ctrl+c returns", run: func(m *Model) tea.Cmd { return m.rawLogs(c) }},
			{label: "View logs in berth", hint: "searchable logs tab", run: func(m *Model) tea.Cmd { m.setTab(TLogs); m.focus = 1; return nil }},
			{key: "w", label: "Open in browser", hint: "first published port", run: func(m *Model) tea.Cmd { m.openBrowser(c); return nil }},
			{key: "d", label: "Remove", hint: "delete container", danger: true, run: func(m *Model) tea.Cmd { m.askRemove(c); return nil }},
			{key: "K", label: "Kill", hint: "SIGKILL", danger: true, run: func(m *Model) tea.Cmd {
				m.ask("Kill "+c.Name()+"?", []string{"Sends SIGKILL immediately."}, true, func(m *Model) tea.Cmd { return m.act(c, "kill") })
				return nil
			}},
			{key: "y", label: "Copy container ID", hint: c.ID[:12], run: func(m *Model) tea.Cmd { return copyText(m, c.ID) }},
		}
		if _, ok := composeOf(c); ok {
			its = append(its, item{key: "C", label: "Compose…", hint: "up · down · pull · build · logs", run: func(m *Model) tea.Cmd {
				m.openPalette("Compose", m.composeItems())
				return nil
			}})
		}
		for i := range tabNames {
			t := Tab(i)
			its = append(its, item{label: "Show " + tabNames[i], hint: "detail tab", run: func(m *Model) tea.Cmd { m.setTab(t); return nil }})
		}
		return its
	case VImages:
		return []item{
			{key: "d", label: "Remove image", danger: true, run: func(m *Model) tea.Cmd { m.askRemoveResource(); return nil }},
			{key: "P", label: "Prune dangling images", danger: true, run: func(m *Model) tea.Cmd { m.askPrune("images"); return nil }},
		}
	case VVolumes:
		return []item{
			{key: "d", label: "Remove volume", danger: true, run: func(m *Model) tea.Cmd { m.askRemoveResource(); return nil }},
			{key: "P", label: "Prune anonymous volumes", danger: true, run: func(m *Model) tea.Cmd { m.askPrune("volumes"); return nil }},
		}
	case VNetworks:
		return []item{
			{key: "d", label: "Remove network", danger: true, run: func(m *Model) tea.Cmd { m.askRemoveResource(); return nil }},
			{key: "P", label: "Prune unused networks", danger: true, run: func(m *Model) tea.Cmd { m.askPrune("networks"); return nil }},
		}
	}
	return nil
}

func (m Model) globalItems() []item {
	var its []item
	for i, n := range viewNames {
		v := View(i)
		its = append(its, item{key: fmt.Sprint(i + 1), label: "Go to " + n, hint: "view", run: func(m *Model) tea.Cmd { m.setView(v); return nil }})
	}
	for i, w := range windows {
		i := i
		its = append(its, item{label: "Time window " + w.label, hint: "charts", run: func(m *Model) tea.Cmd { m.winIdx = i; return nil }})
	}
	its = append(its,
		item{key: "e", label: "Toggle stopped containers", hint: "hide/show", run: func(m *Model) tea.Cmd { m.hideStopped = !m.hideStopped; m.rebuildRows(); return nil }},
		item{key: "o", label: "Cycle sort", hint: "name → cpu → memory", run: func(m *Model) tea.Cmd { m.sortMode = (m.sortMode + 1) % len(sortNames); m.rebuildRows(); return nil }},
		item{key: "+", label: "Next screen mode", hint: "normal / half / full", run: func(m *Model) tea.Cmd { m.zoom = (m.zoom + 1) % 3; return nil }},
		item{key: "b", label: "Bulk commands", hint: "act on many containers", run: func(m *Model) tea.Cmd { m.openPalette("Bulk commands", m.bulkItems()); return nil }},
		item{key: "!", label: "Show alerts", hint: "crashes, OOM kills, unhealthy", run: func(m *Model) tea.Cmd { m.openAlerts(); return nil }},
		item{key: "?", label: "Keyboard shortcuts", hint: "help", run: func(m *Model) tea.Cmd { m.help = true; return nil }},
		item{key: "q", label: "Quit", run: func(m *Model) tea.Cmd { m.logs.stop(); return tea.Quit }},
	)
	return its
}

func (m Model) bulkItems() []item {
	cli := m.cli
	each := func(label string, pick func(docker.Container) bool, verb string) func(m *Model) tea.Cmd {
		return func(m *Model) tea.Cmd {
			var ids []string
			for _, c := range m.snap.Containers {
				if pick(c) {
					ids = append(ids, c.ID)
				}
			}
			if len(ids) == 0 {
				m.flash("nothing to "+verb, false)
				return nil
			}
			m.ask(fmt.Sprintf("%s (%d container%s)?", label, len(ids), plural(len(ids))), nil, verb == "stop" || verb == "kill", func(m *Model) tea.Cmd {
				return m.run("bulk:"+verb, fmt.Sprintf("%s %d containers", verb, len(ids)), func(ctx context.Context) error {
					var first error
					for _, id := range ids {
						if err := cli.Action(ctx, id, verb); err != nil && first == nil {
							first = err
						}
					}
					return first
				})
			})
			return nil
		}
	}
	running := func(c docker.Container) bool { return c.State == "running" }
	stopped := func(c docker.Container) bool { return c.State == "exited" || c.State == "created" }
	its := []item{
		{label: "Stop all running containers", danger: true, run: each("Stop all running", running, "stop")},
		{label: "Restart all running containers", run: each("Restart all running", running, "restart")},
		{label: "Start all stopped containers", run: each("Start all stopped", stopped, "start")},
	}
	if c := m.selected(); c != nil && c.Project() != "" {
		p := c.Project()
		inP := func(c docker.Container) bool { return c.Project() == p }
		its = append(its,
			item{label: "Restart project " + p, hint: "compose", run: each("Restart project "+p, func(c docker.Container) bool { return inP(c) && c.State == "running" }, "restart")},
			item{label: "Stop project " + p, hint: "compose", danger: true, run: each("Stop project "+p, func(c docker.Container) bool { return inP(c) && c.State == "running" }, "stop")},
			item{label: "Start project " + p, hint: "compose", run: each("Start project "+p, func(c docker.Container) bool { return inP(c) && c.State != "running" }, "start")},
		)
	}
	its = append(its,
		item{label: "Remove stopped containers", hint: "prune", danger: true, run: func(m *Model) tea.Cmd { m.askPrune("containers"); return nil }},
		item{label: "Prune dangling images", hint: "prune", danger: true, run: func(m *Model) tea.Cmd { m.askPrune("images"); return nil }},
		item{label: "Prune anonymous volumes", hint: "prune", danger: true, run: func(m *Model) tea.Cmd { m.askPrune("volumes"); return nil }},
		item{label: "Prune unused networks", hint: "prune", danger: true, run: func(m *Model) tea.Cmd { m.askPrune("networks"); return nil }},
	)
	return its
}

// ---------------------------------------------------------------- rendering

func modal(title string, lines []string, w int, accent lipgloss.Color) string {
	bs := fg(accent)
	iw := w - 4
	var b strings.Builder
	t := " " + title + " "
	b.WriteString(bs.Render("╭─") + sBold.Render(t) + bs.Render(strings.Repeat("─", max(0, w-3-lipgloss.Width(t)))+"╮"))
	pad := withBg("", cPanel, iw+2)
	b.WriteString("\n" + bs.Render("│") + pad + bs.Render("│"))
	for _, l := range lines {
		b.WriteString("\n" + bs.Render("│") + withBg(" "+fit(l, iw)+" ", cPanel, iw+2) + bs.Render("│"))
	}
	b.WriteString("\n" + bs.Render("│") + pad + bs.Render("│"))
	b.WriteString("\n" + bs.Render("╰"+strings.Repeat("─", w-2)+"╯"))
	return b.String()
}

func (m Model) viewPalette() string {
	p := m.palette
	w := clamp(m.w*55/100, 50, 84)
	iw := w - 4
	lines := []string{p.input.View(), sSubtle.Render(strings.Repeat("─", iw))}
	vis, start := m.paletteWindow()
	for i := start; i < len(p.filtered) && i < start+vis; i++ {
		it := p.items[p.filtered[i]]
		key := "   "
		if it.key != "" {
			key = sKey.Render(fmt.Sprintf("%-3s", it.key))
		}
		ls := sText
		if it.danger {
			ls = fg(lipgloss.Color("#f5a3b3"))
		}
		left := key + " " + ls.Render(it.label)
		row := " " + spread(left, sMuted.Render(ansi.Truncate(it.hint, iw/2, "…")), iw-1)
		if i == p.cur {
			row = withBg(fg(cAccent).Render("▌")+spread(left, sMuted.Render(ansi.Truncate(it.hint, iw/2, "…")), iw-1), cSelBg, iw)
		}
		lines = append(lines, row)
	}
	if len(p.filtered) == 0 {
		lines = append(lines, sMuted.Render("no matching actions"))
	}
	lines = append(lines, "", sMuted.Render(fmt.Sprintf("%d actions", len(p.filtered)))+"   "+sKey.Render("↑↓")+sMuted.Render(" move  ")+sKey.Render("enter")+sMuted.Render(" run  ")+sKey.Render("esc")+sMuted.Render(" close"))
	return modal(p.title, lines, w, cAccent)
}

// paletteWindow returns how many items are visible and the first one shown.
func (m Model) paletteWindow() (vis, start int) {
	vis = clamp(m.h-12, 4, 14)
	if p := m.palette; p != nil && p.cur >= vis {
		start = p.cur - vis + 1
	}
	return
}

// boxOrigin returns the top-left cell of a centred overlay box.
func (m Model) boxOrigin(box string) (x, y, w, h int) {
	ls := strings.Split(box, "\n")
	for _, l := range ls {
		w = max(w, lipgloss.Width(l))
	}
	h = len(ls)
	return max(0, (m.w-w)/2), max(0, (m.h-h)/2), w, h
}

// paletteMouse: hover highlights, click runs, wheel scrolls, outside closes.
func (m *Model) paletteMouse(msg tea.MouseMsg) tea.Cmd {
	p := m.palette
	x0, y0, w, h := m.boxOrigin(m.viewPalette())
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		p.cur = max(0, p.cur-1)
		return nil
	case tea.MouseButtonWheelDown:
		p.cur = min(len(p.filtered)-1, p.cur+1)
		return nil
	}
	inside := msg.X >= x0 && msg.X < x0+w && msg.Y >= y0 && msg.Y < y0+h
	vis, start := m.paletteWindow()
	idx := -1
	if j := msg.Y - (y0 + 4); inside && j >= 0 && j < vis && start+j < len(p.filtered) { // border, pad, input, rule
		idx = start + j
	}
	switch {
	case msg.Action == tea.MouseActionMotion:
		if idx >= 0 {
			p.cur = idx
		}
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		if !inside {
			m.palette = nil
			return nil
		}
		if idx >= 0 {
			it := p.items[p.filtered[idx]]
			m.palette = nil
			return it.run(m)
		}
	}
	return nil
}

// confirmMouse: click the confirm button to accept, click outside to cancel.
func (m *Model) confirmMouse(msg tea.MouseMsg) tea.Cmd {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return nil
	}
	c := m.confirm
	x0, y0, w, h := m.boxOrigin(m.viewConfirm())
	if msg.X < x0 || msg.X >= x0+w || msg.Y < y0 || msg.Y >= y0+h {
		m.confirm = nil
		return nil
	}
	btnY := y0 + h - 3 // button row sits above the bottom pad + border
	if msg.Y == btnY {
		switch rel := msg.X - (x0 + 2); {
		case rel >= 0 && rel < len(confirmYes):
			m.confirm = nil
			return c.yes(m)
		case rel >= len(confirmYes)+3:
			m.confirm = nil
		}
	}
	return nil
}

const confirmYes = " y  confirm "

func (m Model) viewConfirm() string {
	c := m.confirm
	w := clamp(m.w*45/100, 50, 74)
	col := cAccent
	if c.danger {
		col = cRed
	}
	lines := []string{}
	for _, b := range c.body {
		lines = append(lines, sText.Render(b))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, withBg(sBold.Render(confirmYes), Dim(col, 0.55), len(confirmYes))+"   "+sMuted.Render("n / esc  cancel"))
	return modal(c.title, lines, w, col)
}

var helpSections = []struct {
	title string
	keys  [][2]string
}{
	{"Global", [][2]string{{"1-6", "containers · images · volumes · networks · events · doctor"}, {"0", "hosts: every Docker environment, switch between them"}, {"x", "context menu"}, {"ctrl+p  :", "command palette"}, {"b", "bulk commands"}, {"C", "compose: up · down · pull · build · logs"}, {"c", "custom commands (config file)"}, {"!", "alerts (click the ▲ badge too)"}, {"tab / enter", "focus detail pane"}, {"esc", "back / clear"}, {"+  _", "screen mode"}, {"?", "this help"}, {"q", "quit"}}},
	{"Containers", [][2]string{{"s", "start / stop"}, {"r", "restart"}, {"p", "pause / unpause"}, {"d", "remove"}, {"K", "kill"}, {"E", "exec shell"}, {"a", "attach"}, {"w", "open in browser"}, {"m", "raw logs (ctrl+c returns)"}, {"e", "hide / show stopped"}, {"o", "sort: name · cpu · mem"}, {"/", "filter list"}}},
	{"Detail", [][2]string{{"[  ]", "previous / next tab"}, {"t  T", "chart window 1m · 5m · 15m · 1h"}, {"/", "search logs (regex)"}, {"f", "follow logs"}, {"z", "wrap logs"}, {"i", "log timestamps"}, {"v", "reveal masked env secrets"}, {"↑↓ pgup pgdn", "scroll"}}},
	{"Resources", [][2]string{{"d", "remove selected"}, {"P", "prune unused"}, {"J", "raw JSON inspect"}}},
	{"Mouse", [][2]string{{"click", "select rows, tabs, time windows"}, {"wheel", "scroll list or detail"}}},
}

func (m Model) viewHelp() string {
	w := clamp(m.w*70/100, 60, 110)
	colW := (w - 6) / 2
	var left, right []string
	for i, s := range helpSections {
		block := []string{fg(cAccent2).Bold(true).Render(s.title)}
		for _, k := range s.keys {
			block = append(block, sKey.Render(fmt.Sprintf("%-14s", k[0]))+sText.Render(ansi.Truncate(k[1], colW-15, "…")))
		}
		block = append(block, "")
		if i%2 == 0 {
			left = append(left, block...)
		} else {
			right = append(right, block...)
		}
	}
	n := max(len(left), len(right))
	var lines []string
	for i := 0; i < n; i++ {
		lines = append(lines, fit(at(left, i), colW)+"  "+fit(at(right, i), colW))
	}
	lines = append(lines, sMuted.Render("berth — press ? or esc to close"))
	return modal("Keyboard shortcuts", lines, w, cAccent)
}

// ---------------------------------------------------------------- os helpers

func execCommand(argv []string) *exec.Cmd { return exec.Command(argv[0], argv[1:]...) }

func openURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		if p, err := exec.LookPath("wslview"); err == nil {
			cmd = exec.Command(p, u)
		} else if p, err := exec.LookPath("explorer.exe"); err == nil {
			cmd = exec.Command(p, u)
		} else {
			cmd = exec.Command("xdg-open", u)
		}
	}
	return cmd.Start()
}

// copyText writes to the clipboard via OSC 52 (works over SSH and in
// Windows Terminal) — no external tools needed.
func copyText(m *Model, s string) tea.Cmd {
	m.flash("copied "+ansi.Truncate(s, 24, "…"), false)
	return func() tea.Msg {
		_, _ = os.Stdout.WriteString(ansi.SetSystemClipboard(s))
		return nil
	}
}
