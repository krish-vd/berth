package ui

import (
	"math"
	"strings"
)

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// Sparkline renders the last `width` values as heat-coloured blocks.
// max <= 0 auto-scales.
func Sparkline(vals []float64, width int, max float64) string {
	if width <= 0 {
		return ""
	}
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	if max <= 0 {
		max = maxOf(vals)
	}
	var b strings.Builder
	b.WriteString(sSubtle.Render(strings.Repeat("·", width-len(vals))))
	for _, v := range vals {
		t := 0.0
		if max > 0 {
			t = math.Max(0, math.Min(1, v/max))
		}
		idx := int(math.Round(t * float64(len(sparkRunes)-1)))
		b.WriteString(fg(Heat(t)).Render(string(sparkRunes[idx])))
	}
	return b.String()
}

// braille dot bits by [col][row] within one 2×4 cell.
var dotBits = [2][4]rune{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}

// AreaChart draws a braille line chart with a stippled fill, newest value on
// the right. Cell colour follows height (heat ramp) so peaks glow warmer.
// heatScale decides how "hot" the top of the chart is (0..1); pass the
// value/max ratio that the chart's ceiling represents.
func AreaChart(vals []float64, width, height int, ceil float64, heatScale float64) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	dw, dh := width*2, height*4
	if len(vals) > dw {
		vals = vals[len(vals)-dw:]
	}
	if ceil <= 0 {
		ceil = maxOf(vals)
	}
	if ceil <= 0 {
		ceil = 1
	}
	line := make([][]rune, height)
	fill := make([][]rune, height)
	for i := range line {
		line[i] = make([]rune, width)
		fill[i] = make([]rune, width)
	}
	set := func(g [][]rune, x, y int) { // y=0 bottom
		if x < 0 || x >= dw || y < 0 || y >= dh {
			return
		}
		row := height - 1 - y/4
		g[row][x/2] |= dotBits[x%2][3-y%4]
	}
	off := dw - len(vals)
	prevY := -1
	for i, v := range vals {
		x := off + i
		y := int(math.Round(math.Max(0, math.Min(1, v/ceil)) * float64(dh-1)))
		lo, hi := y, y
		if prevY >= 0 {
			lo, hi = min(prevY, y), max(prevY, y)
			// connect halfway so slopes are shared between columns
			if y > prevY {
				lo = prevY + (y-prevY)/2
			} else if y < prevY {
				hi = y + (prevY-y)/2
			}
		}
		for yy := lo; yy <= hi; yy++ {
			set(line, x, yy)
		}
		for yy := 0; yy < lo; yy++ {
			if (x+yy)%2 == 0 {
				set(fill, x, yy)
			}
		}
		prevY = y
	}

	out := make([]string, height)
	var b strings.Builder
	for r := 0; r < height; r++ {
		b.Reset()
		level := float64(height-r) / float64(height) * heatScale
		lc := Heat(level)
		fc := Dim(lc, 0.72)
		ls := fg(lc)
		fs := fg(fc)
		gs := fg(cSubtle)
		// group runs of identical style to keep escape output small
		var run strings.Builder
		cur := -1
		flush := func() {
			if run.Len() == 0 {
				return
			}
			switch cur {
			case 0:
				b.WriteString(gs.Render(run.String()))
			case 1:
				b.WriteString(fs.Render(run.String()))
			case 2:
				b.WriteString(ls.Render(run.String()))
			}
			run.Reset()
		}
		for c := 0; c < width; c++ {
			l, f := line[r][c], fill[r][c]
			kind, ch := 0, ' '
			switch {
			case l != 0:
				kind, ch = 2, 0x2800+(l|f)
			case f != 0:
				kind, ch = 1, 0x2800+f
			default:
				if r == height-1 {
					ch = '⠀'
				}
			}
			if kind != cur {
				flush()
				cur = kind
			}
			run.WriteRune(ch)
		}
		flush()
		out[r] = b.String()
	}
	return out
}

// Bar renders a horizontal gauge of `width` cells with a heat-coloured fill.
func Bar(t float64, width int) string {
	t = math.Max(0, math.Min(1, t))
	full := t * float64(width)
	n := int(full)
	parts := []rune(" ▏▎▍▌▋▊▉")
	var b strings.Builder
	col := fg(Heat(t))
	b.WriteString(col.Render(strings.Repeat("█", n)))
	rest := width - n
	if rest > 0 {
		frac := int((full - float64(n)) * 8)
		if frac > 0 {
			b.WriteString(col.Render(string(parts[frac])))
			rest--
		}
		b.WriteString(sSubtle.Render(strings.Repeat("─", rest)))
	}
	return b.String()
}

// Tween eases a displayed value toward its target each frame.
type Tween struct{ Cur, Target float64 }

func (t *Tween) Set(v float64) { t.Target = v }
func (t *Tween) Step() {
	d := t.Target - t.Cur
	if t.Settled() {
		t.Cur = t.Target
		return
	}
	t.Cur += d * 0.7 // settles in ~3 frames
}

// Settled reports whether the value is visually at its target.
func (t *Tween) Settled() bool {
	return math.Abs(t.Target-t.Cur) <= math.Max(0.05, math.Abs(t.Target)*0.002)
}

func maxOf(v []float64) float64 {
	m := 0.0
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}

// niceMax rounds a ceiling up to a pleasant axis value.
func niceMax(v float64) float64 {
	if v <= 0 {
		return 1
	}
	exp := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if v <= m*exp {
			return m * exp
		}
	}
	return 10 * exp
}
