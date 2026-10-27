package ui

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	colorful "github.com/lucasb-eyer/go-colorful"
)

// Palette — a calm, deep-night theme with a load-reactive accent ramp.
var (
	cBg       = lipgloss.Color("#16161e")
	cPanel    = lipgloss.Color("#1a1b26")
	cBorder   = lipgloss.Color("#2f334d")
	cBorderHi = lipgloss.Color("#7aa2f7")
	cText     = lipgloss.Color("#c0caf5")
	cMuted    = lipgloss.Color("#565f89")
	cSubtle   = lipgloss.Color("#3b4261")
	cAccent   = lipgloss.Color("#7aa2f7")
	cAccent2  = lipgloss.Color("#bb9af7")
	cGreen    = lipgloss.Color("#9ece6a")
	cYellow   = lipgloss.Color("#e0af68")
	cRed      = lipgloss.Color("#f7768e")
	cCyan     = lipgloss.Color("#7dcfff")
	cTeal     = lipgloss.Color("#73daca")
	cSelBg    = lipgloss.Color("#283457")
)

var (
	sText   = fg(cText)
	sMuted  = fg(cMuted)
	sSubtle = fg(cSubtle)
	sBold   = fg(cText).Bold(true)
	sAccent = fg(cAccent).Bold(true)
	sKey    = fg(cAccent2).Bold(true)
)

// ramp stops: cool teal → blue → amber → red as load rises.
var ramp = []colorful.Color{
	hex("#73daca"),
	hex("#7aa2f7"),
	hex("#e0af68"),
	hex("#f7768e"),
}

func hex(s string) colorful.Color { c, _ := colorful.Hex(s); return c }

// heatTable is the load ramp precomputed at 128 steps (Luv blending per
// cell per frame was expensive).
var heatTable = func() [128]lipgloss.Color {
	var tbl [128]lipgloss.Color
	for k := range tbl {
		t := float64(k) / float64(len(tbl)-1)
		seg := t * float64(len(ramp)-1)
		i := min(int(seg), len(ramp)-2)
		tbl[k] = lipgloss.Color(ramp[i].BlendLuv(ramp[i+1], seg-float64(i)).Clamped().Hex())
	}
	return tbl
}()

// Heat maps t∈[0,1] onto the load ramp.
func Heat(t float64) lipgloss.Color {
	t = math.Max(0, math.Min(1, t))
	return heatTable[int(math.Round(t*float64(len(heatTable)-1)))]
}

var dimCache sync.Map

// Dim blends a color toward the panel background (memoised).
func Dim(c lipgloss.Color, amt float64) lipgloss.Color {
	key := string(c) + fmt.Sprintf("%.2f", amt)
	if v, ok := dimCache.Load(key); ok {
		return v.(lipgloss.Color)
	}
	a := hex(string(c))
	out := lipgloss.Color(a.BlendRgb(hex(string(cPanel)), math.Round(amt*100)/100).Clamped().Hex())
	dimCache.Store(key, out)
	return out
}

// Pulse returns a color oscillating between c and its dimmed self. It is
// wall-clock based so it looks the same at any frame rate.
func Pulse(c lipgloss.Color, _ int) lipgloss.Color {
	t := (math.Sin(float64(time.Now().UnixMilli())/1000*2.4) + 1) / 2
	return Dim(c, math.Round(0.65*t*20)/20)
}

func HumanBytes(b float64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%.0fB", b)
	}
	div, exp := float64(unit), 0
	for n := b / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	v := b / div
	if v >= 100 {
		return fmt.Sprintf("%.0f%c", v, "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f%c", v, "KMGTPE"[exp])
}

func HumanRate(b float64) string { return HumanBytes(b) + "/s" }
