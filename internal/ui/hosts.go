package ui

// Environments view (key 0): every Docker host berth can manage — this
// machine, saved hosts and docker contexts — probed live, side by side.
// Enter switches the whole session to the selected host.

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/krish-vd/berth/internal/config"
	"github.com/krish-vd/berth/internal/hosts"
)

// SwitchTo is set when the user picks another environment; main reads it
// after the program exits and starts a new session on that host.
var SwitchTo string

// CurrentHost is the endpoint this session is connected to, and HostLabel
// its friendly name when it has one (both set by main).
var CurrentHost, HostLabel string

const probeTimeout = 8 * time.Second

type hostState struct {
	list    []hosts.Host
	st      []hosts.Status
	gen     int
	pending int
	tried   bool
	at      time.Time
	form    *hostForm
}

type hostForm struct {
	name, url textinput.Model
	field     int
	err       string
	orig      string // URL being edited, "" when adding
}

type hostListMsg struct {
	gen  int
	list []hosts.Host
}

type hostProbeMsg struct {
	gen, i int
	st     hosts.Status
}

func (m *Model) startHosts() tea.Cmd {
	h := &m.hosts
	h.tried = true
	h.gen++
	gen := h.gen
	return func() tea.Msg { return hostListMsg{gen: gen, list: hosts.Discover(CurrentHost)} }
}

func (m *Model) onHostList(msg hostListMsg) tea.Cmd {
	h := &m.hosts
	if msg.gen != h.gen {
		return nil
	}
	old := map[string]hosts.Status{}
	for i, x := range h.list {
		if i < len(h.st) {
			old[x.URL] = h.st[i]
		}
	}
	h.list, h.st = msg.list, make([]hosts.Status, len(msg.list))
	for i, x := range h.list {
		h.st[i] = old[x.URL] // keep the last reading on screen while re-probing
	}
	m.rcur[VHosts] = clamp(m.rcur[VHosts], 0, max(0, len(h.list)-1))
	return m.probeAll()
}

func (m *Model) probeAll() tea.Cmd {
	h := &m.hosts
	h.at, h.pending = time.Now(), len(h.list)
	gen := h.gen
	cmds := make([]tea.Cmd, len(h.list))
	for i, x := range h.list {
		i, x := i, x
		cmds[i] = func() tea.Msg { return hostProbeMsg{gen: gen, i: i, st: hosts.Probe(x, probeTimeout)} }
	}
	return tea.Batch(cmds...)
}

func (m *Model) onHostProbe(msg hostProbeMsg) {
	h := &m.hosts
	if msg.gen != h.gen || msg.i >= len(h.st) {
		return
	}
	h.st[msg.i] = msg.st
	h.pending = max(0, h.pending-1)
}

func (m *Model) hostKey(ks string) tea.Cmd {
	h := &m.hosts
	i := m.rcur[VHosts]
	switch ks {
	case "r":
		return m.startHosts()
	case "a":
		m.openHostForm("", "")
	case "e":
		if i < len(h.list) && h.list[i].Source != hosts.SourceLocal {
			m.openHostForm(h.list[i].Name, h.list[i].URL)
		}
	case "d":
		if i >= len(h.list) {
			return nil
		}
		x := h.list[i]
		if x.Source != hosts.SourceConfig {
			m.flash("only saved hosts can be removed here — "+x.Source+" entries live elsewhere", true)
			return nil
		}
		m.ask("Remove “"+x.Name+"”?", []string{x.URL, "", "It is only removed from berth's list; nothing on the host changes."}, true, func(m *Model) tea.Cmd {
			if err := config.RemoveHost(x.URL); err != nil {
				m.flash(err.Error(), true)
				return nil
			}
			m.flash("removed "+x.Name, false)
			return m.startHosts()
		})
	case "enter":
		if i >= len(h.list) {
			return nil
		}
		return m.switchHost(i)
	}
	return nil
}

func (m *Model) switchHost(i int) tea.Cmd {
	x, st := m.hosts.list[i], m.hosts.st[i]
	if hostsKey(x.URL) == hostsKey(CurrentHost) {
		m.flash("already connected to "+x.Name, false)
		return nil
	}
	do := func(m *Model) tea.Cmd {
		SwitchTo = x.URL
		m.logs.stop()
		return tea.Quit
	}
	if st.Probed && !st.OK {
		m.ask("Switch to “"+x.Name+"” anyway?", []string{"The last probe failed:", st.Err, "", "berth will keep trying to connect; press 0 there to come back."}, false, do)
		return nil
	}
	return do(m)
}

func hostsKey(u string) string { return strings.TrimRight(strings.ToLower(u), "/") }

// ---------------------------------------------------------------- add / edit form

func (m *Model) openHostForm(name, u string) {
	mk := func(ph, v string) textinput.Model {
		t := textinput.New()
		t.Prompt, t.Placeholder, t.CharLimit = "", ph, 200
		t.SetValue(v)
		return t
	}
	f := &hostForm{name: mk("e.g. QA server", name), url: mk("ssh://user@host  ·  tcp://host:2375", u)}
	if u == "" {
		f.field = 1
		f.url.Focus()
	} else {
		f.name.Focus()
	}
	f.orig = u
	m.hosts.form = f
}

func (m *Model) hostFormKey(k tea.KeyMsg) tea.Cmd {
	f := m.hosts.form
	switch k.String() {
	case "esc":
		m.hosts.form = nil
		return nil
	case "tab", "shift+tab", "up", "down":
		f.field = 1 - f.field
		if f.field == 0 {
			f.name.Focus()
			f.url.Blur()
		} else {
			f.url.Focus()
			f.name.Blur()
		}
		return nil
	case "enter":
		raw := strings.TrimSpace(f.url.Value())
		if !strings.Contains(raw, "://") && raw != "" { // "user@host" means ssh
			raw = "ssh://" + raw
		}
		u, err := url.Parse(raw)
		switch {
		case raw == "" || err != nil || u.Host == "" && u.Scheme != "unix" && u.Scheme != "wsl":
			f.err = "enter an endpoint like ssh://user@host or tcp://host:2375"
			return nil
		case u.Scheme != "ssh" && u.Scheme != "tcp" && u.Scheme != "unix" && u.Scheme != "wsl":
			f.err = "supported: ssh://  tcp://  unix://  wsl://"
			return nil
		}
		name := strings.TrimSpace(f.name.Value())
		if name == "" {
			name = u.Hostname()
		}
		if f.orig != "" && f.orig != raw {
			_ = config.RemoveHost(f.orig)
		}
		if err := config.AddHost(config.Host{Name: name, URL: raw}); err != nil {
			f.err = err.Error()
			return nil
		}
		m.hosts.form = nil
		m.flash("saved "+name+" — probing…", false)
		return m.startHosts()
	}
	var cmd tea.Cmd
	if f.field == 0 {
		f.name, cmd = f.name.Update(k)
	} else {
		f.url, cmd = f.url.Update(k)
	}
	f.err = ""
	return cmd
}

func (m Model) viewHostForm() string {
	f := m.hosts.form
	w := clamp(m.w*50/100, 56, 80)
	label := func(s string, on bool) string {
		if on {
			return fg(cAccent2).Bold(true).Render("› " + s)
		}
		return sMuted.Render("  " + s)
	}
	lines := []string{
		label("Name", f.field == 0), "    " + f.name.View(), "",
		label("Endpoint", f.field == 1), "    " + f.url.View(), "",
		sMuted.Render("  ssh://user@host       Docker over SSH (key auth, docker on the host)"),
		sMuted.Render("  tcp://host:2375       Docker API exposed on TCP"),
		sMuted.Render("  user@host             short for ssh://"),
	}
	if f.err != "" {
		lines = append(lines, "", fg(cRed).Render("  "+f.err))
	}
	lines = append(lines, "", sKey.Render("enter")+sMuted.Render(" save   ")+sKey.Render("tab")+sMuted.Render(" next field   ")+sKey.Render("esc")+sMuted.Render(" cancel"))
	title := "Add environment"
	if f.orig != "" {
		title = "Edit environment"
	}
	return modal(title, lines, w, cAccent)
}

// ---------------------------------------------------------------- rendering

func (m Model) hostGlyph(st hosts.Status, pending bool) (string, lipgloss.Color) {
	switch {
	case !st.Probed && pending:
		return string(spinner[m.frame%len(spinner)]), cAccent
	case !st.Probed:
		return "○", cMuted
	case !st.OK:
		return "✗", cRed
	case st.Unhealthy > 0:
		return "▲", cYellow
	}
	return "●", cGreen
}

func (m Model) viewHostList(w, h int) string {
	iw := w - 2
	hs := m.hosts
	if len(hs.list) == 0 {
		return panel(sBold.Render("Environments"), "", []string{"", sAccent.Render(" " + string(spinner[m.frame%len(spinner)]) + " finding environments…")}, w, h, true)
	}
	var rows []string
	for i, x := range hs.list {
		st := hs.st[i]
		g, col := m.hostGlyph(st, hs.pending > 0)
		name := x.Name
		if hostsKey(x.URL) == hostsKey(CurrentHost) {
			name += " " + "◂"
		}
		right := ""
		switch {
		case st.OK:
			right = fg(cGreen).Render(fmt.Sprint(st.Running)) + sMuted.Render(" up")
			if st.Unhealthy > 0 {
				right += "  " + fg(cYellow).Render(fmt.Sprint(st.Unhealthy)) + sMuted.Render(" sick")
			}
		case st.Probed:
			right = fg(cRed).Render("unreachable")
		}
		left := fg(col).Bold(true).Render(g) + " " + sText.Render(ansi.Truncate(name, max(6, iw-lipgloss.Width(right)-6), "…"))
		row := spread(left, right+" ", iw-1)
		if i == m.rcur[VHosts] {
			rows = append(rows, m.selRow(row, iw, m.focus == 0))
		} else {
			rows = append(rows, " "+row)
		}
		rows = append(rows, "   "+sSubtle.Render(ansi.Truncate(x.URL+"  ·  "+x.Source, iw-4, "…")))
	}
	vis := h - 2
	off := 0
	if r := m.rcur[VHosts]*2 + 1; r >= vis {
		off = r - vis + 1
	}
	up, total := 0, 0
	for _, st := range hs.st {
		if st.OK {
			up++
			total += st.Running
		}
	}
	right := fg(cGreen).Render(fmt.Sprintf("%d/%d online", up, len(hs.list))) + sMuted.Render(fmt.Sprintf(" · %d running", total))
	if hs.pending > 0 && m.hostsFirstProbe() { // later re-probes run quietly behind the last readings
		right = sAccent.Render(string(spinner[m.frame%len(spinner)])+" probing") + "  " + right
	}
	return panel(sBold.Render("Environments"), right, rows[min(off, len(rows)):], w, h, m.focus == 0)
}

func (m Model) viewHostDetail(w, h int) string {
	iw, ih := w-2, h-2
	hs := m.hosts
	if len(hs.list) == 0 {
		return panel(sBold.Render("—"), "", nil, w, h, false)
	}
	i := min(m.rcur[VHosts], len(hs.list)-1)
	x, st := hs.list[i], hs.st[i]
	g, col := m.hostGlyph(st, hs.pending > 0)
	here := hostsKey(x.URL) == hostsKey(CurrentHost)
	var l []string
	status := "not probed yet"
	switch {
	case !st.Probed && hs.pending > 0:
		status = "probing…"
	case st.OK:
		status = "online"
	case st.Probed:
		status = "unreachable"
	}
	head := " " + fg(col).Bold(true).Render(g+" "+status)
	if here {
		head += sSubtle.Render("  │  ") + fg(cAccent2).Bold(true).Render("connected now")
	}
	if st.Probed {
		head += sSubtle.Render("  │  ") + sMuted.Render(fmt.Sprintf("%d ms · probed %s ago", st.Latency.Milliseconds(), ago(st.At)))
	}
	l = append(l, head, "")
	kv := func(k, v string) string { return "   " + sMuted.Render(fmt.Sprintf("%-12s", k)) + sText.Render(v) }
	l = append(l, kv("Endpoint", x.URL), kv("Source", x.Source))
	if st.OK {
		in := st.Info
		l = append(l, kv("Host", in.Name), kv("System", in.OperatingSystem), kv("Engine", "Docker "+in.ServerVersion),
			kv("Capacity", fmt.Sprintf("%d CPU · %s memory", in.NCPU, HumanBytes(float64(in.MemTotal)))), "")
		bar := func(n, of int, c lipgloss.Color) string {
			if of == 0 {
				return ""
			}
			return fg(c).Render(strings.Repeat("█", max(1, n*24/of)))
		}
		total := st.Running + st.Stopped + st.Unhealthy
		l = append(l, " "+fg(cAccent2).Bold(true).Render("Containers")+sMuted.Render(fmt.Sprintf("  %d total · %d compose project%s · %d images", total, st.Projects, plural(st.Projects), st.Info.Images)))
		if st.Running > 0 {
			l = append(l, "   "+fg(cGreen).Render(fmt.Sprintf("%-4d", st.Running))+sMuted.Render("running    ")+bar(st.Running, total, cGreen))
		}
		if st.Unhealthy > 0 {
			l = append(l, "   "+fg(cYellow).Render(fmt.Sprintf("%-4d", st.Unhealthy))+sMuted.Render("unhealthy  ")+bar(st.Unhealthy, total, cYellow))
		}
		if st.Stopped > 0 {
			l = append(l, "   "+sText.Render(fmt.Sprintf("%-4d", st.Stopped))+sMuted.Render("stopped    ")+bar(st.Stopped, total, cMuted))
		}
	} else if st.Probed {
		l = append(l, "", " "+fg(cRed).Bold(true).Render("Why it failed"))
		for _, ln := range wrapLines(st.Err, iw-6) {
			l = append(l, "   "+sText.Render(ln))
		}
		l = append(l, "", " "+fg(cAccent2).Bold(true).Render("Checklist"))
		for _, ln := range []string{
			"ssh://  — `ssh user@host docker ps` must work from this machine without a password (key auth)",
			"tcp://  — the engine must listen on that port (dockerd -H tcp://0.0.0.0:2375), reachable through the firewall",
			"the remote user needs access to Docker (member of the docker group)",
		} {
			for j, w := range wrapLines(ln, iw-8) {
				p := "   · "
				if j > 0 {
					p = "     "
				}
				l = append(l, p+sMuted.Render(w))
			}
		}
	}
	l = append(l, "", " "+fg(cAccent2).Bold(true).Render("Actions"))
	if !here {
		l = append(l, "   "+sKey.Render("enter")+sMuted.Render("  switch berth to this environment — every view, log, shell and doctor check follows"))
	}
	l = append(l, "   "+sKey.Render("a")+sMuted.Render("      add an environment"))
	if x.Source == hosts.SourceConfig {
		l = append(l, "   "+sKey.Render("e  d")+sMuted.Render("   edit · remove from berth"))
	}
	l = append(l, "   "+sKey.Render("r")+sMuted.Render("      probe every environment again"))
	return panel(sBold.Render(x.Name), sMuted.Render(x.Source), m.scroll(l, m.dScroll, iw, ih, 0), w, h, m.focus == 1)
}

func (m Model) hostsFirstProbe() bool {
	for _, st := range m.hosts.st {
		if !st.Probed {
			return true
		}
	}
	return false
}
