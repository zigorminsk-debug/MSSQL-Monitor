package main

// Генерация SVG-графиков для HTML-отчёта (без внешних библиотек).

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Series — линия на графике.
type Series struct {
	Name   string
	Color  string
	Values []float64
	Dashed bool
}

// Threshold — горизонтальная линия-порог.
type Threshold struct {
	Value float64
	Color string
	Label string
}

type axes struct {
	w, h           int
	yMax           float64
	pl, pr, pt, pb int
	gw, gh         int
	xLabels        []string
}

func niceMax(v float64) float64 {
	if v <= 0 {
		return 1
	}
	mag := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if v <= m*mag {
			return m * mag
		}
	}
	return 10 * mag
}

func newAxes(w, h int, yMax float64, xLabels []string) *axes {
	if yMax <= 0 {
		yMax = 1
	}
	a := &axes{w: w, h: h, yMax: niceMax(yMax), pl: 46, pr: 10, pt: 12, pb: 26, xLabels: xLabels}
	a.gw = w - a.pl - a.pr
	a.gh = h - a.pt - a.pb
	return a
}

func (a *axes) x(i, n int) float64 {
	if n <= 1 {
		return float64(a.pl)
	}
	return float64(a.pl) + float64(a.gw)*float64(i)/float64(n-1)
}

func (a *axes) y(v float64) float64 {
	return float64(a.pt) + float64(a.gh)*(1-math.Min(v, a.yMax)/a.yMax)
}

func (a *axes) grid() string {
	var b strings.Builder
	for i := 0; i < 5; i++ {
		v := a.yMax * float64(i) / 4
		y := a.y(v)
		fmt.Fprintf(&b, "<line x1='%d' y1='%.1f' x2='%d' y2='%.1f' class='gridline'/>", a.pl, y, a.w-a.pr, y)
		fmt.Fprintf(&b, "<text x='%d' y='%.1f' class='axis' text-anchor='end'>%s</text>", a.pl-6, y+3.5, fmtAxis(v))
	}
	anchors := []string{"start", "middle", "end"}
	for idx, frac := range []float64{0, 0.5, 1} {
		if idx < len(a.xLabels) {
			x := float64(a.pl) + float64(a.gw)*frac
			fmt.Fprintf(&b, "<text x='%.1f' y='%d' class='axis' text-anchor='%s'>%s</text>",
				x, a.h-8, anchors[idx], esc(a.xLabels[idx]))
		}
	}
	return b.String()
}

func fmtAxis(v float64) string {
	switch {
	case v >= 1000000:
		return fmt.Sprintf("%.0fM", v/1e6)
	case v >= 10000:
		return fmt.Sprintf("%.0fk", v/1e3)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

func downsample(values []float64, target int) []float64 {
	n := len(values)
	if n == 0 {
		return []float64{}
	}
	if n <= target {
		out := make([]float64, n)
		copy(out, values)
		return out
	}
	out := make([]float64, target)
	bucket := float64(n) / float64(target)
	for i := 0; i < target; i++ {
		lo := int(float64(i) * bucket)
		hi := int(float64(i+1) * bucket)
		if hi <= lo {
			hi = lo + 1
		}
		if hi > n {
			hi = n
		}
		sum, cnt := 0.0, 0
		for _, v := range values[lo:hi] {
			sum += v
			cnt++
		}
		if cnt > 0 {
			out[i] = sum / float64(cnt)
		} else if i > 0 {
			out[i] = out[i-1]
		}
	}
	return out
}

func timeLabels(ts []time.Time) []string {
	f := func(t time.Time) string { return t.Format("02.01 15:04") }
	if len(ts) == 0 {
		return []string{"—", "—", "—"}
	}
	return []string{f(ts[0]), f(ts[len(ts)/2]), f(ts[len(ts)-1])}
}

func legend(series []Series) string {
	items := series
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	x0, colW := 46, 150
	for i, s := range items {
		x := x0 + i*colW
		fmt.Fprintf(&b, "<rect x='%d' y='4' width='10' height='10' rx='2' fill='%s'/>", x, s.Color)
		fmt.Fprintf(&b, "<text x='%d' y='13' font-size='11' fill='#475569'>%s</text>", x+15, esc(truncStr(s.Name, 26)))
	}
	return b.String()
}

func lineChart(series []Series, xLabels []string, w, h int, yMax float64, thresholds []Threshold) string {
	maxV := yMax
	if maxV <= 0 {
		for _, s := range series {
			for _, v := range s.Values {
				if v > maxV {
					maxV = v
				}
			}
		}
	}
	ax := newAxes(w, h, maxV, xLabels)
	var b strings.Builder
	fmt.Fprintf(&b, "<svg viewBox='0 0 %d %d' class='chart' preserveAspectRatio='xMidYMid meet' role='img'>", w, h)
	b.WriteString(ax.grid())
	for _, t := range thresholds {
		y := ax.y(t.Value)
		fmt.Fprintf(&b, "<line x1='%d' y1='%.1f' x2='%d' y2='%.1f' stroke='%s' stroke-width='1.2' stroke-dasharray='5,4' opacity='.8'/>",
			ax.pl, y, w-ax.pr, y, t.Color)
		fmt.Fprintf(&b, "<text x='%d' y='%.1f' class='axis' fill='%s'>%s</text>", w-ax.pr-4, y-4, t.Color, esc(t.Label))
	}
	for _, s := range series {
		vv := downsample(s.Values, 420)
		if len(vv) == 0 {
			continue
		}
		pts := make([]string, len(vv))
		for i, v := range vv {
			pts[i] = fmt.Sprintf("%.1f,%.1f", ax.x(i, len(vv)), ax.y(v))
		}
		dash := ""
		if s.Dashed {
			dash = " stroke-dasharray='6,4'"
		}
		fmt.Fprintf(&b, "<polyline fill='none' stroke='%s' stroke-width='2'%s points='%s' stroke-linejoin='round' stroke-linecap='round'/>",
			s.Color, dash, strings.Join(pts, " "))
		fmt.Fprintf(&b, "<circle cx='%.1f' cy='%.1f' r='3' fill='%s'/>", ax.x(len(vv)-1, len(vv)), ax.y(vv[len(vv)-1]), s.Color)
	}
	b.WriteString(legend(series))
	b.WriteString("</svg>")
	return b.String()
}

func areaChart(series []Series, xLabels []string, w, h int, yMax float64) string {
	maxV := yMax
	if maxV <= 0 {
		for _, s := range series {
			for _, v := range s.Values {
				if v > maxV {
					maxV = v
				}
			}
		}
	}
	ax := newAxes(w, h, maxV, xLabels)
	var b strings.Builder
	fmt.Fprintf(&b, "<svg viewBox='0 0 %d %d' class='chart' preserveAspectRatio='xMidYMid meet'>", w, h)
	b.WriteString(ax.grid())
	for _, s := range series {
		vv := downsample(s.Values, 420)
		if len(vv) == 0 {
			continue
		}
		pts := make([]string, len(vv))
		for i, v := range vv {
			pts[i] = fmt.Sprintf("%.1f,%.1f", ax.x(i, len(vv)), ax.y(v))
		}
		fmt.Fprintf(&b, "<polyline fill='none' stroke='%s' stroke-width='2' points='%s'/>", s.Color, strings.Join(pts, " "))
		area := append(append([]string{}, pts...),
			fmt.Sprintf("%.1f,%.1f %.1f,%.1f", ax.x(len(vv)-1, len(vv)), ax.y(0), float64(ax.pl), ax.y(0)))
		fmt.Fprintf(&b, "<polygon fill='%s' opacity='0.13' points='%s'/>", s.Color, strings.Join(area, " "))
	}
	b.WriteString(legend(series))
	b.WriteString("</svg>")
	return b.String()
}

func stackedArea(layers []Series, xLabels []string, w, h int, yMax float64) string {
	n := 0
	for _, l := range layers {
		if len(l.Values) > n {
			n = len(l.Values)
		}
	}
	if n == 0 {
		return emptyChart(w, h)
	}
	totals := make([]float64, n)
	for _, l := range layers {
		for i, v := range l.Values {
			if i < n {
				totals[i] += v
			}
		}
	}
	maxV := yMax
	if maxV <= 0 {
		for _, t := range totals {
			if t > maxV {
				maxV = t
			}
		}
	}
	ax := newAxes(w, h, maxV, xLabels)
	var b strings.Builder
	fmt.Fprintf(&b, "<svg viewBox='0 0 %d %d' class='chart' preserveAspectRatio='xMidYMid meet'>", w, h)
	b.WriteString(ax.grid())
	cum := make([]float64, n)
	for _, l := range layers {
		vv := downsample(l.Values, 420)
		m := len(vv)
		if m == 0 {
			continue
		}
		top := make([]float64, m)
		for i, v := range vv {
			c := 0.0
			if i < len(cum) {
				c = cum[i]
			}
			top[i] = c + v
		}
		ptsTop := make([]string, m)
		for i, v := range top {
			ptsTop[i] = fmt.Sprintf("%.1f,%.1f", ax.x(i, m), ax.y(v))
		}
		ptsBot := make([]string, m)
		for i := m - 1; i >= 0; i-- {
			c := 0.0
			if i < len(cum) {
				c = cum[i]
			}
			ptsBot[m-1-i] = fmt.Sprintf("%.1f,%.1f", ax.x(i, m), ax.y(c))
		}
		fmt.Fprintf(&b, "<polygon fill='%s' opacity='0.75' stroke='%s' stroke-width='1' points='%s %s'/>",
			l.Color, l.Color, strings.Join(ptsTop, " "), strings.Join(ptsBot, " "))
		cum = top
	}
	b.WriteString(legend(layers))
	b.WriteString("</svg>")
	return b.String()
}

// BarItem — горизонтальная полоса.
type BarItem struct {
	Label string
	Value float64
	Color string
}

func hBars(items []BarItem, unit string, w, h int) string {
	if len(items) == 0 {
		return emptyChart(w, 160)
	}
	rowH, pad := 26, 10
	if h <= 0 {
		h = pad*2 + rowH*len(items)
	}
	maxV := 1.0
	for _, it := range items {
		if it.Value > maxV {
			maxV = it.Value
		}
	}
	labelW := int(float64(w) * 0.42)
	barW := w - labelW - 90
	var b strings.Builder
	fmt.Fprintf(&b, "<svg viewBox='0 0 %d %d' class='chart' preserveAspectRatio='xMidYMin meet'>", w, h)
	for i, it := range items {
		y := pad + i*rowH
		bw := math.Max(2, float64(barW)*it.Value/maxV)
		fmt.Fprintf(&b, "<text x='%d' y='%.1f' class='axis' text-anchor='end'>%s</text>",
			labelW-6, float64(y)+12, esc(truncStr(it.Label, 34)))
		fmt.Fprintf(&b, "<rect x='%d' y='%.1f' width='%.1f' height='14' rx='3' fill='%s'/>",
			labelW, float64(y)+2, bw, it.Color)
		fmt.Fprintf(&b, "<text x='%.1f' y='%.1f' class='axis'>%s%s</text>",
			float64(labelW)+bw+6, float64(y)+13, fmtBarVal(it.Value), esc(unit))
	}
	b.WriteString("</svg>")
	return b.String()
}

func fmtBarVal(v float64) string {
	if v >= 1000 {
		s := fmt.Sprintf("%d", int64(math.Round(v)))
		return strings.ReplaceAll(s, ",", " ")
	}
	s := fmt.Sprintf("%.1f", v)
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}

func gaugeSVG(score int) string {
	w, h := 170, 110
	color := "#16a34a"
	switch {
	case score < 50:
		color = "#dc2626"
	case score < 70:
		color = "#f97316"
	case score < 85:
		color = "#f59e0b"
	}
	cx := float64(w) / 2
	cy := float64(h) - 18
	r := float64(w)/2 - 16
	ang := math.Pi * (1 - math.Min(float64(score), 100)/100.0)
	x, y := cx+r*math.Cos(ang), cy-r*math.Sin(ang)
	var b strings.Builder
	fmt.Fprintf(&b, "<svg viewBox='0 0 %d %d' width='%d' height='%d' role='img'>", w, h, w, h)
	fmt.Fprintf(&b, "<path d='M %.1f %.1f A %.1f %.1f 0 0 1 %.1f %.1f' fill='none' stroke='%s' stroke-width='13' stroke-linecap='round' opacity='.18'/>",
		cx-r, cy, r, r, cx+r, cy, color)
	if score > 0 {
		fmt.Fprintf(&b, "<path d='M %.1f %.1f A %.1f %.1f 0 0 1 %.1f %.1f' fill='none' stroke='%s' stroke-width='13' stroke-linecap='round'/>",
			cx-r, cy, r, r, x, y, color)
	}
	fmt.Fprintf(&b, "<text x='%d' y='%d' text-anchor='middle' font-size='30' font-weight='700' fill='%s'>%d</text>",
		int(cx), int(cy)-8, color, score)
	fmt.Fprintf(&b, "<text x='%d' y='%d' text-anchor='middle' font-size='11' fill='#64748b'>из 100</text>",
		int(cx), int(cy)+10)
	b.WriteString("</svg>")
	return b.String()
}

func emptyChart(w, h int) string {
	return fmt.Sprintf("<svg viewBox='0 0 %d %d' class='chart'><text x='50%%' y='50%%' text-anchor='middle' "+
		"font-size='13' fill='#94a3b8'>нет данных за период</text></svg>", w, h)
}
