// Package doctor runs project-defined health checks (from a doctor.yaml)
// against the Docker host. berth stays generic; projects ship their checks.
package doctor

import (
	"context"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/krish-vd/berth/internal/docker"
)

// Check is one entry in doctor.yaml. Run is a POSIX shell script executed
// on the Docker host: exit 0 = pass, 2 = warning, anything else = fail.
// The first output line is the summary; the rest are details.
type Check struct {
	Name    string `yaml:"name"`
	Group   string `yaml:"group"`
	Run     string `yaml:"run"`
	Fix     string `yaml:"fix"`     // optional script, run in the terminal after confirmation
	FixHint string `yaml:"fixHint"` // shown when there is no automatic fix
	Timeout int    `yaml:"timeout"` // seconds, default 30
}

type File struct {
	Title  string  `yaml:"title"`
	Checks []Check `yaml:"checks"`
}

type Status int

const (
	Pending Status = iota
	Running
	Pass
	Warn
	Fail
)

type Result struct {
	Status   Status
	Summary  string
	Details  string
	Duration time.Duration
	At       time.Time
}

// BuiltIn names berth's own generic checks, used when no project file exists.
const BuiltIn = "built-in checks"

//go:embed default.yaml
var builtIn []byte

// Find locates doctor.yaml: explicit path, $BERTH_DOCTOR, .berth/doctor.yaml
// in the working directory or any parent, then the user config dir, then BuiltIn.
func Find(explicit string) string {
	for _, p := range []string{explicit, os.Getenv("BERTH_DOCTOR")} {
		if p != "" {
			return p
		}
	}
	if wd, err := os.Getwd(); err == nil {
		for d := wd; ; d = filepath.Dir(d) {
			p := filepath.Join(d, ".berth", "doctor.yaml")
			if _, err := os.Stat(p); err == nil {
				return p
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		p := filepath.Join(dir, "berth", "doctor.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return BuiltIn
}

func Load(path string) (*File, error) {
	if path == "" {
		return nil, errors.New("no doctor.yaml found")
	}
	b := builtIn
	if path != BuiltIn {
		var err error
		if b, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	var f File
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// RunAll executes every check (4 at a time) and reports each result as it lands.
func RunAll(cli *docker.Client, checks []Check, report func(i int, r Result)) {
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func(i int, c Check) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			report(i, Result{Status: Running})
			report(i, Run(cli, c))
		}(i, c)
	}
	wg.Wait()
}

func Run(cli *docker.Client, c Check) Result {
	to := time.Duration(c.Timeout) * time.Second
	if to <= 0 {
		to = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	start := time.Now()
	out, code, err := cli.ShellOutput(ctx, c.Run)
	r := Result{Duration: time.Since(start), At: time.Now()}
	text := strings.TrimRight(string(out), "\r\n ")
	sum, det, _ := strings.Cut(text, "\n")
	r.Summary, r.Details = strings.TrimSpace(sum), det
	switch {
	case ctx.Err() != nil:
		r.Status, r.Summary = Fail, "timed out after "+to.String()
	case err != nil:
		r.Status, r.Summary = Fail, err.Error()
	case code == 0:
		r.Status = Pass
	case code == 2:
		r.Status = Warn
	default:
		r.Status = Fail
	}
	if r.Summary == "" {
		r.Summary = map[Status]string{Pass: "ok", Warn: "warning", Fail: "failed"}[r.Status]
	}
	return r
}
