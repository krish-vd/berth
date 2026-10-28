// berth — a live, graph-first terminal observatory for Docker.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/pprof"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/krish-vd/berth/internal/docker"
	"github.com/krish-vd/berth/internal/hosts"
	"github.com/krish-vd/berth/internal/metrics"
	"github.com/krish-vd/berth/internal/ui"
)

var version = "dev"

func main() {
	host := flag.String("host", "", "docker endpoint: unix:///path, tcp://h:p, ssh://user@host, wsl://[distro] (default $DOCKER_HOST)")
	interval := flag.Duration("interval", time.Second, "sampling interval")
	showVersion := flag.Bool("version", false, "print version and exit")
	snapshot := flag.String("snapshot", "", "render one frame at WxH (e.g. 160x45) to stdout and exit")
	keys := flag.String("keys", "", "with --snapshot: keys to press before rendering (space-separated, e.g. \"] ]\" or \"2 down\")")
	doctorFile := flag.String("doctor", "", "doctor checks file (default: .berth/doctor.yaml in this folder or a parent, or $BERTH_DOCTOR)")
	tz := flag.String("tz", "", "timezone for displayed times, e.g. Asia/Kolkata (default: system; in WSL the Windows zone)")
	flag.Parse()
	setupTimezone(*tz)
	ui.DoctorPath = *doctorFile
	ui.StartInDoctor = flag.Arg(0) == "doctor"
	if p := os.Getenv("BERTH_STATS"); p != "" {
		startStats(p)
	}

	if *showVersion {
		fmt.Println("berth", version)
		return
	}

	if path := os.Getenv("BERTH_CPUPROFILE"); path != "" { // dev: go tool pprof
		if f, err := os.Create(path); err == nil {
			if err := pprof.StartCPUProfile(f); err != nil {
				_ = os.WriteFile(path+".err", []byte(err.Error()), 0o644)
			}
			time.AfterFunc(20*time.Second, func() {
				pprof.StopCPUProfile()
				_ = f.Close()
				_ = os.WriteFile(path+".done", nil, 0o644)
			})
		}
	}

	endpoint := docker.Resolve(*host)
	if *snapshot != "" {
		cli, err := docker.New(endpoint)
		if err != nil {
			fmt.Fprintln(os.Stderr, "berth:", err)
			os.Exit(1)
		}
		ui.CurrentHost = endpoint
		var w, h int
		fmt.Sscanf(*snapshot, "%dx%d", &w, &h)
		fmt.Println(headless(context.Background(), cli, metrics.NewSampler(cli, *interval), w, h, *keys))
		return
	}
	// one session per environment: picking another host in the Hosts view (0)
	// ends this session and starts a fresh one there
	for {
		next, err := session(endpoint, *interval)
		if err != nil {
			fmt.Fprintln(os.Stderr, "berth:", err)
			os.Exit(1)
		}
		if next == "" {
			return
		}
		endpoint = next
		ui.StartInDoctor = false
	}
}

// session runs berth against one Docker endpoint until the user quits
// (returns "") or switches environment (returns the new endpoint).
func session(endpoint string, interval time.Duration) (string, error) {
	cli, err := docker.New(endpoint)
	if err != nil {
		return "", err
	}
	ui.CurrentHost, ui.SwitchTo, ui.HostLabel = endpoint, "", ""
	if hs := hosts.Discover(endpoint); len(hs) > 0 && hs[0].Name != "This machine" {
		ui.HostLabel = hs[0].Name
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	smp := metrics.NewSampler(cli, interval)
	p := tea.NewProgram(ui.New(cli, smp), tea.WithAltScreen(), tea.WithMouseAllMotion(), tea.WithFPS(20))
	go smp.Run(ctx, func(s metrics.Snapshot) { p.Send(ui.SnapMsg(s)) })
	go func() { // engine events, reconnecting with backoff
		for backoff := time.Second; ctx.Err() == nil; {
			start := time.Now()
			_ = cli.Events(ctx, func(e docker.Event) {
				if !strings.HasPrefix(e.Action, "exec_") { // healthcheck noise: don't wake the UI
					p.Send(ui.EventMsg(e))
				}
			})
			if time.Since(start) > time.Minute {
				backoff = time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}()

	if _, err := p.Run(); err != nil {
		return "", err
	}
	return ui.SwitchTo, nil
}
