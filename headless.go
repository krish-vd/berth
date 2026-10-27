package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/krish-vd/berth/internal/docker"
	"github.com/krish-vd/berth/internal/metrics"
	"github.com/krish-vd/berth/internal/ui"
)

// headless drives the UI without a terminal: it samples for a few seconds,
// presses the given keys, lets async loads land, then returns one frame.
// Used for screenshots, docs and smoke tests.
func headless(ctx context.Context, cli *docker.Client, smp *metrics.Sampler, w, h int, keys string) string {
	msgs := make(chan tea.Msg, 256)
	var exec func(tea.Cmd)
	exec = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			msg := c()
			if b, ok := msg.(tea.BatchMsg); ok {
				for _, cc := range b {
					exec(cc)
				}
				return
			}
			if msg != nil {
				select {
				case msgs <- msg:
				case <-time.After(10 * time.Second):
				}
			}
		}()
	}

	var m tea.Model = ui.New(cli, smp)
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	exec(m.Init())

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	t0 := time.Now()
	go smp.Run(sctx, func(s metrics.Snapshot) {
		if os.Getenv("BERTH_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "%5.1fs round containers=%d err=%v\n", time.Since(t0).Seconds(), len(s.Containers), s.Err)
		}
		msgs <- ui.SnapMsg(s)
	})
	if os.Getenv("BERTH_NOEVENTS") == "" {
		go func() {
			_ = cli.Events(sctx, func(e docker.Event) {
				if os.Getenv("BERTH_DEBUG") != "" && !strings.HasPrefix(e.Action, "exec_") {
					fmt.Fprintf(os.Stderr, "%5.1fs event %s %s %s exit=%s\n", time.Since(t0).Seconds(), e.Type, e.Action, e.Name(), e.Actor.Attributes["exitCode"])
				}
				msgs <- ui.EventMsg(e)
			})
		}()
	}

	keysAt := time.After(8 * time.Second)
	wait := 12 * time.Second
	if s := os.Getenv("BERTH_SNAPSHOT_WAIT"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			wait = d
		}
	}
	done := time.After(wait)
	for {
		select {
		case msg := <-msgs:
			var c tea.Cmd
			m, c = m.Update(msg)
			exec(c)
		case <-keysAt:
			for _, k := range strings.Fields(keys) {
				var c tea.Cmd
				m, c = m.Update(keyMsg(k))
				exec(c)
			}
		case <-done:
			if os.Getenv("BERTH_BENCH") != "" {
				for _, label := range []string{"steady", "animating"} {
					t := time.Now()
					for i := 0; i < 200; i++ {
						if label == "animating" {
							m, _ = m.Update(ui.FrameMsg())
						}
						_ = m.View()
					}
					fmt.Fprintf(os.Stderr, "View %s: %.2f ms/frame\n", label, float64(time.Since(t).Microseconds())/200/1000)
				}
			}
			return m.View()
		}
	}
}

func keyMsg(k string) tea.Msg {
	// mouse: click:X,Y or move:X,Y (0-based cells)
	if kind, xy, ok := strings.Cut(k, ":"); ok && (kind == "click" || kind == "move") {
		var x, y int
		fmt.Sscanf(xy, "%d,%d", &x, &y)
		ev := tea.MouseEvent{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
		if kind == "move" {
			ev.Button, ev.Action = tea.MouseButtonNone, tea.MouseActionMotion
		}
		return tea.MouseMsg(ev)
	}
	switch k {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+p":
		return tea.KeyMsg{Type: tea.KeyCtrlP}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}
