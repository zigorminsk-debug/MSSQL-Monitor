package main

// Общие утилиты: преобразование значений из БД, форматирование, экранирование.

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"
)

// --- преобразование значений из строк БД (могут быть nil/int64/float64/string/[]byte) ---

func toF(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case uint64:
		return float64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return f
	case []byte:
		return toF(string(x))
	default:
		f, ok := v.(fmt.Stringer)
		if ok {
			return toF(f.String())
		}
		return 0
	}
}

func toI(v any) int64 { return int64(math.Round(toF(v))) }

// toI98 округляет вниз (как SQL-серверные счётчики).
func toPtrF(v any) *float64 {
	if v == nil {
		return nil
	}
	f := toF(v)
	return &f
}

func sstr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case time.Time:
		return x.Format("02.01.2006 15:04")
	default:
		return fmt.Sprintf("%v", x)
	}
}

func bval(v any) bool { return toI(v) != 0 }

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func esc(s any) string { return html.EscapeString(sstr(s)) }

// --- форматирование чисел (как в отчёте) ---

func fmtNum(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	a := math.Abs(v)
	switch {
	case a >= 1e9:
		return fmt.Sprintf("%.1f млрд", v/1e9)
	case a >= 1e6:
		return fmt.Sprintf("%.1f млн", v/1e6)
	case a >= 1e3:
		return strings.ReplaceAll(fmt.Sprintf("%d", int64(math.Round(v))), ",", " ")
	case v == math.Trunc(v):
		return fmt.Sprintf("%d", int64(v))
	default:
		return fmt.Sprintf("%.1f", v)
	}
}

func fmtNumD(v *float64, def string) string {
	if v == nil {
		return def
	}
	return fmtNum(*v)
}

func fmtSizeMB(mb float64) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.1f ГБ", mb/1024)
	}
	return fmt.Sprintf("%.0f МБ", mb)
}

func fmtMS(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1f с", ms/1000)
	}
	return fmt.Sprintf("%.0f мс", ms)
}

func fmtDur(sec int64) string {
	if sec <= 0 {
		return "—"
	}
	d := sec / 86400
	h := (sec % 86400) / 3600
	m := (sec % 3600) / 60
	switch {
	case d > 0:
		return fmt.Sprintf("%d д %d ч", d, h)
	case h > 0:
		return fmt.Sprintf("%d ч %d мин", h, m)
	default:
		return fmt.Sprintf("%d мин", m)
	}
}

func fmtGB(kb float64) string { return fmt.Sprintf("%.0f ГБ", kb/1024/1024) }

// joinNonEmpty склеивает непустые части.
func joinNonEmpty(parts ...string) string {
	out := []string{}
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}
