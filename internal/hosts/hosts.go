// Package hosts finds the Docker environments berth can manage — this
// machine, hosts saved in the config file and the docker CLI's contexts —
// and probes each one for health without switching to it.
package hosts

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/krish-vd/berth/internal/config"
	"github.com/krish-vd/berth/internal/docker"
)

const (
	SourceLocal   = "this machine"
	SourceConfig  = "saved"
	SourceContext = "docker context"
)

// Host is one Docker environment.
type Host struct {
	Name   string
	URL    string
	Source string
}

// Status is the result of one probe.
type Status struct {
	Probed    bool
	OK        bool
	Err       string
	Info      docker.Info
	Running   int
	Stopped   int
	Unhealthy int
	Projects  int
	Latency   time.Duration
	At        time.Time
}

// Discover lists every environment, current first, without duplicates.
func Discover(current string) []Host {
	current = docker.Resolve(current)
	out := []Host{{Name: localName(current), URL: current, Source: SourceLocal}}
	seen := map[string]bool{key(current): true}
	add := func(h Host) {
		if h.URL == "" || seen[key(h.URL)] {
			return
		}
		seen[key(h.URL)] = true
		out = append(out, h)
	}
	if c, err := config.Load(); err == nil {
		for _, h := range c.Hosts {
			add(Host{Name: h.Name, URL: h.URL, Source: SourceConfig})
		}
	}
	for _, h := range contexts() {
		add(h)
	}
	if out[0].Source == SourceLocal && !isLocal(current) { // started with --host pointing elsewhere
		for i := 1; i < len(out); i++ {
			if key(out[i].URL) == key(current) {
				out[0].Name = out[i].Name
			}
		}
	}
	return out
}

func localName(u string) string {
	if isLocal(u) {
		return "This machine"
	}
	if c, err := config.Load(); err == nil {
		for _, h := range c.Hosts {
			if key(h.URL) == key(u) {
				return h.Name
			}
		}
	}
	return strings.TrimPrefix(strings.TrimPrefix(u, "ssh://"), "tcp://")
}

func isLocal(u string) bool {
	return strings.HasPrefix(u, "unix://") || strings.HasPrefix(u, "wsl") || strings.HasPrefix(u, "npipe://")
}

func key(u string) string { return strings.TrimRight(strings.ToLower(u), "/") }

// contexts reads `docker context ls`. Endpoints that only mean something on
// the machine running the docker CLI (local sockets) are skipped; on Windows
// the CLI runs inside WSL, so those are what berth already reaches via wsl://.
func contexts() []Host {
	argv := []string{"docker", "context", "ls", "--format", "{{json .}}"}
	if runtime.GOOS == "windows" {
		argv = append([]string{"wsl.exe", "-e"}, argv...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	docker.HideWindow(cmd)
	b, err := cmd.Output()
	if err != nil {
		return nil
	}
	var out []Host
	for _, ln := range strings.Split(string(b), "\n") {
		var c struct{ Name, DockerEndpoint string }
		if json.Unmarshal([]byte(strings.TrimSpace(ln)), &c) != nil || c.DockerEndpoint == "" {
			continue
		}
		if strings.HasPrefix(c.DockerEndpoint, "unix://") || strings.HasPrefix(c.DockerEndpoint, "npipe://") {
			continue
		}
		out = append(out, Host{Name: c.Name, URL: c.DockerEndpoint, Source: SourceContext})
	}
	return out
}

// Probe connects, reads engine info and the container list, and measures the
// round trip. It never blocks longer than the timeout.
func Probe(h Host, timeout time.Duration) Status {
	st := Status{Probed: true, At: time.Now()}
	cli, err := docker.New(h.URL)
	if err != nil {
		st.Err = err.Error()
		return st
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	var (
		wg   sync.WaitGroup
		info docker.Info
		cs   []docker.Container
		ierr error
		cerr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); info, ierr = cli.Info(ctx) }()
	go func() { defer wg.Done(); cs, cerr = cli.Containers(ctx) }()
	wg.Wait()
	st.Latency = time.Since(start)
	if ierr == nil {
		ierr = cerr
	}
	if ierr != nil {
		st.Err = cleanErr(ierr, ctx)
		return st
	}
	st.OK, st.Info = true, info
	projects := map[string]bool{}
	for _, c := range cs {
		switch {
		case c.Health() == "unhealthy":
			st.Unhealthy++
		case c.State == "running":
			st.Running++
		default:
			st.Stopped++
		}
		if p := c.Project(); p != "" {
			projects[p] = true
		}
	}
	st.Projects = len(projects)
	return st
}

func cleanErr(err error, ctx context.Context) string {
	if ctx.Err() != nil {
		return "no answer (timed out)"
	}
	s := err.Error()
	for _, cut := range []string{"Get \"http://docker/", "Get \"http://docker"} {
		if i := strings.Index(s, cut); i >= 0 {
			if j := strings.Index(s[i:], ": "); j >= 0 {
				s = s[i+j+2:]
			}
		}
	}
	if strings.Contains(s, "Permission denied") {
		return "ssh refused the key: add your public key to the host"
	}
	if strings.Contains(s, "Host key verification failed") {
		return "unknown host key: run `ssh <host>` once to trust it"
	}
	return s
}
