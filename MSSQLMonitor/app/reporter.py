# -*- coding: utf-8 -*-
"""Формирование подробного HTML-отчёта (самодостаточный файл: inline CSS + SVG)."""
import html
import os
from datetime import datetime, timedelta
from typing import Optional

from . import svgchart as SC
from .analyzer import Analysis, SEV_RU, WAIT_CATS, missing_index_script, categorize_wait

# ------------------------------------------------------------------ формат
def fnum(v, default='—'):
    try:
        v = float(v)
    except (TypeError, ValueError):
        return default
    if abs(v) >= 1e9:
        return '%.1f млрд' % (v / 1e9)
    if abs(v) >= 1e6:
        return '%.1f млн' % (v / 1e6)
    if abs(v) >= 1e3:
        return f"{int(round(v)):,}".replace(',', ' ')
    if v == int(v):
        return str(int(v))
    return '%.1f' % v


def fsize_mb(v):
    try:
        v = float(v)
    except (TypeError, ValueError):
        return '—'
    if v >= 1024:
        return '%.1f ГБ' % (v / 1024)
    return '%.0f МБ' % v


def fms(v):
    try:
        v = float(v)
    except (TypeError, ValueError):
        return '—'
    if v >= 1000:
        return '%.1f с' % (v / 1000)
    return '%.0f мс' % v


def fdt(d):
    if isinstance(d, datetime):
        return d.strftime('%d.%m.%Y %H:%M')
    return str(d) if d else '—'


def fdur(sec):
    try:
        sec = int(sec)
    except (TypeError, ValueError):
        return '—'
    d, r = divmod(sec, 86400)
    h, r = divmod(r, 3600)
    m, _ = divmod(r, 60)
    if d:
        return '%d д %d ч' % (d, h)
    if h:
        return '%d ч %d мин' % (h, m)
    return '%d мин' % m


def esc(s):
    return html.escape(str(s if s is not None else '—'), quote=True)


SEV_CLASS = {'critical': 'c-crit', 'warning': 'c-warn', 'info': 'c-info', 'ok': 'c-ok'}
STATUS_ICON = {'bad': '⛔', 'warn': '⚠️', 'ok': '✅'}


def badge(sev):
    return "<span class='badge %s'>%s</span>" % (SEV_CLASS[sev], SEV_RU[sev])


# ------------------------------------------------------------------ шаблон
CSS = """
:root{--ink:#0f172a;--mut:#64748b;--line:#e2e8f0;--bg:#eef2f7;--card:#fff;--acc:#1d4ed8}
*{box-sizing:border-box}
body{margin:0;font:14px/1.55 'Segoe UI',Roboto,Arial,sans-serif;color:var(--ink);background:var(--bg)}
a{color:var(--acc);text-decoration:none}a:hover{text-decoration:underline}
.wrap{max-width:1100px;margin:0 auto;padding:0 20px 60px}
header.rep{background:linear-gradient(135deg,#0f172a 0%,#1e3a8a 60%,#1d4ed8 100%);color:#fff;
 padding:30px 20px 22px;margin-bottom:22px}
header.rep .wrap{padding-bottom:0}
h1{margin:0 0 4px;font-size:24px;font-weight:700}
.sub{color:#c7d2fe;font-size:13px}
.grid{display:flex;flex-wrap:wrap;gap:14px;margin-top:18px}
.kpi{background:rgba(255,255,255,.08);border:1px solid rgba(255,255,255,.15);border-radius:12px;
 padding:12px 18px;min-width:150px}
.kpi .v{font-size:21px;font-weight:700}
.kpi .l{font-size:11.5px;color:#c7d2fe;text-transform:uppercase;letter-spacing:.4px}
nav{position:sticky;top:0;background:#ffffffee;backdrop-filter:blur(6px);border-bottom:1px solid var(--line);
 z-index:5;padding:8px 20px;font-size:13px}
nav .wrap{padding-bottom:0;display:flex;flex-wrap:wrap;gap:4px 16px}
h2{font-size:19px;margin:34px 0 12px;padding-bottom:8px;border-bottom:2px solid var(--line)}
h2 .num{color:#94a3b8;font-weight:500;margin-right:6px}
h3{font-size:15px;margin:20px 0 8px}
.card{background:var(--card);border:1px solid var(--line);border-radius:14px;padding:16px 18px;margin:12px 0}
.card.transparent{background:transparent;border:0;padding:0}
table{width:100%;border-collapse:collapse;font-size:13px;background:var(--card);
 border:1px solid var(--line);border-radius:10px;overflow:hidden}
th{background:#f8fafc;text-align:left;padding:8px 10px;font-weight:600;color:#334155;
 border-bottom:1px solid var(--line);white-space:nowrap}
td{padding:7px 10px;border-bottom:1px solid #f1f5f9;vertical-align:top}
tr:last-child td{border-bottom:0}
tr:hover td{background:#f8fafc}
td.r,th.r{text-align:right;white-space:nowrap}
.mono{font-family:Consolas,Menlo,monospace;font-size:12px}
.badge{display:inline-block;padding:2px 9px;border-radius:20px;font-size:11.5px;font-weight:600;white-space:nowrap}
.c-crit{background:#fee2e2;color:#b91c1c}.c-warn{background:#fef3c7;color:#b45309}
.c-info{background:#dbeafe;color:#1d4ed8}.c-ok{background:#dcfce7;color:#15803d}
.tag{display:inline-block;padding:1px 8px;border-radius:6px;font-size:11px;background:#f1f5f9;color:#475569}
.tag.red{background:#fee2e2;color:#b91c1c}.tag.amber{background:#fef3c7;color:#b45309}
.tag.green{background:#dcfce7;color:#15803d}
.finding{border:1px solid var(--line);border-left:5px solid #94a3b8;border-radius:10px;
 background:var(--card);padding:12px 16px;margin:10px 0}
.finding.critical{border-left-color:#dc2626}.finding.warning{border-left-color:#f59e0b}
.finding.info{border-left-color:#3b82f6}.finding.ok{border-left-color:#16a34a}
.finding h4{margin:0 0 6px;font-size:14.5px}
.finding .meta{font-size:12px;color:var(--mut);margin-bottom:4px}
.finding p{margin:5px 0}
details{margin-top:8px}
details summary{cursor:pointer;font-size:12.5px;color:var(--acc);user-select:none}
details pre{background:#0f172a;color:#e2e8f0;border-radius:8px;padding:12px 14px;overflow-x:auto;
 font-size:12px;line-height:1.5}
.cause{border:1px solid var(--line);border-radius:12px;background:var(--card);padding:14px 16px;margin:10px 0}
.cause.critical{border-left:5px solid #dc2626}.cause.warning{border-left:5px solid #f59e0b}
.cause.info{border-left:5px solid #3b82f6}.cause.ok{border-left:5px solid #16a34a}
.cause h4{margin:0 0 6px;font-size:15px}
.cause ul{margin:6px 0;padding-left:20px}
.evidence{font-size:12.5px;color:#475569;background:#f8fafc;border-radius:8px;padding:8px 12px;margin:6px 0}
.evidence li{margin:2px 0}
.chart{width:100%;height:auto;background:var(--card);border:1px solid var(--line);border-radius:10px}
.chart text{font-family:'Segoe UI',Roboto,Arial,sans-serif}
.gridline{stroke:#e2e8f0;stroke-width:1}
.axis{font-size:11px;fill:#94a3b8}
.qtext{max-width:520px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;
 font-family:Consolas,Menlo,monospace;font-size:11.5px;color:#334155}
.two{display:grid;grid-template-columns:1fr 1fr;gap:14px}
@media(max-width:900px){.two{grid-template-columns:1fr}}
.note{background:#eff6ff;border:1px solid #bfdbfe;border-radius:10px;padding:10px 14px;font-size:13px;color:#1e40af}
.warnbox{background:#fffbeb;border:1px solid #fde68a;border-radius:10px;padding:10px 14px;font-size:13px;color:#92400e}
.footer{color:var(--mut);font-size:12px;text-align:center;margin-top:40px}
.score-wrap{display:flex;gap:20px;align-items:center}
@media print{nav{display:none}body{background:#fff}.card,table{box-shadow:none}}
"""


def section(num, title, anchor, body):
    return "<h2 id='%s'><span class='num'>%s</span>%s</h2>%s" % (anchor, num, esc(title), body)


# ------------------------------------------------------------------ сборка
def build_report(snapshot: dict, history: Optional[dict], analysis: Analysis,
                 settings=None, out_path: str = None, title: str = None,
                 period_hours: float = 24) -> str:
    inst = snapshot.get('instance') or {}
    sysinfo = snapshot.get('sys') or {}
    st = analysis.stats
    demo = snapshot.get('source') == 'demo'
    samples = (history or {}).get('samples') or []
    period_h = ((samples[-1]['ts'] - samples[0]['ts']).total_seconds() / 3600
                if len(samples) > 1 else 0)

    title = title or ('Демонстрационный отчёт' if demo else 'Отчёт мониторинга') + \
        ' — SQL Server ' + str(inst.get('server_name') or '')

    # ------------------------------------------------------------- header
    kpi_cards = "".join("<div class='kpi'><div class='v'>%s</div><div class='l'>%s</div></div>" % (v, l)
                        for v, l in [
                            (str(analysis.health_score), 'Health score'),
                            (str(analysis.counts['critical']), 'Критично'),
                            (str(analysis.counts['warning']), 'Предупреждений'),
                            (str(analysis.counts['info']), 'Рекомендаций'),
                            (fnum(st['cpu_avg'], '—') + '%', 'ЦП SQL (среднее)'),
                            (fnum(st['batch_avg'], '—'), 'Batch/sec (среднее)'),
                        ])
    info_rows = "".join("<tr><td>%s</td><td>%s</td></tr>" % (esc(k), esc(v)) for k, v in [
        ('Сервер / экземпляр', inst.get('server_name') or '—'),
        ('Версия', '%s (%s %s)' % (inst.get('product_version') or '—', inst.get('product_level') or '',
                                   inst.get('edition') or '')),
        ('Время работы сервера', fdur(sysinfo.get('uptime_sec'))),
        ('Процессоры', '%s логических' % fnum(sysinfo.get('cpu_count')) if sysinfo.get('cpu_count') else '—'),
        ('Память', fsize_mb((sysinfo.get('physical_memory_kb') or 0) / 1024)),
        ('Отчёт сформирован', fdt(datetime.now())),
        ('Период наблюдений', ('%s — %s (%.1f ч, %d замеров)'
                               % (fdt(samples[0]['ts']), fdt(samples[-1]['ts']), period_h, len(samples)))
         if period_h else 'разовый снимок (суточный мониторинг не выполнялся)'),
        ('Режим', 'демо-данные (имитация)' if demo else 'реальный сервер'),
    ])
    header = """
<header class='rep'><div class='wrap'>
 <h1>%s</h1>
 <div class='sub'>Мониторинг производительности, ресурсы, ожидания, настройки — с рекомендациями</div>
 <div class='grid'>%s</div>
</div></header>""" % (esc(title), kpi_cards)

    gauge_html = SC.gauge(analysis.health_score)
    summary = """
<div class='card score-wrap'>
  <div>%s</div>
  <div style='flex:1'>
    <b>Итоговая оценка: %d из 100.</b> %s
    <table style='margin-top:10px'>%s</table>
  </div>
</div>""" % (gauge_html, analysis.health_score, esc(analysis.summary), info_rows)

    nav = """<nav><div class='wrap'><a href='#causes'>Причины замедлений</a>
<a href='#recs'>Рекомендации</a><a href='#charts'>Графики</a><a href='#waits'>Ожидания</a>
<a href='#queries'>Запросы</a><a href='#blocking'>Блокировки</a><a href='#resources'>Ресурсы</a>
<a href='#disks'>Диски</a><a href='#indexes'>Индексы</a><a href='#settings'>Настройки</a>
<a href='#method'>Методика</a></div></nav>"""

    parts = ["<!DOCTYPE html><html lang='ru'><head><meta charset='utf-8'>",
             "<meta name='viewport' content='width=device-width, initial-scale=1'>",
             "<title>%s</title><style>%s</style></head><body>" % (esc(title), CSS),
             header, nav, "<div class='wrap'>", summary]

    # ------------------------------------------------------- 1. причины
    causes_html = []
    for c in analysis.causes:
        ev = ''.join('<li>%s</li>' % esc(e) for e in c.evidence)
        fx = ''.join('<li>%s</li>' % esc(f) for f in c.fixes)
        causes_html.append(
            "<div class='cause %s'><h4>%s %s</h4>"
            "<div class='evidence'><b>Наблюдения:</b><ul>%s</ul></div>"
            "<p>%s</p><b>Что делать:</b><ul>%s</ul></div>"
            % (c.severity, badge(c.severity), esc(c.title), ev, esc(c.explanation), fx))
    if not period_h:
        causes_html.insert(0, "<div class='warnbox'><b>Внимание:</b> суточный мониторинг не выполнялся — "
                              "причины замедлений оценены по разовому снимку. Запустите суточный мониторинг "
                              "в программе, чтобы получить полный анализ за 24 часа.</div>")
    parts.append(section('1', 'Причины замедления работы', 'causes', ''.join(causes_html)))

    # ------------------------------------------------------- 2. рекомендации
    recs = []
    if not analysis.findings:
        recs.append("<div class='card'>Проблем не обнаружено. 🎉</div>")
    groups = {}
    for f in analysis.findings:
        groups.setdefault(f.severity, []).append(f)
    for sev in ('critical', 'warning', 'info'):
        for f in groups.get(sev, []):
            script = ("<details><summary>Показать SQL-скрипт исправления</summary><pre>%s</pre></details>"
                      % esc(f.script)) if f.script else ''
            recs.append(
                "<div class='finding %s'><h4>%s %s</h4><div class='meta'>Область: %s</div>"
                "<p><b>Проблема:</b> %s</p><p><b>Рекомендация:</b> %s</p>%s</div>"
                % (f.severity, badge(f.severity), esc(f.title), esc(f.area),
                   esc(f.problem or '—'), esc(f.recommendation or '—'), script))
    parts.append(section('2', 'Рекомендации по устранению и оптимизации', 'recs', ''.join(recs)))

    # ------------------------------------------------------- 3. графики
    charts = []
    if samples:
        ts = [s['ts'] for s in samples]
        xl = SC.time_labels(ts)

        def col(key):
            return [s.get(key) or 0 for s in samples]

        charts.append("<h3>Загрузка ЦП, %</h3>" + SC.area_chart(
            [{'name': 'ЦП SQL Server', 'color': '#dc2626', 'values': col('sql_cpu')},
             {'name': 'ЦП других процессов', 'color': '#f59e0b', 'values': col('other_cpu')}],
            xl, y_max=100))
        charts.append("<h3>Интенсивность запросов</h3>" + SC.line_chart(
            [{'name': 'Batch Requests/sec', 'color': '#1d4ed8', 'values': col('batch_sec')},
             {'name': 'Transactions/sec', 'color': '#9333ea', 'values': col('trans_sec')}], xl))
        charts.append("<h3>Ввод-вывод страниц</h3>" + SC.line_chart(
            [{'name': 'Page reads/sec', 'color': '#2563eb', 'values': col('reads_sec')},
             {'name': 'Page writes/sec', 'color': '#0891b2', 'values': col('writes_sec')}], xl))
        charts.append("<h3>Время жизни страниц в кэше (PLE)</h3>" + SC.line_chart(
            [{'name': 'Page Life Expectancy, сек', 'color': '#16a34a', 'values': col('ple')}], xl,
            thresholds=[{'value': 300, 'label': 'мин. норма 300', 'color': '#dc2626'}]))
        charts.append("<h3>Заблокированные процессы</h3>" + SC.line_chart(
            [{'name': 'Processes blocked', 'color': '#d97706', 'values': col('blocked')}], xl))
        # ожидания по категориям (в минутных корзинах)
        waits = (history or {}).get('waits') or []
        if waits:
            buckets = {}
            for w in waits:
                t = w['ts']
                if not t:
                    continue
                key = t.replace(second=0, microsecond=0)
                cat = categorize_wait(w['wait_type'])
                d = buckets.setdefault(key, {})
                d[cat] = d.get(cat, 0) + (w['wait_ms'] or 0)
            keys = sorted(buckets)
            top_cats = [c['key'] for c in analysis.wait_by_cat[:5]]
            layers = []
            for cat in top_cats:
                layers.append({'name': WAIT_CATS[cat]['title'], 'color': WAIT_CATS[cat]['color'],
                               'values': [buckets[k].get(cat, 0) / 60.0 for k in keys]})
            xl2 = SC.time_labels(keys)
            charts.append("<h3>Ожидания по категориям (мс/сек, стек)</h3>" +
                          SC.stacked_area(layers, xl2))
        charts_hint = ''
    else:
        charts.append("<div class='warnbox'>История мониторинга пуста. Запустите «Суточный мониторинг» "
                      "в программе — графики загрузки за 24 часа появятся в этом разделе.</div>")
    parts.append(section('3', 'Графики загрузки за период', 'charts', ''.join(charts)))

    # ------------------------------------------------------- 4. ожидания
    w_html = []
    if analysis.wait_by_cat:
        bars = [{'label': c['title'], 'value': c['pct'], 'color': c['color']}
                for c in analysis.wait_by_cat]
        w_html.append("<div class='card'><h3>Распределение ожиданий по категориям</h3>%s</div>"
                      % SC.hbars(bars, unit='%'))
        rows = []
        for c in analysis.wait_by_cat:
            info = WAIT_CATS.get(c['key'], {})
            rows.append(
                "<tr><td><span class='tag' style='background:%s22;color:%s'>●</span> <b>%s</b>"
                "<div style='font-size:12px;color:var(--mut)'>%s</div></td>"
                "<td class='r'>%.1f%%</td><td class='r'>%s мс</td></tr>"
                % (c['color'], c['color'], esc(c['title']), esc(info.get('why', '')),
                   c['pct'], fnum(c['ms'])))
        w_html.append("<table><tr><th>Категория</th><th class='r'>Доля</th>"
                      "<th class='r'>Суммарно за период</th></tr>%s</table>" % ''.join(rows))
        top_rows = ''.join(
            "<tr><td class='mono'>%s</td><td><span class='tag'>%s</span></td>"
            "<td class='r'>%s мс</td><td class='r'>%.1f%%</td></tr>"
            % (esc(w['wait_type']), esc(WAIT_CATS[w['cat']]['title']), fnum(w['ms']), w['pct'])
            for w in analysis.wait_top)
        w_html.append("<h3>Топ-15 типов ожиданий</h3><table><tr><th>Тип ожидания</th><th>Категория</th>"
                      "<th class='r'>Время</th><th class='r'>Доля</th></tr>%s</table>" % top_rows)
        fixes = ''.join("<div class='card'><h3>%s — что с этим делать</h3><ul>%s</ul></div>" %
                        (esc(c['title']), ''.join('<li>%s</li>' % esc(f) for f in WAIT_CATS[c['key']]['fixes']))
                        for c in analysis.wait_by_cat[:4] if WAIT_CATS.get(c['key']))
        w_html.append(fixes)
    else:
        w_html.append("<div class='card'>Статистика ожиданий за период недоступна "
                      "(нет истории мониторинга).</div>")
    parts.append(section('4', 'Анализ ожиданий (причины простоев)', 'waits', ''.join(w_html)))

    # ------------------------------------------------------- 5. запросы
    q_html = []
    tq = snapshot.get('top_queries') or []
    if tq:
        rows = []
        for q in tq:
            rows.append(
                "<tr><td>%s</td><td class='r'>%s</td><td class='r'>%s</td><td class='r'>%s</td>"
                "<td class='r'>%s</td><td>%s</td></tr>"
                % (esc(q.get('db_name') or '—'), fnum(q.get('execution_count')),
                   fms(q.get('avg_cpu_ms')), fms(q.get('avg_elapsed_ms')),
                   fnum(q.get('avg_logical_reads')), fdt(q.get('last_execution_time'))))
        q_html.append("<div class='note'>Статистика накопительная: с момента запуска SQL Server или "
                      "последней очистки кэша планов (sys.dm_exec_query_stats).</div>")
        q_html.append("<h3>Самые дорогие запросы по процессору</h3><table>"
                      "<tr><th>БД</th><th class='r'>Вызовов</th><th class='r'>ЦП (сред.)</th>"
                      "<th class='r'>Длит. (сред.)</th><th class='r'>Чтений (сред.)</th>"
                      "<th>Последний запуск</th></tr>%s</table>" % ''.join(rows))
        det = ''.join("<details><summary>Текст запроса: %s</summary><pre>%s</pre></details>"
                      % (esc((q.get('query_text') or '')[:100]), esc(q.get('query_text') or ''))
                      for q in tq[:10])
        q_html.append(det)
    slow = (history or {}).get('slow') or []
    if slow:
        rows = ''.join("<tr><td>%s</td><td>%s</td><td class='r'>%s</td><td class='r'>%s</td>"
                       "<td class='qtext mono'>%s</td></tr>"
                       % (fdt(s['ts']), esc(s.get('db_name') or ''), fms(s.get('elapsed_ms')),
                          fms(s.get('cpu_ms')), esc((s.get('text') or '')[:160]))
                       for s in slow[:30])
        q_html.append("<h3>Медленные запросы за период (выполнялись дольше %s мс)</h3><table>"
                      "<tr><th>Время</th><th>БД</th><th class='r'>Длительность</th>"
                      "<th class='r'>ЦП</th><th>Запрос</th></tr>%s</table>"
                      % (fnum((settings.slow_query_ms if settings else 10000), '10000'), rows))
    if not q_html:
        q_html.append("<div class='card'>Данные о запросах недоступны (требуются права VIEW SERVER STATE).</div>")
    parts.append(section('5', 'Производительность запросов', 'queries', ''.join(q_html)))

    # ------------------------------------------------------- 6. блокировки
    b_html = []
    blk = (history or {}).get('blocking') or []
    cur_blocked = (snapshot.get('blocking') or {})
    if blk:
        rows = ''.join(
            "<tr><td>%s</td><td class='r'>%s</td><td class='r'>%s</td><td>%s</td>"
            "<td class='qtext mono'>%s</td><td>%s</td></tr>"
            % (fdt(b['ts']), fnum(b.get('victims')), fms(b.get('max_wait_ms')),
               esc(b.get('head_login') or b.get('head_program') or '—'),
               esc((b.get('head_query') or '')[:140]),
               "<details><summary>Запрос жертвы</summary><pre>%s</pre></details>"
               % esc(b.get('victim_query') or '—'))
            for b in blk[:25])
        b_html.append("<p>За период зафиксировано <b>%d</b> ситуаций блокирования.</p>" % len(blk))
        b_html.append("<table><tr><th>Время</th><th class='r'>Жертв</th><th class='r'>Макс. ожидание</th>"
                      "<th>Кто блокирует</th><th>Блокирующий запрос</th><th>Запрос жертвы</th></tr>"
                      "%s</table>" % rows)
        b_html.append("<div class='note'>«Жертвы» — запросы, ожидающие блокировку. Логин/программа "
                      "указывают владельца блокирующей сессии; если запрос не показан — сессия удерживает "
                      "блокировку открытой транзакцией без активного запроса (проверьте приложение).</div>")
    elif cur_blocked.get('count'):
        b_html.append("<p>В момент снимка заблокировано процессов: <b>%d</b>.</p>" % cur_blocked['count'])
        rows = ''.join("<tr><td class='r'>%s</td><td class='r'>%s</td><td class='qtext mono'>%s</td></tr>"
                       % (fnum(c.get('victims')), fms(c.get('max_wait_ms')), esc((c.get('head_query') or '')[:160]))
                       for c in cur_blocked.get('chains') or [])
        b_html.append("<table><tr><th class='r'>Жертв</th><th class='r'>Макс. ожидание</th>"
                      "<th>Блокирующий запрос</th></tr>%s</table>" % rows)
    else:
        b_html.append("<div class='card'>✅ За период наблюдений цепочек блокирования не зафиксировано.</div>")
    parts.append(section('6', 'Блокировки', 'blocking', ''.join(b_html)))

    # ------------------------------------------------------- 7. ресурсы
    r_html = []
    points = snapshot.get('points') or {}
    os_mem = snapshot.get('os_mem') or {}
    mem_rows = "".join("<tr><td>%s</td><td class='r'>%s</td></tr>" % (esc(k), esc(v)) for k, v in [
        ('Целевая память SQL (target)', fsize_mb(points.get('target_mem_mb'))),
        ('Занято SQL Server (total)', fsize_mb(points.get('total_mem_mb'))),
        ('Служебные нужды (stolen)', fsize_mb(points.get('stolen_mem_mb'))),
        ('PLE сейчас / среднее за период', '%s / %s сек' % (fnum(points.get('ple')), fnum(st['ple_avg']))),
        ('Попаданий в буферный кэш', '%s%%' % fnum(points.get('buffer_hit_pct'))),
        ('Физическая память сервера', fsize_mb((sysinfo.get('physical_memory_kb') or 0) / 1024)),
        ('Свободно на уровне ОС', fsize_mb((os_mem.get('available_physical_kb') or 0) / 1024)),
        ('Состояние памяти ОС', esc(os_mem.get('system_memory_state_desc') or '—')),
    ])
    r_html.append("<div class='two'><div><h3>Память</h3><table>%s</table></div>" % mem_rows)
    cpu_rows = "".join("<tr><td>%s</td><td class='r'>%s</td></tr>" % (esc(k), esc(v)) for k, v in [
        ('Логических процессоров', fnum(sysinfo.get('cpu_count'))),
        ('Физических процессоров (сокетов)', fnum((sysinfo.get('cpu_count') or 0) /
                                                  (sysinfo.get('hyperthread_ratio') or 1))),
        ('ЦП SQL: сейчас / среднее / макс',
         '%s%% / %s%% / %s%%' % (fnum(points.get('_cpu_now', snapshot.get('cpu', {}).get('sql'))),
                                 fnum(st['cpu_avg']), fnum(st['cpu_max']))),
        ('Подключений (сейчас / среднее)',
         '%s / %s' % (fnum(points.get('connections')), fnum(
             sum(s.get('connections') or 0 for s in samples) / len(samples) if samples else None))),
        ('Batch Requests/sec: сейчас / среднее / макс',
         '%s / %s / %s' % (fnum(snapshot.get('rates', {}).get('batch_sec')), fnum(st['batch_avg']),
                           fnum(st['batch_max']))),
        ('Компиляции / сек (среднее)', fnum(sum(s.get('compiles_sec') or 0 for s in samples) /
                                            len(samples) if samples else None)),
    ])
    r_html.append("<div><h3>Процессор и сессии</h3><table>%s</table></div></div>" % cpu_rows)
    parts.append(section('7', 'Ресурсы: процессор и память', 'resources', ''.join(r_html)))

    # ------------------------------------------------------- 8. диски
    d_html = []
    files = snapshot.get('files') or []
    if files:
        mode_txt = ('Средние значения за окно мониторинга' if snapshot.get('files_mode') == 'window'
                    else 'Средние с момента запуска сервера (%s)' % fdur(sysinfo.get('uptime_sec')))
        d_html.append("<div class='note'>%s (sys.dm_io_virtual_file_stats).</div>" % mode_txt)
        rows = []
        for f in sorted(files, key=lambda x: -(x.get('write_latency_ms') or 0) -
                                              (x.get('read_latency_ms') or 0)):
            rl, wl = f.get('read_latency_ms') or 0, f.get('write_latency_ms') or 0
            limit = 15 if f.get('type_desc') == 'LOG' else 25
            flag = "<span class='tag red'>высокая</span>" if (rl > limit or wl > limit) else \
                   "<span class='tag green'>норма</span>"
            rows.append(
                "<tr><td>%s</td><td class='mono'>%s</td><td>%s</td><td class='r'>%s</td>"
                "<td class='r'>%s / %s</td><td class='r'>%s / %s</td><td class='r'>%s</td><td>%s</td></tr>"
                % (esc(f['db_name']), esc(f['file_name']),
                   'журнал' if f.get('type_desc') == 'LOG' else 'данные',
                   fsize_mb(f.get('size_mb')),
                   fnum(f.get('reads_sec')), fnum(f.get('writes_sec')),
                   fnum(f.get('read_latency_ms')), fnum(f.get('write_latency_ms')),
                   '%.1f' % ((f.get('mb_read_sec') or 0) + (f.get('mb_write_sec') or 0)),
                   flag))
        d_html.append("<table><tr><th>БД</th><th>Файл</th><th>Тип</th><th class='r'>Размер</th>"
                      "<th class='r'>Чтений/сек</th><th class='r'>Задержка чтения/записи, мс</th>"
                      "<th class='r'>МБ/сек</th><th>Оценка</th></tr>%s</table>" % ''.join(rows))
        d_html.append("<div class='note'>Ориентиры: журнал транзакций — задержка записи до 5–10 мс "
                      "(лучше < 5); файлы данных — чтение до 20–25 мс на HDD, до 5 мс на SSD.</div>")
    else:
        d_html.append("<div class='card'>Данные ввода-вывода недоступны.</div>")
    parts.append(section('8', 'Диски и файлы баз данных', 'disks', ''.join(d_html)))

    # ------------------------------------------------------- 9. индексы
    i_html = []
    mi = snapshot.get('missing_indexes') or []
    if mi:
        rows = ''.join(
            "<tr><td>%s</td><td class='mono'>%s</td><td class='r'>%s</td><td class='r'>%.0f%%</td>"
            "<td class='r'>%s</td><td><details><summary>CREATE INDEX</summary><pre>%s</pre></details></td></tr>"
            % (esc(m.get('db_name')), esc(m.get('table_ref')), fnum(m.get('user_seeks')),
               m.get('avg_user_impact') or 0, fnum(m.get('score')), esc(missing_index_script(m)))
            for m in mi)
        i_html.append("<h3>Недостающие индексы (по данным оптимизатора)</h3><table>"
                      "<tr><th>БД</th><th>Таблица</th><th class='r'>Поисков</th>"
                      "<th class='r'>Ожид. эффект</th><th class='r'>Приоритет</th><th>Скрипт</th></tr>"
                      "%s</table><div class='note'>Создавайте индексы по одному и проверяйте эффект: "
                      "каждый индекс замедляет вставки/обновления. Объединяйте похожие индексы.</div>"
                      % ''.join(rows))
    frag = snapshot.get('frag') or []
    if frag:
        rows = ''.join(
            "<tr><td>%s</td><td class='mono'>%s.%s</td><td class='mono'>%s</td><td class='r'>%.0f%%</td>"
            "<td class='r'>%s</td><td class='r'>%s</td></tr>"
            % (esc(f.get('db_name')), esc(f.get('schema_name')), esc(f.get('table_name')),
               esc(f.get('index_name')), f.get('avg_fragmentation_in_percent') or 0,
               fnum(f.get('page_count')), fsize_mb((f.get('page_count') or 0) * 8 / 1024))
            for f in frag)
        i_html.append("<h3>Фрагментированные индексы (≥ 30%%)</h3><table><tr><th>БД</th><th>Таблица</th>"
                      "<th>Индекс</th><th class='r'>Фрагментация</th><th class='r'>Страниц</th>"
                      "<th class='r'>Размер</th></tr>%s</table>" % rows)
    stale = snapshot.get('stale_stats') or []
    if stale:
        rows = ''.join(
            "<tr><td>%s</td><td class='mono'>%s.%s</td><td class='mono'>%s</td><td class='r'>%s</td>"
            "<td class='r'>%.0f%%</td><td>%s</td></tr>"
            % (esc(s.get('db_name')), esc(s.get('schema_name')), esc(s.get('table_name')),
               esc(s.get('stats_name')), fnum(s.get('rows')),
               100 * (s.get('modification_counter') or 0) / max(1, s.get('rows') or 1),
               fdt(s.get('last_updated')))
            for s in stale)
        i_html.append("<h3>Устаревшая статистика (изменено &gt; 20%% строк)</h3><table>"
                      "<tr><th>БД</th><th>Таблица</th><th>Статистика</th><th class='r'>Строк</th>"
                      "<th class='r'>Изменено</th><th>Обновлялась</th></tr>%s</table>" % rows)
    if not i_html:
        i_html.append("<div class='card'>✅ Проблем с индексами и статистикой не найдено "
                      "(или глубокий анализ отключён в настройках).</div>")
    parts.append(section('9', 'Индексы и статистика', 'indexes', ''.join(i_html)))

    # ------------------------------------------------------- 10. настройки
    s_html = []
    if analysis.config_audit:
        rows = ''.join(
            "<tr><td class='mono'>%s</td><td class='r'>%s</td><td class='r'>%s</td><td>%s</td></tr>"
            % (esc(a['param']), esc(a['current']), esc(a['rec']), STATUS_ICON.get(a['status'], ''))
            for a in analysis.config_audit)
        s_html.append("<h3>Аудит конфигурации экземпляра</h3><table><tr><th>Параметр</th>"
                      "<th class='r'>Текущее</th><th class='r'>Рекомендуется</th><th>Статус</th></tr>"
                      "%s</table>" % rows)
    dbs = snapshot.get('databases') or []
    if dbs:
        backups = {b['db_name']: b for b in (snapshot.get('backups') or [])}
        rows = []
        for d in dbs:
            name = d.get('name')
            b = backups.get(name) or {}
            system = name in ('master', 'model', 'msdb', 'tempdb')
            flags = []
            if d.get('is_auto_shrink_on'):
                flags.append("<span class='tag red'>auto_shrink</span>")
            if d.get('is_auto_close_on'):
                flags.append("<span class='tag red'>auto_close</span>")
            if (d.get('compatibility_level') or 150) <= 110:
                flags.append("<span class='tag amber'>compat %s</span>" % d.get('compatibility_level'))
            if (d.get('page_verify_option_desc') or '').upper() != 'CHECKSUM':
                flags.append("<span class='tag amber'>verify</span>")
            if (d.get('state_desc') or 'ONLINE') != 'ONLINE':
                flags.append("<span class='tag red'>%s</span>" % d.get('state_desc'))
            if d.get('log_reuse_wait_desc') not in ('NOTHING', None) and not system:
                flags.append("<span class='tag amber'>log: %s</span>" % d.get('log_reuse_wait_desc'))
            rows.append(
                "<tr><td>%s%s</td><td>%s</td><td class='r'>%s</td><td class='r'>%s</td>"
                "<td class='r'>%s</td><td>%s</td></tr>"
                % (esc(name), ' <span class="tag">системная</span>' if system else '',
                   esc(d.get('recovery_model_desc') or ''),
                   esc(d.get('compatibility_level') or ''),
                   fdt(b.get('last_full')), fdt(b.get('last_log')),
                   ' '.join(flags) or '<span class="tag green">ok</span>'))
        s_html.append("<h3>Базы данных</h3><table><tr><th>База</th><th>Модель восст.</th>"
                      "<th class='r'>Compat</th><th class='r'>Посл. full</th><th class='r'>Посл. log</th>"
                      "<th>Флаги</th></tr>%s</table>" % ''.join(rows))
    cfg = snapshot.get('config') or {}
    if cfg:
        rows = ''.join("<tr><td class='mono'>%s</td><td class='r mono'>%s</td></tr>" % (esc(k), esc(v))
                       for k, v in sorted(cfg.items()))
        s_html.append("<details><summary>Полный список настроек сервера (sys.configurations)</summary>"
                      "<table><tr><th>Параметр</th><th class='r'>Значение</th></tr>%s</table></details>" % rows)
    parts.append(section('10', 'Настройки сервера и баз данных', 'settings', ''.join(s_html)))

    # ------------------------------------------------------- 11. методика
    method = """
<div class='card'>
<p><b>Как формируется отчёт.</b> Данные собираются из системных динамических представлений SQL Server:
sys.dm_os_performance_counters (счётчики), sys.dm_os_wait_stats (ожидания), sys.dm_exec_requests /
sys.dm_exec_sessions (активные запросы и блокировки), sys.dm_io_virtual_file_stats (ввод-вывод файлов),
sys.dm_os_sys_info и sys.dm_os_sys_memory (ЦП и память), sys.configurations (настройки),
sys.databases и msdb.dbo.backupset (базы и копии), sys.dm_exec_query_stats (топ запросов),
sys.dm_db_missing_index_* (недостающие индексы), sys.dm_db_index_physical_stats (фрагментация).</p>
<p><b>Методология.</b> Анализ причин замедления основан на методологии ожиданий (waits-and-queues):
нерабочие ожидания отфильтрованы, остальные сгруппированы по категориям (ЦП, память, диск, блокировки,
сеть), и по их долям в сочетании со счётчиками (PLE, ЦП, задержки файлов) определяются узкие места.</p>
<p><b>Важно.</b> Рекомендации — отправная точка для оптимизации. Перед применением скриптов
(особенно CREATE INDEX, ALTER DATABASE, sp_configure) проверяйте их на тестовой среде и учитывайте
особенности вашей нагрузки. Для полного мониторинга учётной записи нужны права VIEW SERVER STATE
и чтение msdb.</p>
<p><b>Суточный мониторинг.</b> Программа опрашивает сервер каждые 30 секунд (настраивается) и хранит
историю до 7 дней; отчёт можно сформировать в любой момент — графики строятся за последние 24 часа.</p>
</div>"""
    parts.append(section('11', 'Методика и ограничения', 'method', method))

    parts.append("<div class='footer'>Сформировано %s · MS SQL Monitor %s</div>"
                 % (fdt(datetime.now()), _version()))
    parts.append("</div></body></html>")
    html_doc = ''.join(parts)

    if out_path:
        os.makedirs(os.path.dirname(os.path.abspath(out_path)) or '.', exist_ok=True)
        with open(out_path, 'w', encoding='utf-8') as f:
            f.write(html_doc)
    return html_doc


def _version():
    try:
        from . import __version__
        return __version__
    except Exception:
        return ''
