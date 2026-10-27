// Package ui is berth's Bubble Tea interface.
package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/krish-vd/berth/internal/docker"
	"github.com/krish-vd/berth/internal/metrics"
)

const fps = 10

type View int

const (
	VContainers View = iota
	VImages
	VVolumes
	VNetworks
	VEvents
	VDoctor
)

var viewNames = []string{"Containers", "Images", "Volumes", "Networks", "Events", "Doctor"}

type Tab int

const (
	TMetrics Tab = iota
	TLogs
	TEnv
	TConfig
	TTop
	TInspect
)

var tabNames = []string{"Metrics", "Logs", "Env", "Config", "Top", "Inspect"}

var windows = []struct {
	label string
	secs  int
}{{"1m", 60}, {"5m", 300}, {"15m", 900}, {"1h", 3600}}

var spinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// ---- messages ----

type (
	snapMsg   metrics.Snapshot
	frameMsg  time.Time
	eventMsg  docker.Event
	actionMsg struct {
		key, label string
		err        error
	}
	execMsg    struct{ err error }
	inspectMsg struct {
		id  string
		ins *docker.Inspect
		err error
	}
	topMsg struct {
		id  string
		top *docker.Top
		err error
	}
	resMsg struct {
		images   []docker.Image
		networks []docker.Network
		volumes  []docker.Volume
	}
	dfMsg      struct{ du *docker.DiskUsage }
	historyMsg struct {
		id     string
		layers []docker.Layer
	}
)

// FrameMsg advances animations by one frame (used by --snapshot).
func FrameMsg() tea.Msg { return frameMsg(time.Now()) }

// SnapMsg wraps a sampler snapshot for delivery via tea.Program.Send.
func SnapMsg(s metrics.Snapshot) tea.Msg { return snapMsg(s) }

// EventMsg wraps an engine event for delivery via tea.Program.Send.
func EventMsg(e docker.Event) tea.Msg { return eventMsg(e) }

type row struct {
	group string
	count int
	c     *docker.Container
}

type anim struct{ cpu, mem Tween }

type span struct {
	at    time.Time
	state string
}

type alert struct {
	at   time.Time
	text string
	kind string // crash | oom | unhealthy
	id   string // container
	name string
}

type Model struct {
	cli *docker.Client
	smp *metrics.Sampler

	snap    metrics.Snapshot
	gotSnap bool

	view  View
	tab   Tab
	focus int // 0 list, 1 detail
	zoom  int // 0 normal, 1 wide detail, 2 detail only

	rows    []row
	cursor  int
	offset  int
	selID   string
	lastSel string
	rcur    [6]int
	roff    [6]int

	w, h   int
	frame  int
	winIdx int

	anims            map[string]*anim
	hostCPU, hostMem Tween
	pending          map[string]string

	insp      map[string]*docker.Inspect
	inspAt    time.Time
	top       *docker.Top
	topID     string
	topAt     time.Time
	dScroll   int
	revealEnv bool
	rawRes    bool   // J: raw JSON in resource detail panes
	rawFor    string // kind:id the rawText belongs to
	rawText   string
	rawWant   string // last raw JSON requested
	logs      logState

	images   []docker.Image
	du       *docker.DiskUsage
	networks []docker.Network
	volumes  []docker.Volume
	resAt    time.Time
	dfAt     time.Time
	dfBusy   bool
	layers   map[string][]docker.Layer

	events   []docker.Event
	timeline map[string][]span
	crashes  map[string][]time.Time // unexpected exits, for the ✗N badge
	stopReq  map[string]time.Time   // last intentional stop/kill per container
	alerts   []alert

	hideStopped bool
	sortMode    int
	filter      textinput.Model
	filtering   bool

	palette    *palette
	confirm    *confirm
	help       bool
	alertsOpen bool
	alertCur   int

	toast      string
	toastErr   bool
	toastUntil time.Time

	hoverX, hoverY int // last pointer cell, for chart readouts

	doc docState // project health checks (Doctor view)

	ticking bool         // a frame tick is scheduled
	cache   *renderCache // shared across value copies; written during View
}

var sortNames = []string{"name", "cpu", "memory"}

func New(cli *docker.Client, smp *metrics.Sampler) Model {
	fi := textinput.New()
	fi.Prompt = "/ "
	fi.Placeholder = "filter by name, image or project"
	return Model{
		cli: cli, smp: smp,
		anims: map[string]*anim{}, pending: map[string]string{},
		insp: map[string]*docker.Inspect{}, layers: map[string][]docker.Layer{},
		timeline: map[string][]span{}, crashes: map[string][]time.Time{}, stopReq: map[string]time.Time{},
		filter: fi, logs: newLogState(),
		ticking: true, cache: &renderCache{}, hoverX: -1, hoverY: -1,
	}
}

// StartInDoctor opens the Doctor view at launch (`berth doctor`).
var StartInDoctor bool

type openDoctorMsg struct{}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{frameTick(), m.fetchResources()}
	if StartInDoctor {
		cmds = append(cmds, func() tea.Msg { return openDoctorMsg{} })
	}
	return tea.Batch(cmds...)
}

func frameTick() tea.Cmd {
	return tea.Tick(time.Second/fps, func(t time.Time) tea.Msg { return frameMsg(t) })
}

// ---------------------------------------------------------------- update

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	StatUpdates.Add(1)
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.clampScroll()

	case frameMsg:
		StatFrames.Add(1)
		m.frame++
		if a := m.anims[m.selID]; a != nil { // only the focused container glides
			a.cpu.Step()
			a.mem.Step()
		}
		m.hostCPU.Step()
		m.hostMem.Step()
		if d := m.frameDelay(); d > 0 {
			return m, frameTickD(d)
		}
		m.ticking = false // idle: no redraws until the next data/input event
		return m, nil

	case snapMsg:
		if msg.Err != nil { // keep last good data; surface the error in the footer
			m.snap.Err = msg.Err
			break
		}
		m.snap = metrics.Snapshot(msg)
		m.gotSnap = true
		m.rebuildRows()
		m.retarget()
		m.recordStates()
		cmd = m.periodic()

	case eventMsg:
		m.onEvent(docker.Event(msg))

	case actionMsg:
		delete(m.pending, msg.key)
		if msg.err != nil {
			m.flash(msg.label+" failed: "+msg.err.Error(), true)
		} else {
			m.flash(msg.label+" ✓", false)
		}
		m.resAt = time.Time{}
		cmd = m.fetchResources()

	case execMsg:
		if msg.err != nil {
			m.flash("shell: "+msg.err.Error(), true)
		}
		// Bubble Tea's RestoreTerminal re-enters the alt screen but does not
		// re-enable mouse tracking, so clicks die after any external command.
		cmd = tea.EnableMouseAllMotion

	case inspectMsg:
		if msg.err == nil {
			m.insp[msg.id] = msg.ins
		}

	case topMsg:
		if msg.err == nil {
			m.top, m.topID = msg.top, msg.id
		}

	case dfMsg:
		m.dfBusy = false
		if msg.du != nil {
			m.du = msg.du
		}

	case resMsg:
		if msg.images != nil {
			m.images = msg.images
		}
		if msg.networks != nil {
			m.networks = msg.networks
		}
		if msg.volumes != nil {
			m.volumes = msg.volumes
		}

	case rawMsg:
		if msg.err == nil {
			m.rawFor, m.rawText = msg.key, msg.text
		} else {
			m.rawFor, m.rawText = msg.key, "{\"error\": \""+msg.err.Error()+"\"}"
		}

	case historyMsg:
		m.layers[msg.id] = msg.layers

	case openDoctorMsg:
		m.setView(VDoctor)
		cmd = m.startDoctor()

	case docMsg:
		cmd = m.onDoc(msg)

	case rerunDoctorMsg:
		cmd = m.startDoctor()

	case logBatchMsg:
		if c := m.logs.receive(msg); c != nil {
			cmd = c
		}

	case tea.MouseMsg:
		m.hoverX, m.hoverY = msg.X, msg.Y
		switch {
		case m.confirm != nil:
			cmd = m.confirmMouse(msg)
		case m.palette != nil:
			cmd = m.paletteMouse(msg)
		case m.alertsOpen:
			cmd = m.alertMouse(msg)
		case msg.Action == tea.MouseActionPress && m.alertBadgeHit(msg.X, msg.Y):
			m.openAlerts()
		case m.help:
			if msg.Action == tea.MouseActionPress {
				m.help = false
			}
		case m.scrollMouse(msg): // click or drag on a scrollbar
		case msg.Action != tea.MouseActionMotion:
			m.mouse(msg)
		}

	case tea.KeyMsg:
		var mm tea.Model
		mm, cmd = m.key(msg)
		m = mm.(Model)
	}
	cmds := []tea.Cmd{cmd, m.sync()}
	if !m.ticking {
		if d := m.frameDelay(); d > 0 {
			m.ticking = true
			cmds = append(cmds, frameTickD(d))
		}
	}
	return m, tea.Batch(cmds...)
}

// frameDelay picks the animation cadence: 15fps while values glide or
// spinners spin, ~6fps for slow pulses, 0 (no frames) when nothing moves.
func (m *Model) frameDelay() time.Duration {
	fast := time.Second / fps
	if !m.gotSnap || len(m.pending) > 0 || (m.tab == TLogs && m.logs.id == "" && m.view == VContainers) ||
		(m.view == VVolumes && m.dfBusy) || (m.view == VDoctor && m.doc.running) || !m.hostCPU.Settled() || !m.hostMem.Settled() {
		return fast
	}
	if a := m.anims[m.selID]; a != nil && (!a.cpu.Settled() || !a.mem.Settled()) {
		return fast
	}
	if n := len(m.alerts); n > 0 && time.Since(m.alerts[n-1].at) < 30*time.Second {
		return time.Second / 6 // badge pulses briefly, then holds still
	}
	for _, c := range m.snap.Containers {
		switch {
		case c.State == "restarting", c.Health() == "starting":
			return time.Second / 8 // spinner
		case c.Health() == "unhealthy":
			return time.Second / 4 // slow red pulse
		}
	}
	return 0
}

func frameTickD(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return frameMsg(t) })
}

// sync reacts to selection / tab changes by (re)loading detail data.
func (m *Model) sync() tea.Cmd {
	var cmds []tea.Cmd
	switch m.view {
	case VContainers:
		c := m.selected()
		if c == nil {
			m.logs.stop()
			break
		}
		if c.ID != m.lastSel {
			m.lastSel = c.ID
			m.dScroll = 0
			m.top = nil
			cmds = append(cmds, m.fetchInspect(c.ID), m.fetchTop(c.ID))
		}
		if m.tab == TLogs {
			if ins := m.insp[c.ID]; ins != nil && m.logs.id != c.ID {
				cmds = append(cmds, m.logs.start(m.cli, c.ID, ins.Config.Tty))
			}
		} else if m.logs.id != "" {
			m.logs.stop()
		}
	case VImages:
		m.logs.stop()
		if im := m.selImage(); im != nil {
			if _, ok := m.layers[im.ID]; !ok {
				m.layers[im.ID] = nil
				cmds = append(cmds, m.fetchHistory(im.ID))
			}
		}
	default:
		m.logs.stop()
	}
	// however the Doctor view was opened (key, click, palette), load it once
	if m.view == VDoctor && !m.doc.tried {
		cmds = append(cmds, m.startDoctor())
	}
	if kind, id := m.rawKey(); kind != "" && m.rawWant != kind+":"+id {
		m.rawWant = kind + ":" + id
		cmds = append(cmds, m.fetchRaw(kind, id))
	}
	return tea.Batch(cmds...)
}

// periodic refreshes slow-moving data on the 1s sampler cadence.
func (m *Model) periodic() tea.Cmd {
	var cmds []tea.Cmd
	now := time.Now()
	if c := m.selected(); c != nil && m.view == VContainers {
		if now.Sub(m.inspAt) > 5*time.Second {
			m.inspAt = now
			cmds = append(cmds, m.fetchInspect(c.ID))
		}
		if m.tab == TTop && now.Sub(m.topAt) > 2*time.Second {
			m.topAt = now
			cmds = append(cmds, m.fetchTop(c.ID))
		}
	}
	if now.Sub(m.resAt) > 15*time.Second {
		cmds = append(cmds, m.fetchResources())
	}
	return tea.Batch(cmds...)
}

func (m *Model) mouse(msg tea.MouseMsg) {
	lw := m.listWidth()
	inDetail := msg.X >= lw || m.zoom == 2 || m.view == VEvents
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if inDetail {
			m.scrollDetail(-3)
		} else {
			m.move(-1)
		}
	case tea.MouseButtonWheelDown:
		if inDetail {
			m.scrollDetail(3)
		} else {
			m.move(1)
		}
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return
		}
		if msg.Y == 0 && m.hitHeaderTabs(msg.X) {
			return
		}
		if msg.Y == headerHeight && m.hitDetailChrome(msg.X) {
			return
		}
		if inDetail {
			m.focus = 1
			return
		}
		m.focus = 0
		i := msg.Y - headerHeight - 1
		if m.view == VContainers {
			i += m.offset
			if i >= 0 && i < len(m.rows) && m.rows[i].c != nil {
				m.cursor = i
				m.selID = m.rows[i].c.ID
			}
		} else if m.view == VDoctor {
			rows, off := m.doctorLayout(m.bodyHeight())
			if r := off + i; i >= 0 && r < len(rows) && rows[r] >= 0 {
				m.rcur[VDoctor] = rows[r]
				m.dScroll = 0
			}
		} else if n := m.resCount(); n > 0 {
			i += m.roff[m.view]
			if i >= 0 && i < n {
				m.rcur[m.view] = i
			}
		}
	}
}

// ---------------------------------------------------------------- keys

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	ks := k.String()

	if m.confirm != nil {
		switch ks {
		case "y", "Y", "enter":
			c := m.confirm
			m.confirm = nil
			return m, c.yes(&m)
		case "n", "N", "esc", "q":
			m.confirm = nil
		}
		return m, nil
	}
	if m.palette != nil {
		return m.paletteKey(k)
	}
	if m.alertsOpen {
		return m, m.alertKey(k)
	}
	if m.help {
		if ks == "?" || ks == "esc" || ks == "q" {
			m.help = false
		}
		return m, nil
	}
	if m.filtering {
		switch ks {
		case "esc":
			m.filtering = false
			m.filter.SetValue("")
			m.filter.Blur()
		case "enter":
			m.filtering = false
			m.filter.Blur()
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(k)
			m.rebuildRows()
			return m, cmd
		}
		m.rebuildRows()
		return m, nil
	}
	if m.logs.searching {
		return m, m.logs.searchKey(k)
	}

	switch ks {
	case "ctrl+c", "q":
		m.logs.stop()
		return m, tea.Quit
	case "?":
		m.help = true
		return m, nil
	case "!":
		m.openAlerts()
		return m, nil
	case "x":
		m.openPalette("Menu", m.contextItems())
		return m, nil
	case "ctrl+p", ":":
		its := m.contextItems()
		if m.view == VContainers {
			its = append(its, m.composeItems()...)
		}
		m.openPalette("Command palette", append(its, m.globalItems()...))
		return m, nil
	case "b":
		m.openPalette("Bulk commands", m.bulkItems())
		return m, nil
	case "c":
		if m.view == VContainers {
			m.openPalette("Custom commands", m.customItems())
			return m, nil
		}
	case "C":
		if m.view == VContainers {
			title := "Compose"
			if c := m.selected(); c != nil {
				if t, ok := composeOf(c); ok {
					title = "Compose · " + t.project
					if t.service != "" {
						title += " / " + t.service
					}
				}
			}
			m.openPalette(title, m.composeItems())
		}
		return m, nil
	case "1", "2", "3", "4", "5", "6":
		m.setView(View(ks[0] - '1'))
		if m.view == VDoctor && m.doc.file == nil && !m.doc.running {
			return m, tea.Batch(m.fetchResources(), m.startDoctor())
		}
		return m, m.fetchResources()
	case "tab":
		m.focus = 1 - m.focus
		return m, nil
	case "enter":
		m.focus = 1
		return m, nil
	case "esc":
		if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.rebuildRows()
		} else if m.logs.query != "" && m.tab == TLogs {
			m.logs.query = ""
		} else {
			m.focus = 0
		}
		return m, nil
	case "+":
		m.zoom = (m.zoom + 1) % 3
		m.clampScroll()
		return m, nil
	case "_":
		m.zoom = (m.zoom + 2) % 3
		m.clampScroll()
		return m, nil
	case "[":
		m.setTab((m.tab + Tab(len(tabNames)) - 1) % Tab(len(tabNames)))
		return m, nil
	case "]":
		m.setTab((m.tab + 1) % Tab(len(tabNames)))
		return m, nil
	case "t":
		m.winIdx = (m.winIdx + 1) % len(windows)
		return m, nil
	case "T":
		m.winIdx = (m.winIdx + len(windows) - 1) % len(windows)
		return m, nil
	case "/":
		if m.focus == 1 && m.view == VContainers && m.tab == TLogs {
			m.logs.beginSearch()
			return m, textinput.Blink
		}
		if m.view == VContainers {
			m.filtering = true
			m.filter.Focus()
			return m, textinput.Blink
		}
		return m, nil
	}

	// navigation
	if m.focus == 1 || m.view == VEvents {
		switch ks {
		case "up", "k":
			m.scrollDetail(-1)
		case "down", "j":
			m.scrollDetail(1)
		case "pgup", "ctrl+u":
			m.scrollDetail(-m.bodyHeight() / 2)
		case "pgdown", "ctrl+d":
			m.scrollDetail(m.bodyHeight() / 2)
		case "home", "g":
			m.scrollDetail(-1 << 20)
		case "end", "G":
			m.scrollDetail(1 << 20)
		}
	} else {
		switch ks {
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case "pgup", "ctrl+u":
			m.move(-10)
		case "pgdown", "ctrl+d":
			m.move(10)
		case "home", "g":
			m.move(-1 << 20)
		case "end", "G":
			m.move(1 << 20)
		}
	}

	// per-view actions
	switch m.view {
	case VContainers:
		return m, m.containerKey(ks)
	case VImages, VVolumes, VNetworks:
		return m, m.resourceKey(ks)
	case VDoctor:
		return m, m.doctorKey(ks)
	case VEvents:
		if ks == "c" {
			m.events = nil
		}
	}
	return m, nil
}

func (m *Model) containerKey(ks string) tea.Cmd {
	switch ks {
	case "e":
		m.hideStopped = !m.hideStopped
		m.rebuildRows()
		return nil
	case "o":
		m.sortMode = (m.sortMode + 1) % len(sortNames)
		m.rebuildRows()
		m.flash("sort by "+sortNames[m.sortMode], false)
		return nil
	case "m":
		if c := m.selected(); c != nil {
			return m.rawLogs(c)
		}
		return nil
	case "f":
		if m.tab == TLogs {
			m.logs.scroll = 0
		}
		return nil
	case "z":
		m.logs.wrap = !m.logs.wrap
		return nil
	case "i":
		m.logs.ts = !m.logs.ts
		return nil
	case "v":
		m.revealEnv = !m.revealEnv
		return nil
	}
	c := m.selected()
	if c == nil {
		return nil
	}
	switch ks {
	case "s":
		if c.State == "running" || c.State == "restarting" || c.State == "paused" {
			return m.act(c, "stop")
		}
		return m.act(c, "start")
	case "r":
		return m.act(c, "restart")
	case "p":
		if c.State == "paused" {
			return m.act(c, "unpause")
		}
		return m.act(c, "pause")
	case "d":
		m.askRemove(c)
	case "K":
		m.ask("Kill "+c.Name()+"?", []string{"Sends SIGKILL immediately — the process gets no chance to clean up."}, true,
			func(m *Model) tea.Cmd { return m.act(c, "kill") })
	case "E":
		return m.shell(c)
	case "a":
		return m.attach(c)
	case "w":
		m.openBrowser(c)
	}
	return nil
}

func (m *Model) setView(v View) {
	if v == m.view {
		return
	}
	m.view = v
	m.focus = 0
	m.dScroll = 0
}

func (m *Model) setTab(t Tab) {
	m.view = VContainers
	m.tab = t
	m.dScroll = 0
}

func (m *Model) scrollDetail(d int) {
	if m.view == VContainers && m.tab == TLogs {
		m.logs.scroll = clamp(m.logs.scroll-d, 0, m.cache.logMax)
		return
	}
	m.dScroll = max(0, m.dScroll+d)
	m.clampDetail()
}

// ---------------------------------------------------------------- commands

func (m *Model) run(key, label string, f func(ctx context.Context) error) tea.Cmd {
	if key != "" {
		if _, busy := m.pending[key]; busy {
			return nil
		}
		m.pending[key] = label
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		return actionMsg{key: key, label: label, err: f(ctx)}
	}
}

func (m *Model) act(c *docker.Container, verb string) tea.Cmd {
	id := c.ID
	return m.run(c.Name(), verb+" "+c.Name(), func(ctx context.Context) error { return m.cli.Action(ctx, id, verb) })
}

func (m *Model) askRemove(c *docker.Container) {
	body := []string{"Image  " + c.Image, "State  " + c.State}
	if c.State == "running" {
		body = append(body, "", "It is running and will be force-stopped first.")
	}
	id := c.ID
	m.ask("Remove container "+c.Name()+"?", body, true, func(m *Model) tea.Cmd {
		return m.run(c.Name(), "remove "+c.Name(), func(ctx context.Context) error { return m.cli.RemoveContainer(ctx, id, true, false) })
	})
}

func (m *Model) ask(title string, body []string, danger bool, yes func(*Model) tea.Cmd) {
	m.confirm = &confirm{title: title, body: body, danger: danger, yes: yes}
}

func (m *Model) shell(c *docker.Container) tea.Cmd {
	if c.State != "running" {
		m.flash(c.Name()+" is not running", true)
		return nil
	}
	return tea.ExecProcess(m.cli.Exec(c.ID), func(err error) tea.Msg { return execMsg{err} })
}

// rawLogs hands the terminal to `docker logs --follow` (lazydocker's `m`).
// Ctrl+C ends the stream and returns to berth; Bubble Tea ignores the
// signal itself while an external process owns the terminal.
func (m *Model) rawLogs(c *docker.Container) tea.Cmd {
	argv := append(m.cli.DockerCLI(), "logs", "--follow", "--tail", "500", "--timestamps", c.ID)
	cmd := execCommand(argv)
	return tea.Sequence(
		tea.Printf("\n  ◆ berth · logs for %s · press Ctrl+C to return\n", c.Name()),
		tea.ExecProcess(cmd, func(error) tea.Msg { return execMsg{} }), // exit 130 on Ctrl+C is expected
	)
}

func (m *Model) attach(c *docker.Container) tea.Cmd {
	if c.State != "running" {
		m.flash(c.Name()+" is not running", true)
		return nil
	}
	argv := append(m.cli.DockerCLI(), "attach", "--sig-proxy=false", c.ID)
	cmd := execCommand(argv)
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return execMsg{err} })
}

func (m *Model) openBrowser(c *docker.Container) {
	for _, p := range c.Ports {
		if p.PublicPort > 0 {
			u := fmt.Sprintf("http://localhost:%d", p.PublicPort)
			if err := openURL(u); err != nil {
				m.flash("open browser: "+err.Error(), true)
			} else {
				m.flash("opened "+u, false)
			}
			return
		}
	}
	m.flash(c.Name()+" publishes no ports", true)
}

func (m *Model) fetchInspect(id string) tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ins, err := cli.Inspect(ctx, id)
		return inspectMsg{id, ins, err}
	}
}

func (m *Model) fetchTop(id string) tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		t, err := cli.Top(ctx, id)
		return topMsg{id, t, err}
	}
}

func (m *Model) fetchHistory(id string) tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ls, _ := cli.History(ctx, id)
		if ls == nil {
			ls = []docker.Layer{}
		}
		return historyMsg{id, ls}
	}
}

// fetchResources loads images + networks (cheap). Volume sizes come from
// /system/df, which can take many seconds on big hosts, so they are only
// loaded while the Volumes view is open.
func (m *Model) fetchResources() tea.Cmd {
	m.resAt = time.Now()
	cli := m.cli
	light := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var r resMsg
		r.images, _ = cli.Images(ctx)
		r.networks, _ = cli.Networks(ctx)
		r.volumes, _ = cli.Volumes(ctx)
		return r
	}
	if m.view != VVolumes || (m.du != nil && time.Since(m.dfAt) < time.Minute) || m.dfBusy {
		return light
	}
	m.dfAt, m.dfBusy = time.Now(), true
	return tea.Batch(light, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		du, err := cli.DiskUsage(ctx)
		if err != nil {
			return dfMsg{}
		}
		return dfMsg{du}
	})
}

func (m *Model) flash(s string, isErr bool) {
	m.toast, m.toastErr, m.toastUntil = s, isErr, time.Now().Add(4*time.Second)
}

// ---------------------------------------------------------------- events & state

func (m *Model) onEvent(e docker.Event) {
	// exec_* fire several times a second from healthchecks — pure noise here.
	if strings.HasPrefix(e.Action, "exec_") {
		return
	}
	m.events = append(m.events, e)
	if len(m.events) > 1000 {
		m.events = m.events[len(m.events)-1000:]
	}
	if e.Type != "container" {
		if e.Type == "image" || e.Type == "volume" || e.Type == "network" {
			m.resAt = time.Time{}
		}
		return
	}
	name := e.Name()
	id := e.Actor.ID
	switch {
	case e.Action == "kill" || e.Action == "stop" || e.Action == "restart":
		// docker stop/restart/kill (from berth, the CLI or compose) sends a
		// kill event before the container dies — that exit is intentional.
		m.stopReq[id] = time.Now()
	case e.Action == "die" && e.Actor.Attributes["exitCode"] != "0":
		if t, ok := m.stopReq[id]; ok && time.Since(t) < time.Minute {
			delete(m.stopReq, id)
			break
		}
		m.crashes[id] = append(m.crashes[id], time.Now())
		m.raise("crash", id, name, fmt.Sprintf("crashed (exit %s)", e.Actor.Attributes["exitCode"]))
	case e.Action == "oom":
		m.raise("oom", id, name, "killed: out of memory")
	case strings.HasPrefix(e.Action, "health_status: unhealthy"):
		m.raise("unhealthy", id, name, "became unhealthy")
	}
	if m.lastSel == e.Actor.ID {
		m.inspAt = time.Time{} // refresh detail on the next 1s tick (coalesced)
	}
}

// recentCrashes counts unexpected exits in the last 15 minutes; the badge
// fades on its own once a container stays up.
func (m Model) recentCrashes(id string) int {
	n := 0
	for _, t := range m.crashes[id] {
		if time.Since(t) < 15*time.Minute {
			n++
		}
	}
	return n
}

func (m *Model) raise(kind, id, name, text string) {
	m.alerts = append(m.alerts, alert{at: time.Now(), text: text, kind: kind, id: id, name: name})
	if len(m.alerts) > 100 {
		m.alerts = m.alerts[1:]
	}
	m.flash("▲ "+name+" "+text, true)
}

func (m *Model) recentAlerts() int {
	n := 0
	for _, a := range m.alerts {
		if time.Since(a.at) < 15*time.Minute {
			n++
		}
	}
	return n
}

func stateOf(c *docker.Container) string {
	if c.State == "running" {
		if h := c.Health(); h != "" {
			return h
		}
	}
	return c.State
}

func (m *Model) recordStates() {
	now := m.snap.At
	for i := range m.snap.Containers {
		c := &m.snap.Containers[i]
		st := stateOf(c)
		tl := m.timeline[c.ID]
		if len(tl) == 0 || tl[len(tl)-1].state != st {
			tl = append(tl, span{now, st})
			if len(tl) > 500 {
				tl = tl[1:]
			}
			m.timeline[c.ID] = tl
		}
	}
}

// ---------------------------------------------------------------- list model

func (m *Model) move(d int) {
	if m.view != VContainers {
		n := m.resCount()
		if n == 0 {
			return
		}
		m.rcur[m.view] = clamp(m.rcur[m.view]+d, 0, n-1)
		m.clampScroll()
		return
	}
	if len(m.rows) == 0 {
		return
	}
	i := m.cursor
	step := 1
	if d < 0 {
		step = -1
	}
	for n := 0; n < abs(d); n++ {
		j := i + step
		for j >= 0 && j < len(m.rows) && m.rows[j].c == nil {
			j += step
		}
		if j < 0 || j >= len(m.rows) {
			break
		}
		i = j
	}
	m.cursor = i
	if c := m.rows[i].c; c != nil {
		m.selID = c.ID
	}
	m.clampScroll()
}

func (m *Model) clampScroll() {
	vis := m.bodyHeight() - 2
	if vis < 1 {
		return
	}
	if m.view != VContainers {
		v := m.view
		if m.rcur[v] < m.roff[v] {
			m.roff[v] = m.rcur[v]
		}
		if m.rcur[v] >= m.roff[v]+vis {
			m.roff[v] = m.rcur[v] - vis + 1
		}
		return
	}
	if m.cursor < m.offset+1 {
		m.offset = m.cursor - 1
	}
	if m.cursor >= m.offset+vis {
		m.offset = m.cursor - vis + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *Model) rebuildRows() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	groups := map[string][]*docker.Container{}
	for i := range m.snap.Containers {
		c := &m.snap.Containers[i]
		if m.hideStopped && c.State != "running" && c.State != "paused" && c.State != "restarting" {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(c.Name()+" "+c.Image+" "+c.Project()), q) {
			continue
		}
		groups[c.Project()] = append(groups[c.Project()], c)
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		if g != "" {
			names = append(names, g)
		}
	}
	sort.Strings(names)
	if _, ok := groups[""]; ok {
		names = append(names, "")
	}
	rank := func(c *docker.Container) int {
		if c.State == "running" || c.State == "restarting" || c.State == "paused" {
			return 0
		}
		return 1
	}
	m.rows = m.rows[:0]
	for _, g := range names {
		cs := groups[g]
		sort.SliceStable(cs, func(i, j int) bool {
			a, b := cs[i], cs[j]
			if rank(a) != rank(b) {
				return rank(a) < rank(b)
			}
			switch m.sortMode {
			case 1:
				if x, y := m.smp.Current(a.ID).CPU, m.smp.Current(b.ID).CPU; x != y {
					return x > y
				}
			case 2:
				if x, y := m.smp.Current(a.ID).MemUsed, m.smp.Current(b.ID).MemUsed; x != y {
					return x > y
				}
			}
			return strings.ToLower(a.Name()) < strings.ToLower(b.Name())
		})
		label := g
		if label == "" {
			label = "standalone"
		}
		m.rows = append(m.rows, row{group: label, count: len(cs)})
		for _, c := range cs {
			m.rows = append(m.rows, row{c: c})
		}
	}
	m.cursor = -1
	for i, r := range m.rows {
		if r.c != nil && r.c.ID == m.selID {
			m.cursor = i
		}
	}
	if m.cursor < 0 {
		m.cursor = 0
		for i, r := range m.rows {
			if r.c != nil {
				m.cursor = i
				m.selID = r.c.ID
				break
			}
		}
	}
	m.clampScroll()
}

func (m *Model) retarget() {
	live := map[string]bool{}
	for _, c := range m.snap.Containers {
		live[c.ID] = true
		a := m.anims[c.ID]
		if a == nil {
			a = &anim{}
			m.anims[c.ID] = a
		}
		cur := m.smp.Current(c.ID)
		a.cpu.Set(cur.CPU)
		if c.ID != m.selID { // list rows jump straight to the new value
			a.cpu.Cur = a.cpu.Target
		}
		a.mem.Set(float64(cur.MemUsed))
		if c.ID != m.selID {
			a.mem.Cur = a.mem.Target
		}
	}
	for id := range m.anims {
		if !live[id] {
			delete(m.anims, id)
		}
	}
	if h := m.smp.HostHistory(metrics.CPU, 1); len(h) == 1 {
		m.hostCPU.Set(h[0])
	}
	if h := m.smp.HostHistory(metrics.Mem, 1); len(h) == 1 {
		m.hostMem.Set(h[0])
	}
}

func (m Model) selected() *docker.Container {
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		return m.rows[m.cursor].c
	}
	return nil
}

func (m Model) containerByID(id string) *docker.Container {
	for i := range m.snap.Containers {
		if m.snap.Containers[i].ID == id {
			return &m.snap.Containers[i]
		}
	}
	return nil
}
