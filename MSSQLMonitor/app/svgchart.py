# -*- coding: utf-8 -*-
"""Генерация SVG-графиков для HTML-отчёта (без внешних библиотек).

Все графики — самодостаточный inline-SVG: отчёт открывается в любом браузере,
в том числе без интернета.
"""
import html
import math
from datetime import datetime
from typing import List, Optional, Sequence

FONT = "font-family='Segoe UI, Roboto, Arial, sans-serif'"


def esc(s) -> str:
    return html.escape(str(s), quote=True)


def _nice_max(v: float) -> float:
    if v <= 0:
        return 1.0
    mag = 10 ** math.floor(math.log10(v))
    for m in (1, 2, 2.5, 5, 10):
        if v <= m * mag:
            return m * mag
    return 10 * mag


def downsample(values: Sequence[float], target: int = 420) -> List[float]:
    """Прореживание/усреднение длинных серий до target точек."""
    n = len(values)
    if n <= target:
        return [float(v) if v is not None else 0.0 for v in values]
    out = []
    bucket = n / target
    for i in range(target):
        lo, hi = int(i * bucket), max(int(i * bucket) + 1, int((i + 1) * bucket))
        chunk = [v for v in values[lo:hi] if v is not None]
        out.append(sum(chunk) / len(chunk) if chunk else (out[-1] if out else 0.0))
    return out


class _Axes:
    """Координатная сетка + оси."""

    def __init__(self, w, h, y_max, x_labels, pad_l=46, pad_r=10, pad_t=12, pad_b=26, y_unit=''):
        self.w, self.h = w, h
        self.y_max = _nice_max(y_max) if y_max else 1.0
        self.pl, self.pr, self.pt, self.pb = pad_l, pad_r, pad_t, pad_b
        self.gw = w - pad_l - pad_r
        self.gh = h - pad_t - pad_b
        self.x_labels = x_labels
        self.y_unit = y_unit

    def x(self, i, n):
        return self.pl + (self.gw * i / max(1, n - 1))

    def y(self, v):
        return self.pt + self.gh * (1 - min(v, self.y_max) / self.y_max)

    def grid(self):
        parts = []
        for i in range(5):
            v = self.y_max * i / 4
            y = self.y(v)
            parts.append("<line x1='%d' y1='%.1f' x2='%d' y2='%.1f' class='gridline'/>"
                         % (self.pl, y, self.w - self.pr, y))
            parts.append("<text x='%d' y='%.1f' class='axis' text-anchor='end'>%s</text>"
                         % (self.pl - 6, y + 3.5, _fmt(v)))
        for idx, frac in ((0, 0.0), (1, 0.5), (2, 1.0)):
            if idx < len(self.x_labels):
                x = self.pl + self.gw * frac
                parts.append("<text x='%.1f' y='%d' class='axis' text-anchor='%s'>%s</text>"
                             % (x, self.h - 8,
                                'start' if frac == 0 else ('middle' if frac == 0.5 else 'end'),
                                esc(self.x_labels[idx])))
        return "".join(parts)


def _fmt(v: float) -> str:
    if v >= 1000000:
        return '%.0fM' % (v / 1e6)
    if v >= 10000:
        return '%.0fk' % (v / 1e3)
    if v >= 100:
        return '%.0f' % v
    if v >= 10:
        return '%.0f' % v
    return '%.1f' % v


def time_labels(ts_list: Sequence[datetime]) -> List[str]:
    if not ts_list:
        return ['—', '—', '—']
    f = lambda d: d.strftime('%d.%m %H:%M') if hasattr(d, 'strftime') else str(d)
    return [f(ts_list[0]), f(ts_list[len(ts_list) // 2]), f(ts_list[-1])]


def line_chart(series: List[dict], x_labels: List[str], w=860, h=240,
               y_max=None, unit='', thresholds=None) -> str:
    """series: [{name, color, values, dashed?}]"""
    vals = [v for s in series for v in s.get('values', []) if v is not None]
    y_max = y_max or (max(vals) if vals else 1)
    ax = _Axes(w, h, y_max, x_labels, y_unit=unit)
    n = max(len(s.get('values', [])) for s in series) if series else 0
    parts = ["<svg viewBox='0 0 %d %d' class='chart' preserveAspectRatio='xMidYMid meet' role='img'>" % (w, h)]
    parts.append(ax.grid())
    if thresholds:
        for t in thresholds:
            y = ax.y(t['value'])
            parts.append("<line x1='%d' y1='%.1f' x2='%d' y2='%.1f' stroke='%s' "
                         "stroke-width='1.2' stroke-dasharray='5,4' opacity='.8'/>"
                         % (ax.pl, y, w - ax.pr, y, t.get('color', '#dc2626')))
            parts.append("<text x='%d' y='%.1f' class='axis' fill='%s'>%s</text>"
                         % (w - ax.pr - 4, y - 4, t.get('color', '#dc2626'), esc(t.get('label', ''))))
    for s in series:
        vv = downsample(s.get('values', []))
        if not vv:
            continue
        pts = ' '.join('%.1f,%.1f' % (ax.x(i, len(vv)), ax.y(v)) for i, v in enumerate(vv))
        dash = " stroke-dasharray='6,4'" if s.get('dashed') else ''
        parts.append("<polyline fill='none' stroke='%s' stroke-width='2'%s points='%s' "
                     "stroke-linejoin='round' stroke-linecap='round'/>" % (s['color'], dash, pts))
        last = vv[-1]
        parts.append("<circle cx='%.1f' cy='%.1f' r='3' fill='%s'/>" % (ax.x(len(vv) - 1, len(vv)), ax.y(last), s['color']))
    parts.append(_legend(series))
    parts.append("</svg>")
    return ''.join(parts)


def area_chart(series: List[dict], x_labels: List[str], w=860, h=240, y_max=None) -> str:
    """Несколько линий с полупрозрачной заливкой под первой."""
    vals = [v for s in series for v in s.get('values', []) if v is not None]
    y_max = y_max or (max(vals) if vals else 1)
    ax = _Axes(w, h, y_max, x_labels)
    parts = ["<svg viewBox='0 0 %d %d' class='chart' preserveAspectRatio='xMidYMid meet'>" % (w, h)]
    parts.append(ax.grid())
    for s in series:
        vv = downsample(s.get('values', []))
        if not vv:
            continue
        pts = ' '.join('%.1f,%.1f' % (ax.x(i, len(vv)), ax.y(v)) for i, v in enumerate(vv))
        area = "%s %d,%.1f %d,%.1f" % (pts, ax.x(len(vv) - 1, len(vv)), ax.y(0), ax.pl, ax.y(0))
        parts.append("<polyline fill='none' stroke='%s' stroke-width='2' points='%s'/>" % (s['color'], pts))
        parts.append("<polygon fill='%s' opacity='0.13' points='%s'/>" % (s['color'], area))
    parts.append(_legend(series))
    parts.append("</svg>")
    return ''.join(parts)


def stacked_area(layers: List[dict], x_labels: List[str], w=860, h=240, y_max=None) -> str:
    """layers: [{name,color,values}] — слои суммарной площади."""
    n = max((len(l.get('values', [])) for l in layers), default=0)
    if n == 0:
        return _empty(w, h)
    totals = [sum(l['values'][i] for l in layers if len(l['values']) > i) for i in range(n)]
    y_max = y_max or (max(totals) if totals else 1)
    ax = _Axes(w, h, y_max, x_labels)
    parts = ["<svg viewBox='0 0 %d %d' class='chart' preserveAspectRatio='xMidYMid meet'>" % (w, h)]
    parts.append(ax.grid())
    cum = [0.0] * n
    for l in layers:
        vv = downsample(l.get('values', []), 420)
        m = len(vv)
        top = [cum[i] + vv[i] for i in range(m)]
        pts_top = ' '.join('%.1f,%.1f' % (ax.x(i, m), ax.y(v)) for i, v in enumerate(top))
        pts_bot = ' '.join('%.1f,%.1f' % (ax.x(i, m), ax.y(cum[i])) for i in range(m - 1, -1, -1))
        parts.append("<polygon fill='%s' opacity='0.75' stroke='%s' stroke-width='1' points='%s %s'/>"
                     % (l['color'], l['color'], pts_top, pts_bot))
        cum = top
    parts.append(_legend(layers))
    parts.append("</svg>")
    return ''.join(parts)


def hbars(items: List[dict], unit='', w=560, h=None) -> str:
    """items: [{label, value, color}] — горизонтальные полосы."""
    if not items:
        return _empty(w, h or 160)
    row_h, pad = 26, 10
    h = h or (pad * 2 + row_h * len(items))
    max_v = max((i['value'] for i in items), default=1) or 1
    label_w = int(w * 0.42)
    bar_w = w - label_w - 90
    parts = ["<svg viewBox='0 0 %d %d' class='chart' preserveAspectRatio='xMidYMin meet'>" % (w, h)]
    for i, it in enumerate(items):
        y = pad + i * row_h
        bw = max(2.0, bar_w * (it['value'] / max_v))
        parts.append("<text x='%d' y='%.1f' class='axis' text-anchor='end'>%s</text>"
                     % (label_w - 6, y + 12, esc(_trunc(it['label'], 34))))
        parts.append("<rect x='%d' y='%.1f' width='%.1f' height='14' rx='3' fill='%s'/>"
                     % (label_w, y + 2, bw, it.get('color', '#2563eb')))
        parts.append("<text x='%.1f' y='%.1f' class='axis'>%s%s</text>"
                     % (label_w + bw + 6, y + 13, _fmt_value(it['value']), esc(unit)))
    parts.append("</svg>")
    return ''.join(parts)


def gauge(score: int, w=170, h=110) -> str:
    """Полукруглый индикатор здоровья."""
    color = '#16a34a' if score >= 85 else ('#f59e0b' if score >= 70 else ('#f97316' if score >= 50 else '#dc2626'))
    cx, cy, r = w / 2, h - 18, w / 2 - 16
    import math as _m
    ang = _m.pi * (1 - min(score, 100) / 100.0)
    x, y = cx + r * _m.cos(ang), cy - r * _m.sin(ang)
    parts = ["<svg viewBox='0 0 %d %d' width='%d' height='%d' role='img'>" % (w, h, w, h)]
    parts.append("<path d='M %.1f %.1f A %.1f %.1f 0 0 1 %.1f %.1f' fill='none' stroke='%s' "
                 "stroke-width='13' stroke-linecap='round' opacity='.18'/>" % (cx - r, cy, r, r, cx + r, cy, color))
    if score > 0:
        parts.append("<path d='M %.1f %.1f A %.1f %.1f 0 0 1 %.1f %.1f' fill='none' stroke='%s' "
                     "stroke-width='13' stroke-linecap='round'/>" % (cx - r, cy, r, r, x, y, color))
    parts.append("<text x='%d' y='%d' text-anchor='middle' font-size='30' font-weight='700' "
                 "fill='%s' %s>%d</text>" % (cx, cy - 8, color, FONT, score))
    parts.append("<text x='%d' y='%d' text-anchor='middle' font-size='11' fill='#64748b' %s>из 100</text>"
                 % (cx, cy + 10, FONT))
    parts.append("</svg>")
    return ''.join(parts)


def _legend(series):
    items = [s for s in series if s.get('values')]
    if not items:
        return ''
    n = len(items)
    col_w = 150
    total_w = col_w * n
    x0 = 46
    parts = ["<g>"]
    for i, s in enumerate(items):
        x = x0 + i * col_w
        parts.append("<rect x='%d' y='4' width='10' height='10' rx='2' fill='%s'/>" % (x, s['color']))
        parts.append("<text x='%d' y='13' font-size='11' fill='#475569' %s>%s</text>"
                     % (x + 15, FONT, esc(_trunc(s.get('name', ''), 26))))
    parts.append("</g>")
    return ''.join(parts)


def _trunc(s, n):
    s = str(s)
    return s if len(s) <= n else s[:n - 1] + '…'


def _fmt_value(v):
    if v >= 1000:
        return f"{int(round(v)):,}".replace(',', ' ')
    return ('%.1f' % v).rstrip('0').rstrip('.') if isinstance(v, float) else str(v)


def _empty(w, h):
    return ("<svg viewBox='0 0 %d %d' class='chart'><text x='50%%' y='50%%' text-anchor='middle' "
            "font-size='13' fill='#94a3b8' %s>нет данных за период</text></svg>" % (w, h, FONT))
