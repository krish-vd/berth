package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/krish-vd/berth/internal/ui"
	"strings"
	"time"
	_ "time/tzdata" // zone database baked in, so LoadLocation works everywhere
)

// windowsZones maps common Windows timezone IDs (tzutil /g) to IANA names.
var windowsZones = map[string]string{
	"India Standard Time":          "Asia/Kolkata",
	"UTC":                          "UTC",
	"GMT Standard Time":            "Europe/London",
	"W. Europe Standard Time":      "Europe/Berlin",
	"Central Europe Standard Time": "Europe/Budapest",
	"Romance Standard Time":        "Europe/Paris",
	"Eastern Standard Time":        "America/New_York",
	"Central Standard Time":        "America/Chicago",
	"Mountain Standard Time":       "America/Denver",
	"Pacific Standard Time":        "America/Los_Angeles",
	"Arabian Standard Time":        "Asia/Dubai",
	"Singapore Standard Time":      "Asia/Singapore",
	"China Standard Time":          "Asia/Shanghai",
	"Tokyo Standard Time":          "Asia/Tokyo",
	"AUS Eastern Standard Time":    "Australia/Sydney",
	"Nepal Standard Time":          "Asia/Kathmandu",
	"Sri Lanka Standard Time":      "Asia/Colombo",
	"Bangladesh Standard Time":     "Asia/Dhaka",
	"Pakistan Standard Time":       "Asia/Karachi",
}

// setupTimezone picks the zone for every timestamp berth shows:
// --tz / $BERTH_TZ, then $TZ, then (inside WSL, whose clock defaults to
// UTC) the Windows host's zone, else the system default.
func setupTimezone(flagTZ string) {
	name := flagTZ
	if name == "" {
		name = os.Getenv("BERTH_TZ")
	}
	if name == "" && os.Getenv("TZ") == "" && isWSL() && time.Now().Location().String() == "Local" {
		if _, off := time.Now().Zone(); off == 0 {
			name = windowsZone()
		}
	}
	if name == "" {
		return
	}
	if loc, err := time.LoadLocation(name); err == nil {
		time.Local = loc
	}
}

func isWSL() bool {
	b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.Contains(strings.ToLower(string(b)), "microsoft")
}

func windowsZone() string {
	out, err := exec.Command("tzutil.exe", "/g").Output()
	if err != nil {
		return ""
	}
	return windowsZones[strings.TrimSpace(string(out))]
}

// startStats writes frame/view/update rates to path every 10s (dev aid).
func startStats(path string) {
	go func() {
		var f0, v0, u0 int64
		for range time.Tick(10 * time.Second) {
			f, v, u := ui.StatFrames.Load(), ui.StatViews.Load(), ui.StatUpdates.Load()
			line := fmt.Sprintf("per sec: frames=%.1f views=%.1f updates=%.1f\n", float64(f-f0)/10, float64(v-v0)/10, float64(u-u0)/10)
			f0, v0, u0 = f, v, u
			fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err == nil {
				_, _ = fh.WriteString(line)
				_ = fh.Close()
			}
		}
	}()
}
