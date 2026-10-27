package ui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type rawMsg struct {
	key  string
	text string
	err  error
}

// rawKey identifies what the raw JSON view should currently show.
func (m Model) rawKey() (kind, id string) {
	switch m.view {
	case VContainers:
		if c := m.selected(); c != nil && m.tab == TInspect {
			return "container", c.ID
		}
	case VImages:
		if im := m.selImage(); im != nil && m.rawRes {
			return "image", im.ID
		}
	case VVolumes:
		if v := m.selVolume(); v != nil && m.rawRes {
			return "volume", v.Name
		}
	case VNetworks:
		if n := m.selNetwork(); n != nil && m.rawRes {
			return "network", n.ID
		}
	}
	return "", ""
}

func (m *Model) fetchRaw(kind, id string) tea.Cmd {
	cli := m.cli
	key := kind + ":" + id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b, err := cli.InspectRaw(ctx, kind, id)
		return rawMsg{key: key, text: string(b), err: err}
	}
}

var (
	jsonKey   = regexp.MustCompile(`^(\s*)"((?:[^"\\]|\\.)*)":\s?(.*)$`)
	jsonEnvKV = regexp.MustCompile(`^"([A-Za-z0-9_.]+)=(.*)"(,?)$`)
)

// viewRaw renders pretty JSON with syntax colours, masking secrets in
// environment entries unless revealed.
func (m Model) viewRaw(kind, id string, iw, ih int) []string {
	if m.rawText == "" || m.rawFor != kind+":"+id {
		return []string{"", sMuted.Render("  loading " + kind + " JSON…")}
	}
	src := strings.Split(m.rawText, "\n")
	head := " " + sBold.Render(fmt.Sprintf("%s inspect", kind)) + sMuted.Render(fmt.Sprintf("  ·  %d lines  ·  ↑↓ / wheel / drag the bar to scroll", len(src)))
	body := make([]string, 0, len(src))
	for _, l := range src {
		body = append(body, m.colorJSON(l))
	}
	return append([]string{head, ""}, m.scroll(body, m.dScroll, iw, ih-2, 2)...)
}

func (m Model) colorJSON(l string) string {
	if mm := jsonKey.FindStringSubmatch(l); mm != nil {
		return mm[1] + fg(cCyan).Render(`"`+mm[2]+`"`) + sSubtle.Render(": ") + m.colorJSONValue(mm[3])
	}
	indent := len(l) - len(strings.TrimLeft(l, " "))
	return l[:indent] + m.colorJSONValue(strings.TrimLeft(l, " "))
}

func (m Model) colorJSONValue(v string) string {
	switch {
	case v == "" || strings.Trim(v, "{}[],") == "":
		return sSubtle.Render(v)
	case strings.HasPrefix(v, `"`):
		if kv := jsonEnvKV.FindStringSubmatch(v); kv != nil && !m.revealEnv {
			val := kv[2]
			if isSecret(kv[1]) && val != "" {
				val = "••••••••"
			} else if urlCreds.MatchString(val) {
				val = urlCreds.ReplaceAllString(val, "${1}••••${3}")
			}
			return fg(cGreen).Render(`"`+kv[1]+"="+val+`"`) + sSubtle.Render(kv[3])
		}
		return fg(cGreen).Render(v)
	case v == "true" || v == "false" || v == "true," || v == "false,":
		return fg(cAccent2).Render(v)
	case strings.HasPrefix(v, "null"):
		return sMuted.Render(v)
	}
	return fg(cYellow).Render(v)
}
