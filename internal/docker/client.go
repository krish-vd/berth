// Package docker is a thin, dependency-free client for the Docker Engine API.
//
// It supports unix sockets, tcp, ssh (via `docker system dial-stdio`) and a
// "wsl" transport that lets a native Windows binary talk to a Docker daemon
// running inside WSL without exposing any port.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Client talks to one Docker Engine.
type Client struct {
	http   *http.Client
	base   string
	Host   string // human-readable endpoint, e.g. "wsl://Ubuntu"
	Kind   string // unix | tcp | ssh | wsl
	execv  []string
	shellv []string

	// Parallel is how many stats requests the sampler may run at once.
	Parallel int
}

// New resolves host (flag value, may be empty) and returns a client.
// Resolution order: explicit host > $DOCKER_HOST > platform default.
func New(host string) (*Client, error) {
	if host == "" {
		host = os.Getenv("DOCKER_HOST")
	}
	if host == "" {
		if runtime.GOOS == "windows" {
			host = "wsl://"
		} else {
			host = "unix:///var/run/docker.sock"
		}
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("invalid docker host %q: %w", host, err)
	}

	c := &Client{Host: host, Kind: u.Scheme, base: "http://docker"}
	var dial func(ctx context.Context) (net.Conn, error)

	switch u.Scheme {
	case "unix":
		path := u.Path
		dial = func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}
	case "tcp", "http":
		addr := u.Host
		dial = func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		}
	case "ssh":
		target := u.Host
		if u.User != nil {
			target = u.User.String() + "@" + u.Host
		}
		c.execv = []string{"ssh", target, "docker"}
		c.shellv = []string{"ssh", "-t", target}
		dial = cmdDialer("ssh", "-o", "ConnectTimeout=10", "--", target, "docker", "system", "dial-stdio")
	case "wsl":
		args := []string{}
		if d := strings.Trim(u.Host+u.Path, "/"); d != "" {
			args = append(args, "-d", d)
			c.Host = "wsl://" + d
		} else {
			c.Host = "wsl"
		}
		c.execv = append(append([]string{"wsl.exe"}, args...), "-e", "docker")
		c.shellv = append(append([]string{"wsl.exe"}, args...), "-e", "sh", "-c")
		dial = cmdDialer("wsl.exe", append(args, "-e", "docker", "system", "dial-stdio")...)
	default:
		return nil, fmt.Errorf("unsupported docker host scheme %q", u.Scheme)
	}
	if c.execv == nil {
		c.execv = []string{"docker"}
		c.shellv = []string{"sh", "-c"}
	}

	// Every connection over ssh/wsl is a helper process, so keep the pool
	// small there: 3 parallel stat calls + events + logs + one for detail
	// fetches. Local sockets are free, so allow more parallelism.
	var conns int
	conns, c.Parallel = 32, 12
	if c.Kind == "ssh" || c.Kind == "wsl" {
		conns, c.Parallel = 6, 3
	}
	c.http = &http.Client{Transport: &http.Transport{
		DialContext:         func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		MaxIdleConns:        conns,
		MaxIdleConnsPerHost: conns,
		MaxConnsPerHost:     conns,
		IdleConnTimeout:     90 * time.Second,
	}}
	return c, nil
}

// DockerCLI returns the argv prefix that runs the docker CLI against this
// engine (used for interactive exec).
func (c *Client) DockerCLI() []string { return append([]string(nil), c.execv...) }

// ShellOutput runs a script on the engine's host non-interactively and
// returns its combined output and exit code.
func (c *Client) ShellOutput(ctx context.Context, script string) ([]byte, int, error) {
	argv := []string{}
	for _, a := range c.shellv {
		if a != "-t" { // no tty for captured output
			argv = append(argv, a)
		}
	}
	argv = append(argv, script)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return out, ee.ExitCode(), nil
	}
	if err != nil {
		return out, -1, err
	}
	return out, 0, nil
}

// Shell builds a command that runs a POSIX shell script on the engine's host
// (locally, inside WSL, or over ssh) — used for compose and custom commands.
func (c *Client) Shell(script string) *exec.Cmd {
	argv := append(append([]string(nil), c.shellv...), script)
	return exec.Command(argv[0], argv[1:]...)
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var m struct{ Message string }
		if json.Unmarshal(b, &m) == nil && m.Message != "" {
			return errors.New(m.Message)
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---- API types (only the fields berth uses) ----

type Port struct {
	IP          string
	PrivatePort int
	PublicPort  int
	Type        string
}

type Container struct {
	ID      string
	Names   []string
	Image   string
	State   string // running, exited, paused, restarting, created, dead
	Status  string // "Up 3 hours (healthy)"
	Created int64
	Labels  map[string]string
	Ports   []Port
	ImageID string
	Mounts  []struct {
		Type, Name, Source, Destination string
	}
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string }
	}
}

func (c Container) Name() string {
	if len(c.Names) == 0 {
		return c.ID[:12]
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// Project is the compose project label, or "" when standalone.
func (c Container) Project() string { return c.Labels["com.docker.compose.project"] }

// Health parses the health suffix out of Status.
func (c Container) Health() string {
	switch {
	case strings.Contains(c.Status, "(healthy)"):
		return "healthy"
	case strings.Contains(c.Status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(c.Status, "health: starting"):
		return "starting"
	}
	return ""
}

type Info struct {
	ServerVersion   string
	NCPU            int
	MemTotal        int64
	Name            string
	OperatingSystem string
	Containers      int
	Images          int
}

type Stats struct {
	Read      time.Time `json:"read"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
}

// MemUsed mirrors `docker stats`: usage minus inactive file cache.
func (s *Stats) MemUsed() uint64 {
	u := s.MemoryStats.Usage
	for _, k := range []string{"inactive_file", "total_inactive_file"} {
		if v, ok := s.MemoryStats.Stats[k]; ok && v < u {
			return u - v
		}
	}
	return u
}

func (s *Stats) NetIO() (rx, tx uint64) {
	for _, n := range s.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}
	return
}

func (s *Stats) BlockIO() (r, w uint64) {
	for _, e := range s.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			r += e.Value
		case "write":
			w += e.Value
		}
	}
	return
}

// ---- API calls ----

func (c *Client) Info(ctx context.Context) (Info, error) {
	var i Info
	err := c.do(ctx, http.MethodGet, "/info", &i)
	return i, err
}

func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	var cs []Container
	err := c.do(ctx, http.MethodGet, "/containers/json?all=1", &cs)
	return cs, err
}

// Stats takes a single, immediate sample (no 1s wait on the daemon side).
func (c *Client) Stats(ctx context.Context, id string) (*Stats, error) {
	var s Stats
	err := c.do(ctx, http.MethodGet, "/containers/"+id+"/stats?stream=false&one-shot=true", &s)
	return &s, err
}

// Action runs start|stop|restart|pause|unpause|kill on a container.
func (c *Client) Action(ctx context.Context, id, verb string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/"+verb, nil)
}

// Exec builds an interactive `docker exec -it <id> sh -c ...` command.
func (c *Client) Exec(id string) *exec.Cmd {
	argv := append(c.DockerCLI(), "exec", "-it", id, "sh", "-c", "command -v bash >/dev/null && exec bash || exec sh")
	return exec.Command(argv[0], argv[1:]...)
}
