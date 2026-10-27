package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/krish-vd/berth/internal/docker"
)

// composeTarget is everything needed to address a compose project the same
// way `docker compose` did when it created the container (read from labels).
type composeTarget struct {
	project, service, workdir string
	files                     []string
}

func composeOf(c *docker.Container) (composeTarget, bool) {
	p := c.Labels["com.docker.compose.project"]
	if p == "" {
		return composeTarget{}, false
	}
	t := composeTarget{project: p, service: c.Labels["com.docker.compose.service"], workdir: c.Labels["com.docker.compose.project.working_dir"]}
	for _, f := range strings.Split(c.Labels["com.docker.compose.project.config_files"], ",") {
		if f = strings.TrimSpace(f); f != "" {
			t.files = append(t.files, f)
		}
	}
	return t, true
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }

func (t composeTarget) base() string {
	parts := []string{"docker", "compose", "-p", shq(t.project)}
	if t.workdir != "" {
		parts = append(parts, "--project-directory", shq(t.workdir))
	}
	for _, f := range t.files {
		parts = append(parts, "-f", shq(f))
	}
	return strings.Join(parts, " ")
}

// runInTerminal hands the terminal to a shell script on the engine host and
// waits for Enter afterwards so the output can be read. Streaming commands
// (logs -f) skip the pause: Ctrl+C returns straight to berth.
func (m *Model) runInTerminal(title, script string, pause bool) tea.Cmd {
	full := fmt.Sprintf(`printf '\n\033[1;34m◆ berth\033[0m · %%s\n\n' %s; %s`, shq(title), script)
	if pause {
		full += `; s=$?; echo; if [ $s -eq 0 ]; then printf '\033[32m✓ done\033[0m'; else printf '\033[31m✗ exited with %d\033[0m' $s; fi; printf ' — press Enter to return to berth '; read _`
	}
	return tea.ExecProcess(m.cli.Shell(full), func(error) tea.Msg { return execMsg{} })
}

func (m *Model) compose(t composeTarget, args string, pause bool) tea.Cmd {
	m.resAt = m.resAt.AddDate(-1, 0, 0) // refresh images/networks afterwards
	return m.runInTerminal("docker compose "+args, t.base()+" "+args, pause)
}

func (m Model) composeItems() []item {
	c := m.selected()
	if c == nil {
		return nil
	}
	t, ok := composeOf(c)
	if !ok {
		return []item{{label: c.Name() + " is not part of a compose project", run: func(*Model) tea.Cmd { return nil }}}
	}
	p, s := t.project, t.service
	run := func(args string, pause bool) func(*Model) tea.Cmd {
		return func(m *Model) tea.Cmd { return m.compose(t, args, pause) }
	}
	confirm := func(title string, body []string, args string) func(*Model) tea.Cmd {
		return func(m *Model) tea.Cmd {
			m.ask(title, body, true, func(m *Model) tea.Cmd { return m.compose(t, args, true) })
			return nil
		}
	}
	its := []item{
		{key: "u", label: "Up — project " + p, hint: "create & start all services", run: run("up -d", true)},
		{key: "U", label: "Up with rebuild — project " + p, hint: "build images, then up", run: run("up -d --build", true)},
		{key: "D", label: "Down — project " + p, hint: "stop & remove containers, networks", danger: true,
			run: confirm("docker compose down — "+p+"?", []string{"Stops and removes every container and network of this project.", "Volumes and images are kept."}, "down")},
		{label: "Down + delete volumes — project " + p, hint: "removes data!", danger: true,
			run: confirm("docker compose down -v — "+p+"?", []string{"Stops and removes containers and networks,", "AND DELETES THE PROJECT'S VOLUMES (database data etc).", "", "This cannot be undone."}, "down -v")},
		{key: "R", label: "Restart project " + p, run: run("restart", true)},
		{key: "S", label: "Stop project " + p, danger: true, run: run("stop", true)},
		{key: "s", label: "Start project " + p, run: run("start", true)},
		{key: "p", label: "Pull images — project " + p, hint: "fetch newer images", run: run("pull", true)},
		{key: "b", label: "Build images — project " + p, run: run("build", true)},
		{key: "l", label: "Logs — all services of " + p, hint: "merged, follow · ctrl+c returns", run: run("logs -f --tail 200", false)},
		{label: "Show ps — project " + p, run: run("ps -a", true)},
		{label: "Show resolved config — project " + p, hint: "docker compose config", run: run("config", true)},
	}
	if s != "" {
		its = append(its,
			item{key: "r", label: "Recreate service " + s, hint: "up --force-recreate --no-deps", run: run("up -d --force-recreate --no-deps "+shq(s), true)},
			item{key: "B", label: "Rebuild & restart service " + s, hint: "up --build --no-deps", run: run("up -d --build --no-deps "+shq(s), true)},
			item{key: "P", label: "Pull image — service " + s, run: run("pull "+shq(s), true)},
			item{key: "L", label: "Logs — service " + s, hint: "follow · ctrl+c returns", run: run("logs -f --tail 200 "+shq(s), false)},
		)
	}
	return its
}
