package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ---- inspect ----

type HealthLog struct {
	Start, End string
	ExitCode   int
	Output     string
}

type Inspect struct {
	ID      string
	Name    string
	Created string
	Path    string
	Args    []string
	State   struct {
		Status     string
		Running    bool
		Paused     bool
		Restarting bool
		OOMKilled  bool
		Dead       bool
		Pid        int
		ExitCode   int
		Error      string
		StartedAt  string
		FinishedAt string
		Health     *struct {
			Status        string
			FailingStreak int
			Log           []HealthLog
		}
	}
	RestartCount int
	Config       struct {
		Hostname    string
		User        string
		Env         []string
		Cmd         []string
		Entrypoint  []string
		Image       string
		WorkingDir  string
		Labels      map[string]string
		Tty         bool
		Healthcheck *struct {
			Test     []string
			Interval int64
			Timeout  int64
			Retries  int
		}
	}
	HostConfig struct {
		RestartPolicy struct {
			Name              string
			MaximumRetryCount int
		}
		Memory      int64
		NanoCpus    int64
		NetworkMode string
	}
	Mounts []struct {
		Type, Name, Source, Destination, Mode string
		RW                                    bool
	}
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress  string
			Gateway    string
			MacAddress string
			Aliases    []string
		}
	}
}

func (c *Client) Inspect(ctx context.Context, id string) (*Inspect, error) {
	var i Inspect
	err := c.do(ctx, http.MethodGet, "/containers/"+id+"/json", &i)
	return &i, err
}

// InspectRaw returns the engine's pretty-printed JSON for kind
// (container|image|volume|network) and id.
func (c *Client) InspectRaw(ctx context.Context, kind, id string) ([]byte, error) {
	path := map[string]string{
		"container": "/containers/" + id + "/json",
		"image":     "/images/" + id + "/json",
		"volume":    "/volumes/" + url.PathEscape(id),
		"network":   "/networks/" + id,
	}[kind]
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, path, &raw); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return raw, nil
	}
	return buf.Bytes(), nil
}

// ---- top ----

type Top struct {
	Titles    []string
	Processes [][]string
}

func (c *Client) Top(ctx context.Context, id string) (*Top, error) {
	var t Top
	args := url.QueryEscape("-o pid,user,pcpu,pmem,rss,etime,args")
	err := c.do(ctx, http.MethodGet, "/containers/"+id+"/top?ps_args="+args, &t)
	return &t, err
}

// ---- streaming ----

func (c *Client) stream(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return resp, nil
}

// LogLine is one line of container output.
type LogLine struct {
	Time   time.Time
	Stderr bool
	Text   string
}

// Logs follows a container's logs, invoking fn per line until ctx ends.
func (c *Client) Logs(ctx context.Context, id string, tail int, tty bool, fn func(LogLine)) error {
	resp, err := c.stream(ctx, fmt.Sprintf("/containers/%s/logs?follow=1&stdout=1&stderr=1&timestamps=1&tail=%d", id, tail))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	emit := func(stderr bool, raw string) {
		raw = strings.TrimRight(raw, "\r\n")
		l := LogLine{Stderr: stderr, Text: raw}
		if i := strings.IndexByte(raw, ' '); i > 0 {
			if t, err := time.Parse(time.RFC3339Nano, raw[:i]); err == nil {
				l.Time, l.Text = t, raw[i+1:]
			}
		}
		fn(l)
	}
	if tty {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			emit(false, sc.Text())
		}
		return sc.Err()
	}
	// multiplexed stream: [stream,0,0,0,size32be] + payload
	br := bufio.NewReaderSize(resp.Body, 64*1024)
	hdr := make([]byte, 8)
	partial := [3]string{}
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			return err
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			return err
		}
		s := hdr[0] % 3
		data := partial[s] + string(buf)
		for {
			i := strings.IndexByte(data, '\n')
			if i < 0 {
				break
			}
			emit(s == 2, data[:i])
			data = data[i+1:]
		}
		partial[s] = data
	}
}

type Event struct {
	Type   string
	Action string
	Actor  struct {
		ID         string
		Attributes map[string]string
	}
	Time     int64
	TimeNano int64 `json:"timeNano"`
}

func (e Event) Name() string {
	if n := e.Actor.Attributes["name"]; n != "" {
		return n
	}
	if len(e.Actor.ID) > 12 {
		return e.Actor.ID[:12]
	}
	return e.Actor.ID
}

// Events streams engine events until ctx ends.
func (c *Client) Events(ctx context.Context, fn func(Event)) error {
	resp, err := c.stream(ctx, "/events")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var e Event
		if err := dec.Decode(&e); err != nil {
			return err
		}
		fn(e)
	}
}

// ---- images / volumes / networks ----

type Image struct {
	ID          string
	RepoTags    []string
	RepoDigests []string
	Created     int64
	Size        int64
	Labels      map[string]string
}

func (i Image) Tag() string {
	for _, t := range i.RepoTags {
		if t != "<none>:<none>" {
			return t
		}
	}
	if len(i.RepoDigests) > 0 {
		return strings.SplitN(i.RepoDigests[0], "@", 2)[0] + ":<none>"
	}
	return "<none>"
}

func (i Image) ShortID() string {
	id := strings.TrimPrefix(i.ID, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (c *Client) Images(ctx context.Context) ([]Image, error) {
	var is []Image
	err := c.do(ctx, http.MethodGet, "/images/json", &is)
	return is, err
}

type Layer struct {
	ID        string
	Created   int64
	CreatedBy string
	Size      int64
	Tags      []string
}

func (c *Client) History(ctx context.Context, id string) ([]Layer, error) {
	var ls []Layer
	err := c.do(ctx, http.MethodGet, "/images/"+id+"/history", &ls)
	return ls, err
}

type Volume struct {
	Name       string
	Driver     string
	Mountpoint string
	CreatedAt  string
	Labels     map[string]string
	Scope      string
	UsageData  *struct {
		Size     int64
		RefCount int64
	}
}

func (c *Client) Volumes(ctx context.Context) ([]Volume, error) {
	var r struct{ Volumes []Volume }
	err := c.do(ctx, http.MethodGet, "/volumes", &r)
	return r.Volumes, err
}

type DiskUsage struct {
	LayersSize int64
	Volumes    []Volume
	BuildCache []struct{ Size int64 }
}

func (c *Client) DiskUsage(ctx context.Context) (*DiskUsage, error) {
	var d DiskUsage
	err := c.do(ctx, http.MethodGet, "/system/df", &d)
	return &d, err
}

type Network struct {
	Name     string
	ID       string
	Created  string
	Driver   string
	Scope    string
	Internal bool
	IPAM     struct {
		Config []struct{ Subnet, Gateway string }
	}
	Labels map[string]string
}

func (c *Client) Networks(ctx context.Context) ([]Network, error) {
	var ns []Network
	err := c.do(ctx, http.MethodGet, "/networks", &ns)
	return ns, err
}

// ---- destructive ----

func (c *Client) RemoveContainer(ctx context.Context, id string, force, volumes bool) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/containers/%s?force=%t&v=%t", id, force, volumes), nil)
}

func (c *Client) RemoveImage(ctx context.Context, id string, force bool) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/images/%s?force=%t", id, force), nil)
}

func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), nil)
}

func (c *Client) RemoveNetwork(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/networks/"+id, nil)
}

// Prune removes unused objects of kind containers|images|volumes|networks
// and returns bytes reclaimed.
func (c *Client) Prune(ctx context.Context, kind string) (uint64, error) {
	// note: volume prune only removes anonymous volumes (API >= 1.42) — named
	// volumes such as database data are deliberately never touched.
	path := "/" + kind + "/prune"
	var r struct{ SpaceReclaimed uint64 }
	err := c.do(ctx, http.MethodPost, path, &r)
	return r.SpaceReclaimed, err
}
