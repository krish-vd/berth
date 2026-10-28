package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/krish-vd/berth/internal/docker"
)

// ---------------------------------------------------------------- data access

func (m Model) sortedImages() []docker.Image {
	is := append([]docker.Image(nil), m.images...)
	sort.Slice(is, func(i, j int) bool { return is[i].Created > is[j].Created })
	return is
}

// sortedVolumes lists volumes instantly from /volumes, enriched with sizes
// from /system/df once that (slow) call has landed.
func (m Model) sortedVolumes() []docker.Volume {
	sizes := map[string]*docker.Volume{}
	if m.du != nil {
		for i := range m.du.Volumes {
			sizes[m.du.Volumes[i].Name] = &m.du.Volumes[i]
		}
	}
	vs := append([]docker.Volume(nil), m.volumes...)
	for i := range vs {
		if d := sizes[vs[i].Name]; d != nil {
			vs[i].UsageData = d.UsageData
		}
	}
	sort.SliceStable(vs, func(i, j int) bool {
		if a, b := volSize(vs[i]), volSize(vs[j]); a != b {
			return a > b
		}
		return vs[i].Name < vs[j].Name
	})
	return vs
}

func (m Model) sortedNetworks() []docker.Network {
	ns := append([]docker.Network(nil), m.networks...)
	sort.Slice(ns, func(i, j int) bool { return ns[i].Name < ns[j].Name })
	return ns
}

func volSize(v docker.Volume) int64 {
	if v.UsageData == nil || v.UsageData.Size < 0 {
		return 0
	}
	return v.UsageData.Size
}

func (m Model) resCount() int {
	switch m.view {
	case VHosts:
		return len(m.hosts.list)
	case VDoctor:
		if m.doc.file != nil {
			return len(m.doc.file.Checks)
		}
	case VImages:
		return len(m.images)
	case VVolumes:
		return len(m.volumes)
	case VNetworks:
		return len(m.networks)
	}
	return 0
}

func (m Model) selImage() *docker.Image {
	is := m.sortedImages()
	if i := m.rcur[VImages]; i < len(is) {
		return &is[i]
	}
	return nil
}

func (m Model) selVolume() *docker.Volume {
	vs := m.sortedVolumes()
	if i := m.rcur[VVolumes]; i < len(vs) {
		return &vs[i]
	}
	return nil
}

func (m Model) selNetwork() *docker.Network {
	ns := m.sortedNetworks()
	if i := m.rcur[VNetworks]; i < len(ns) {
		return &ns[i]
	}
	return nil
}

func (m Model) imageUsers(id string) []string {
	var out []string
	for _, c := range m.snap.Containers {
		if c.ImageID == id {
			out = append(out, c.Name())
		}
	}
	sort.Strings(out)
	return out
}

type volUse struct{ name, dest string }

func (m Model) volumeUsers(name string) []volUse {
	var out []volUse
	for _, c := range m.snap.Containers {
		for _, mt := range c.Mounts {
			if mt.Type == "volume" && mt.Name == name {
				out = append(out, volUse{c.Name(), mt.Destination})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

type netUse struct{ name, ip string }

func (m Model) networkUsers(name string) []netUse {
	var out []netUse
	for _, c := range m.snap.Containers {
		if n, ok := c.NetworkSettings.Networks[name]; ok {
			out = append(out, netUse{c.Name(), n.IPAddress})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func isAnon(name string) bool {
	if len(name) != 64 {
		return false
	}
	for _, r := range name {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- list panel

func (m Model) viewResList(w, h int) string {
	iw := w - 2
	vis := h - 2
	focused := m.focus == 0
	cur := m.rcur[m.view]
	var rows []string
	var title, right string
	tail := 18

	switch m.view {
	case VImages:
		is := m.sortedImages()
		var total int64
		for _, im := range is {
			total += im.Size
			used := len(m.imageUsers(im.ID)) > 0
			dot := fg(cMuted).Render("○")
			if used {
				dot = fg(cGreen).Render("●")
			}
			tag := im.Tag()
			ts := sText
			if strings.Contains(tag, "<none>") {
				ts = fg(cYellow)
			}
			name := fit(ts.Render(ansi.Truncate(tag, iw-tail-3, "…")), iw-tail-3)
			rows = append(rows, dot+" "+name+" "+sText.Render(fmt.Sprintf("%7s", HumanBytes(float64(im.Size))))+sMuted.Render(fmt.Sprintf("%10s", ago(time.Unix(im.Created, 0))+" ago")))
		}
		title = sBold.Render("Images")
		right = sMuted.Render(fmt.Sprintf("%d · %s", len(is), HumanBytes(float64(total))))
	case VVolumes:
		vs := m.sortedVolumes()
		var total int64
		for _, v := range vs {
			total += volSize(v)
			used := len(m.volumeUsers(v.Name)) > 0
			dot := fg(cMuted).Render("○")
			if used {
				dot = fg(cGreen).Render("●")
			}
			label := sText.Render(v.Name)
			if isAnon(v.Name) {
				label = sMuted.Render(v.Name[:12]) + sSubtle.Render(" anonymous")
			}
			name := fit(ansi.Truncate(label, iw-tail-3, "…"), iw-tail-3)
			size := sText.Render(fmt.Sprintf("%8s", HumanBytes(float64(volSize(v)))))
			if v.UsageData == nil {
				size = sSubtle.Render(fmt.Sprintf("%8s", string(spinner[m.frame%len(spinner)])))
			}
			rows = append(rows, dot+" "+name+" "+size+sMuted.Render(fmt.Sprintf("%9s", v.Driver)))
		}
		title = sBold.Render("Volumes")
		right = sMuted.Render(fmt.Sprintf("%d · %s", len(vs), HumanBytes(float64(total))))
		if m.du == nil {
			right = sMuted.Render(fmt.Sprintf("%d · measuring sizes…", len(vs)))
		}
	case VNetworks:
		ns := m.sortedNetworks()
		for _, n := range ns {
			users := len(m.networkUsers(n.Name))
			dot := fg(cMuted).Render("○")
			if users > 0 {
				dot = fg(cGreen).Render("●")
			}
			name := fit(sText.Render(ansi.Truncate(n.Name, iw-tail-3, "…")), iw-tail-3)
			rows = append(rows, dot+" "+name+" "+fg(cCyan).Render(fmt.Sprintf("%-8s", n.Driver))+sMuted.Render(fmt.Sprintf("%4d ctr", users))+" ")
		}
		title = sBold.Render("Networks")
		right = sMuted.Render(fmt.Sprint(len(ns)))
	}

	off := m.roff[m.view]
	var lines []string
	for i := off; i < len(rows) && len(lines) < vis; i++ {
		if i == cur {
			lines = append(lines, m.selRow(rows[i], iw, focused))
		} else {
			lines = append(lines, " "+rows[i])
		}
	}
	return panel(title, right, lines, w, h, focused)
}

// ---------------------------------------------------------------- detail panel

func (m Model) viewResDetail(w, h int) string {
	iw, ih := w-2, h-2
	var l []string
	sec := func(t string) {
		if len(l) > 0 {
			l = append(l, "")
		}
		l = append(l, " "+fg(cAccent2).Bold(true).Render(t)+" "+sSubtle.Render(strings.Repeat("─", max(0, iw-lipgloss.Width(t)-3))))
	}
	kv := func(k, v string) {
		if v == "" {
			v = sSubtle.Render("—")
		} else {
			v = sText.Render(v)
		}
		l = append(l, "   "+sMuted.Render(fmt.Sprintf("%-12s", k))+v)
	}
	title := sBold.Render("—")

	switch m.view {
	case VImages:
		im := m.selImage()
		if im == nil {
			break
		}
		title = sBold.Render(ansi.Truncate(im.Tag(), 50, "…"))
		sec("Image")
		kv("id", im.ShortID())
		kv("size", HumanBytes(float64(im.Size)))
		kv("created", time.Unix(im.Created, 0).Local().Format("2006-01-02 15:04")+"  ("+ago(time.Unix(im.Created, 0))+" ago)")
		if len(im.RepoTags) > 1 {
			kv("tags", strings.Join(im.RepoTags, "  "))
		}
		users := m.imageUsers(im.ID)
		sec(fmt.Sprintf("Used by %d container%s", len(users), plural(len(users))))
		if len(users) == 0 {
			l = append(l, "   "+sMuted.Render("unused — safe to remove"))
		}
		for _, u := range users {
			l = append(l, "   "+fg(cGreen).Render("●")+" "+sText.Render(u))
		}
		layers, ok := m.layers[im.ID]
		sec("Layers")
		if !ok || layers == nil {
			l = append(l, "   "+sMuted.Render("loading…"))
			break
		}
		var maxL int64
		for _, ly := range layers {
			maxL = max(maxL, ly.Size)
		}
		barW := 14
		for _, ly := range layers {
			cmd := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(ly.CreatedBy, "/bin/sh -c #(nop) "), "/bin/sh -c "))
			cmd = strings.Join(strings.Fields(cmd), " ")
			t := 0.0
			if maxL > 0 {
				t = float64(ly.Size) / float64(maxL)
			}
			size := sMuted.Render(fmt.Sprintf("%8s", HumanBytes(float64(ly.Size))))
			if ly.Size == 0 {
				size = sSubtle.Render(fmt.Sprintf("%8s", "0"))
			}
			l = append(l, "   "+size+" "+Bar(t, barW)+" "+sText.Render(ansi.Truncate(cmd, max(10, iw-barW-16), "…")))
		}
	case VVolumes:
		v := m.selVolume()
		if v == nil {
			break
		}
		title = sBold.Render(ansi.Truncate(v.Name, 50, "…"))
		sec("Volume")
		kv("name", v.Name)
		kv("driver", v.Driver)
		kv("scope", v.Scope)
		kv("size", HumanBytes(float64(volSize(*v))))
		kv("mountpoint", v.Mountpoint)
		if t, err := time.Parse(time.RFC3339, v.CreatedAt); err == nil {
			kv("created", t.Local().Format("2006-01-02 15:04")+"  ("+ago(t)+" ago)")
		}
		users := m.volumeUsers(v.Name)
		sec(fmt.Sprintf("Mounted by %d container%s", len(users), plural(len(users))))
		if len(users) == 0 {
			l = append(l, "   "+sMuted.Render("not mounted by any container"))
		}
		for _, u := range users {
			l = append(l, "   "+fg(cGreen).Render("●")+" "+sText.Render(fmt.Sprintf("%-28s", u.name))+sMuted.Render(" → ")+sText.Render(u.dest))
		}
		if len(v.Labels) > 0 {
			sec("Labels")
			for _, k := range sortedKeys(v.Labels) {
				l = append(l, "   "+sMuted.Render(k)+sSubtle.Render(" = ")+sText.Render(v.Labels[k]))
			}
		}
	case VNetworks:
		n := m.selNetwork()
		if n == nil {
			break
		}
		title = sBold.Render(n.Name)
		sec("Network")
		kv("id", n.ID[:min(12, len(n.ID))])
		kv("driver", n.Driver)
		kv("scope", n.Scope)
		if n.Internal {
			kv("internal", "yes")
		}
		for _, c := range n.IPAM.Config {
			kv("subnet", c.Subnet)
			kv("gateway", c.Gateway)
		}
		users := m.networkUsers(n.Name)
		sec(fmt.Sprintf("%d connected container%s", len(users), plural(len(users))))
		for _, u := range users {
			l = append(l, "   "+fg(cGreen).Render("●")+" "+sText.Render(fmt.Sprintf("%-28s", u.name))+fg(cCyan).Render(u.ip))
		}
		if len(n.Labels) > 0 {
			sec("Labels")
			for _, k := range sortedKeys(n.Labels) {
				l = append(l, "   "+sMuted.Render(k)+sSubtle.Render(" = ")+sText.Render(n.Labels[k]))
			}
		}
	}
	if kind, id := m.rawKey(); kind != "" {
		return panel(title+"  "+sMuted.Render("raw JSON · J to close"), "", m.viewRaw(kind, id, iw, ih), w, h, m.focus == 1)
	}
	return panel(title, "", m.scroll(l, m.dScroll, iw, ih, 0), w, h, m.focus == 1)
}

func sortedKeys(mp map[string]string) []string {
	ks := make([]string, 0, len(mp))
	for k := range mp {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// ---------------------------------------------------------------- events

var actionColor = map[string]lipgloss.Color{
	"start": cGreen, "create": cTeal, "restart": cYellow, "stop": cYellow, "kill": cRed, "die": cRed, "oom": cRed,
	"destroy": cRed, "pause": cAccent2, "unpause": cAccent2, "pull": cCyan, "delete": cRed, "untag": cMuted, "tag": cCyan,
	"connect": cCyan, "disconnect": cMuted, "mount": cMuted, "unmount": cMuted,
}

func (m Model) viewEvents(w, h int) string {
	iw, ih := w-2, h-2
	var body []string
	for i := len(m.events) - 1; i >= 0; i-- {
		e := m.events[i]
		t := time.Unix(0, e.TimeNano)
		if e.TimeNano == 0 {
			t = time.Unix(e.Time, 0)
		}
		act := e.Action
		base, _, _ := strings.Cut(act, ":")
		col, ok := actionColor[base]
		if !ok {
			col = cMuted
		}
		if strings.Contains(act, "unhealthy") {
			col = cRed
		} else if strings.Contains(act, "healthy") {
			col = cGreen
		}
		if strings.HasPrefix(act, "exec_") {
			col = cSubtle
		}
		extra := ""
		if c := e.Actor.Attributes["exitCode"]; c != "" {
			extra = "exit " + c
		}
		if img := e.Actor.Attributes["image"]; img != "" && e.Type == "container" {
			extra = strings.TrimSpace(extra + "  " + img)
		}
		body = append(body, " "+sMuted.Render(t.Local().Format("15:04:05"))+"  "+
			fg(cAccent2).Render(fmt.Sprintf("%-9s", e.Type))+" "+
			fg(col).Bold(true).Render(fmt.Sprintf("%-22s", ansi.Truncate(act, 22, "…")))+" "+
			sText.Render(fmt.Sprintf("%-32s", ansi.Truncate(e.Name(), 32, "…")))+" "+sMuted.Render(extra))
	}
	head := " " + sBold.Render(fmt.Sprintf("%d events", len(m.events))) + sMuted.Render("  ·  newest first  ·  streaming live from the engine")
	lines := []string{head, ""}
	if len(body) == 0 {
		lines = append(lines, sMuted.Render("  waiting for events — start, stop or restart something"))
	}
	if n := len(m.alerts); n > 0 {
		lines[1] = " " + fg(cRed).Render(fmt.Sprintf("▲ %d alert%s this session — latest: %s %s · press ! for all", n, plural(n), m.alerts[n-1].name, m.alerts[n-1].text))
	}
	lines = append(lines, m.scroll(body, m.dScroll, iw, ih-2, 2)...)
	_ = iw
	return panel(sBold.Render("Events"), sMuted.Render("live"), lines, w, h, true)
}

// ---------------------------------------------------------------- actions

func (m *Model) resourceKey(ks string) tea.Cmd {
	switch ks {
	case "d":
		m.askRemoveResource()
	case "J":
		m.rawRes = !m.rawRes
		m.dScroll = 0
	case "P":
		m.askPrune(map[View]string{VImages: "images", VVolumes: "volumes", VNetworks: "networks"}[m.view])
	}
	return nil
}

func (m *Model) askRemoveResource() {
	cli := m.cli
	switch m.view {
	case VImages:
		im := m.selImage()
		if im == nil {
			return
		}
		id, tag := im.ID, im.Tag()
		body := []string{"ID    " + im.ShortID(), "Size  " + HumanBytes(float64(im.Size))}
		if u := m.imageUsers(id); len(u) > 0 {
			body = append(body, "", fmt.Sprintf("In use by %d container(s) — Docker will refuse unless they are removed.", len(u)))
		}
		m.ask("Remove image "+tag+"?", body, true, func(m *Model) tea.Cmd {
			return m.run("img:"+id, "remove "+tag, func(ctx context.Context) error { return cli.RemoveImage(ctx, id, false) })
		})
	case VVolumes:
		v := m.selVolume()
		if v == nil {
			return
		}
		name := v.Name
		m.ask("Remove volume?", []string{name, "", "All data in this volume is permanently deleted."}, true, func(m *Model) tea.Cmd {
			return m.run("vol:"+name, "remove volume", func(ctx context.Context) error { return cli.RemoveVolume(ctx, name) })
		})
	case VNetworks:
		n := m.selNetwork()
		if n == nil {
			return
		}
		id, name := n.ID, n.Name
		m.ask("Remove network "+name+"?", []string{"Driver  " + n.Driver}, true, func(m *Model) tea.Cmd {
			return m.run("net:"+id, "remove network "+name, func(ctx context.Context) error { return cli.RemoveNetwork(ctx, id) })
		})
	}
}

var pruneInfo = map[string]string{
	"containers": "Removes every stopped container.",
	"images":     "Removes dangling (untagged) images only.",
	"volumes":    "Removes anonymous volumes not used by any container. Named volumes are kept.",
	"networks":   "Removes custom networks not used by any container.",
}

func (m *Model) askPrune(kind string) {
	if kind == "" {
		return
	}
	cli := m.cli
	m.ask("Prune unused "+kind+"?", []string{pruneInfo[kind]}, true, func(m *Model) tea.Cmd {
		return m.run("prune:"+kind, "prune "+kind, func(ctx context.Context) error {
			n, err := cli.Prune(ctx, kind)
			if err == nil && n > 0 {
				return nil
			}
			return err
		})
	})
}
