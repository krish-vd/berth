package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/krish-vd/berth/internal/doctor"
)

type docMsg struct {
	gen  int
	i    int
	r    doctor.Result
	done bool
}

type docState struct {
	path    string
	file    *doctor.File
	err     error
	res     []doctor.Result
	gen     int
	ch      chan docMsg
	running bool
	lastRun time.Time
	tried   bool // checks were loaded (or loading failed) at least once
}

// DoctorPath lets main pass --doctor through.
var DoctorPath string

func (m *Model) startDoctor() tea.Cmd {
	d := &m.doc
	d.tried = true
	d.path = doctor.Find(DoctorPath)
	d.file, d.err = doctor.Load(d.path)
	if d.err != nil {
		return nil
	}
	d.gen++
	d.res = make([]doctor.Result, len(d.file.Checks))
	d.running, d.lastRun = true, time.Now()
	ch := make(chan docMsg, len(d.file.Checks)*2+1)
	d.ch = ch
	gen, cli, checks := d.gen, m.cli, d.file.Checks
	go func() {
		doctor.RunAll(cli, checks, func(i int, r doctor.Result) { ch <- docMsg{gen: gen, i: i, r: r} })
		ch <- docMsg{gen: gen, done: true}
		close(ch)
	}()
	return waitDoc(ch)
}

func waitDoc(ch chan docMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *Model) onDoc(msg docMsg) tea.Cmd {
	d := &m.doc
	if msg.gen != d.gen {
		return nil
	}
	if msg.done {
		d.running = false
		p, w, f := d.counts()
		if f > 0 {
			m.flash(fmt.Sprintf("doctor: %d failing, %d warnings, %d passed", f, w, p), true)
		} else {
			m.flash(fmt.Sprintf("doctor: all %d checks passed%s", p+w, map[bool]string{true: fmt.Sprintf(" (%d warnings)", w), false: ""}[w > 0]), false)
		}
		return nil
	}
	if msg.i < len(d.res) {
		d.res[msg.i] = msg.r
	}
	return waitDoc(d.ch)
}

func (d docState) counts() (pass, warn, fail int) {
	for _, r := range d.res {
		switch r.Status {
		case doctor.Pass:
			pass++
		case doctor.Warn:
			warn++
		case doctor.Fail:
			fail++
		}
	}
	return
}

func (m *Model) doctorKey(ks string) tea.Cmd {
	switch ks {
	case "r":
		if !m.doc.running {
			return m.startDoctor()
		}
	case "f", "enter":
		d := m.doc
		i := m.rcur[VDoctor]
		if d.file == nil || i >= len(d.file.Checks) {
			return nil
		}
		c := d.file.Checks[i]
		if c.Fix == "" {
			if c.FixHint != "" {
				m.flash("no automatic fix — see the hint", true)
			}
			return nil
		}
		m.ask("Run fix for “"+c.Name+"”?", append([]string{"This runs on the Docker host:", ""}, wrapLines(c.Fix, 64)...), true, func(m *Model) tea.Cmd {
			return tea.Sequence(m.runInTerminal("fix: "+c.Name, c.Fix, true), func() tea.Msg { return rerunDoctorMsg{} })
		})
	}
	return nil
}

type rerunDoctorMsg struct{}

var docGlyph = map[doctor.Status]glyph{
	doctor.Pending: {"·", cSubtle}, doctor.Pass: {"✓", cGreen}, doctor.Warn: {"▲", cYellow}, doctor.Fail: {"✗", cRed},
}

func (m Model) docGlyph(r doctor.Result) glyph {
	if r.Status == doctor.Running {
		return glyph{string(spinner[m.frame%len(spinner)]), cAccent}
	}
	return docGlyph[r.Status]
}

func (m Model) viewDoctorList(w, h int) string {
	iw := w - 2
	d := m.doc
	var lines []string
	if d.file == nil && d.err == nil {
		return panel(sBold.Render("Doctor"), "", []string{"", sAccent.Render(" " + string(spinner[m.frame%len(spinner)]) + " loading checks…")}, w, h, true)
	}
	if d.err != nil {
		lines = []string{"", fg(cYellow).Render(" No doctor checks found."), "", sMuted.Render(" berth looks for .berth/doctor.yaml in the"), sMuted.Render(" current folder or a parent, then"), sMuted.Render(" ~/.config/berth/doctor.yaml, $BERTH_DOCTOR"), sMuted.Render(" or --doctor <file>.")}
		return panel(sBold.Render("Doctor"), "", lines, w, h, true)
	}
	group := ""
	rowFor := map[int]int{}
	var rows []string
	for i, c := range d.file.Checks {
		if c.Group != group {
			group = c.Group
			head := " " + fg(cAccent2).Bold(true).Render("▾ "+group) + " "
			rows = append(rows, head+sSubtle.Render(strings.Repeat("─", max(0, iw-lipgloss.Width(head)-1))))
		}
		rowFor[i] = len(rows)
		g := m.docGlyph(d.res[i])
		sum := d.res[i].Summary
		row := fg(g.color).Bold(true).Render(g.r) + " " + fit(sText.Render(c.Name), iw/2) + " " + sMuted.Render(ansi.Truncate(sum, max(4, iw-iw/2-5), "…"))
		if i == m.rcur[VDoctor] {
			rows = append(rows, m.selRow(row, iw, m.focus == 0))
		} else {
			rows = append(rows, " "+row)
		}
	}
	// keep the selected row visible
	vis := h - 2
	off := 0
	if r := rowFor[m.rcur[VDoctor]]; r >= vis {
		off = r - vis + 1
	}
	lines = rows[off:]
	p, wn, f := d.counts()
	right := fg(cGreen).Render(fmt.Sprintf("✓ %d", p)) + "   " + fg(cYellow).Render(fmt.Sprintf("▲ %d", wn)) + "   " + fg(cRed).Render(fmt.Sprintf("✗ %d", f))
	if d.running {
		right = sAccent.Render(string(spinner[m.frame%len(spinner)])+" running") + "  " + right
	}
	title := sBold.Render("Doctor")
	if d.file.Title != "" {
		title += sMuted.Render(" · " + d.file.Title)
	}
	return panel(title, right, lines, w, h, m.focus == 0)
}

func (m Model) viewDoctorDetail(w, h int) string {
	iw, ih := w-2, h-2
	d := m.doc
	if d.err != nil || d.file == nil || len(d.file.Checks) == 0 {
		msg := "no checks"
		if d.err != nil {
			msg = d.err.Error()
		}
		return panel(sBold.Render("—"), "", []string{"", sMuted.Render("  " + msg)}, w, h, false)
	}
	i := min(m.rcur[VDoctor], len(d.file.Checks)-1)
	c, r := d.file.Checks[i], d.res[i]
	g := m.docGlyph(r)
	status := map[doctor.Status]string{doctor.Pending: "not run", doctor.Running: "running…", doctor.Pass: "passed", doctor.Warn: "warning", doctor.Fail: "failed"}[r.Status]
	var l []string
	l = append(l, " "+fg(g.color).Bold(true).Render(g.r+" "+status)+sSubtle.Render("  │  ")+sMuted.Render(c.Group))
	if !r.At.IsZero() {
		l[0] += sSubtle.Render("  │  ") + sMuted.Render(fmt.Sprintf("took %.1fs · %s", r.Duration.Seconds(), r.At.Format("15:04:05")))
	}
	l = append(l, "", " "+sBold.Render(r.Summary))
	if r.Details != "" {
		l = append(l, "")
		for _, ln := range strings.Split(r.Details, "\n") {
			l = append(l, "   "+sText.Render(ansi.Strip(ln)))
		}
	}
	if c.Fix != "" && r.Status != doctor.Pass {
		l = append(l, "", " "+fg(cAccent2).Bold(true).Render("Fix")+"  "+sKey.Render("f")+sMuted.Render(" to run (asks first)"))
		for _, ln := range wrapLines(c.Fix, iw-6) {
			l = append(l, "   "+fg(cTeal).Render(ln))
		}
	}
	if c.FixHint != "" && r.Status != doctor.Pass {
		l = append(l, "", " "+fg(cAccent2).Bold(true).Render("How to fix"))
		for _, ln := range wrapLines(c.FixHint, iw-6) {
			l = append(l, "   "+sText.Render(ln))
		}
	}
	right := sMuted.Render("r re-run all")
	if !d.lastRun.IsZero() {
		right = sMuted.Render("last run "+ago(d.lastRun)+" ago · ") + sKey.Render("r") + sMuted.Render(" re-run")
	}
	return panel(sBold.Render(c.Name), right, m.scroll(l, m.dScroll, iw, ih, 0), w, h, m.focus == 1)
}

// doctorLayout mirrors viewDoctorList's rows: each entry is a check index,
// or -1 for a group heading, plus the scroll offset in use. Mouse clicks
// map through this so headings don't shift the selection.
func (m Model) doctorLayout(h int) (rows []int, off int) {
	if m.doc.file == nil {
		return nil, 0
	}
	group := ""
	sel := 0
	for i, c := range m.doc.file.Checks {
		if c.Group != group {
			group = c.Group
			rows = append(rows, -1)
		}
		if i == m.rcur[VDoctor] {
			sel = len(rows)
		}
		rows = append(rows, i)
	}
	if vis := h - 2; sel >= vis {
		off = sel - vis + 1
	}
	return rows, off
}

func wrapLines(s string, w int) []string {
	var out []string
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out = append(out, strings.Split(ansi.Hardwrap(ln, max(10, w), true), "\n")...)
	}
	return out
}
