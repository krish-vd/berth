package ui

import (
	"strings"
	"text/template"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/krish-vd/berth/internal/config"
	"github.com/krish-vd/berth/internal/docker"
)

// customItems builds the custom-commands menu; the config is re-read each
// time so edits apply without restarting berth.
func (m Model) customItems() []item {
	c := m.selected()
	cfg, err := config.Load()
	if err != nil {
		return []item{{label: "Config error: " + err.Error(), hint: config.Path(), run: func(*Model) tea.Cmd { return nil }}}
	}
	if len(cfg.CustomCommands) == 0 {
		return []item{{label: "No custom commands yet — create an example config", hint: config.Path(), run: func(m *Model) tea.Cmd {
			p, err := config.WriteExample()
			if err != nil {
				m.flash("could not write config: "+err.Error(), true)
			} else {
				m.flash("created "+p+" — press c again", false)
			}
			return nil
		}}}
	}
	if c == nil {
		return []item{{label: "Select a container first", run: func(*Model) tea.Cmd { return nil }}}
	}
	var its []item
	for _, cc := range cfg.CustomCommands {
		cc := cc
		cmdText, err := render(cc.Command, c)
		hint := cmdText
		if err != nil {
			hint = "template error: " + err.Error()
		}
		its = append(its, item{label: cc.Name, hint: hint, run: func(m *Model) tea.Cmd {
			if err != nil {
				m.flash(cc.Name+": "+err.Error(), true)
				return nil
			}
			return m.runInTerminal(cc.Name, cmdText, cc.ShouldPause())
		}})
	}
	return its
}

func render(tmpl string, c *docker.Container) (string, error) {
	t, err := template.New("cmd").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	err = t.Execute(&b, map[string]string{
		"ID": c.ID, "Name": c.Name(), "Project": c.Project(),
		"Service": c.Labels["com.docker.compose.service"], "Image": c.Image,
	})
	return b.String(), err
}
