package main

// Формирование подробного HTML-отчёта (самодостаточный файл: inline CSS + SVG).

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const reportCSS = `
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
@media print{nav{display:none}body{background:#fff}.card,table{box-shadow:none}}
`

var sevClass = map[string]string{"critical": "c-crit", "warning": "c-warn", "info": "c-info", "ok": "c-ok"}
var statusIcon = map[string]string{"bad": "⛔", "warn": "⚠️", "ok": "✅"}

func badgeHTML(sev string) string {
	return fmt.Sprintf("<span class='badge %s'>%s</span>", sevClass[sev], sevRu[sev])
}

// perDBResources — сводка «сколько какая база потребляет»: память (буферный
// кэш), ЦП (накопительно из кэша планов) и диск (агрегат по файлам базы).
func perDBResources(snap *Snapshot) string {
	type dbRes struct {
		MemMB, CPUms, Exec           float64
		Reads, Writes, MBps          float64
		latRsum, latWsum, opsR, opsW float64
	}
	dbs := map[string]*dbRes{}
	get := func(name string) *dbRes {
		name = orDash(name)
		if d, ok := dbs[name]; ok {
			return d
		}
		d := &dbRes{}
		dbs[name] = d
		return d
	}
	totMem, totCPU := 0.0, 0.0
	for _, m := range snap.PerDBMem {
		mb := toF(m["cached_mb"])
		if mb <= 0 {
			continue
		}
		get(sstr(m["db_name"])).MemMB = mb
		totMem += mb
	}
	for _, c := range snap.PerDBCPU {
		ms := toF(c["cpu_ms"])
		if ms <= 0 {
			continue
		}
		d := get(sstr(c["db_name"]))
		d.CPUms = ms
		d.Exec = toF(c["exec_count"])
		totCPU += ms
	}
	for _, f := range snap.Files {
		d := get(f.DBName)
		d.Reads += f.ReadsSec
		d.Writes += f.WritesSec
		d.MBps += f.MBReadSec + f.MBWriteSec
		if f.ReadsSec > 0 {
			d.latRsum += f.ReadLatencyMS * f.ReadsSec
			d.opsR += f.ReadsSec
		}
		if f.WritesSec > 0 {
			d.latWsum += f.WriteLatencyMS * f.WritesSec
			d.opsW += f.WritesSec
		}
	}
	if len(dbs) == 0 {
		return "<div class='warnbox'>Потребление ресурсов по базам данных недоступно " +
			"(требуются права VIEW SERVER STATE).</div>"
	}
	names := make([]string, 0, len(dbs))
	for n := range dbs {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return dbs[names[i]].MemMB > dbs[names[j]].MemMB })

	// диаграммы: память и ЦП (топ-8)
	memBars, cpuBars := []BarItem{}, []BarItem{}
	for i, n := range names {
		if i >= 8 {
			break
		}
		d := dbs[n]
		if d.MemMB > 0 {
			memBars = append(memBars, BarItem{n, d.MemMB, "#2563eb"})
		}
		if d.CPUms > 0 && totCPU > 0 {
			cpuBars = append(cpuBars, BarItem{n, d.CPUms / totCPU * 100, "#dc2626"})
		}
	}
	out := "<h3 style='margin-top:18px'>Потребление ресурсов по базам данных</h3>"
	if len(memBars) > 0 || len(cpuBars) > 0 {
		out += "<div class='two'><div><h3 style='font-size:14px'>Память (буферный кэш), МБ</h3>" +
			hBars(memBars, "МБ", 430, 0) + "</div>" +
			"<div><h3 style='font-size:14px'>ЦП (по кэшу планов), % от суммы</h3>" +
			hBars(cpuBars, "%", 430, 0) + "</div></div>"
	}
	rows := ""
	for i, n := range names {
		if i >= 12 {
			break
		}
		d := dbs[n]
		memTxt, memPct := "—", "—"
		if d.MemMB > 0 {
			memTxt = fmtSizeMB(d.MemMB)
			if totMem > 0 {
				memPct = fmt.Sprintf("%.1f%%", d.MemMB/totMem*100)
			}
		}
		cpuTxt := "—"
		if d.CPUms > 0 {
			cpuTxt = fmt.Sprintf("%.1f%%", d.CPUms/mathMaxF(1, totCPU)*100)
		}
		execs := "—"
		if d.Exec > 0 {
			execs = fmtNum(d.Exec)
		}
		latR, latW := "—", "—"
		if d.opsR > 0 {
			latR = fmtNum(d.latRsum / d.opsR)
		}
		if d.opsW > 0 {
			latW = fmtNum(d.latWsum / d.opsW)
		}
		rows += fmt.Sprintf("<tr><td>%s</td><td class='r'>%s</td><td class='r'>%s</td><td class='r'>%s</td>"+
			"<td class='r'>%s</td><td class='r'>%s / %s</td><td class='r'>%.1f</td><td class='r'>%s / %s</td></tr>",
			esc(n), memTxt, memPct, cpuTxt, execs,
			fmtNum(d.Reads), fmtNum(d.Writes), d.MBps, latR, latW)
	}
	out += "<table><tr><th>База</th><th class='r'>Память в кэше</th><th class='r'>Доля кэша</th>" +
		"<th class='r'>ЦП (доля)</th><th class='r'>Выполнений</th><th class='r'>Чтений / записей в сек</th>" +
		"<th class='r'>МБ/сек</th><th class='r'>Задержка чтения / записи, мс</th></tr>" + rows + "</table>"
	out += "<div class='note'>Методика: память — буферный кэш по sys.dm_os_buffer_descriptors " +
		"(основная часть памяти SQL Server; план-кэш и служебные структуры по базам не разносятся). " +
		"ЦП — накопительное время ядра по кэшу планов sys.dm_exec_query_stats с момента запуска сервера " +
		"(планы, вытесненные из кэша, не учитываются — доли ориентировочные). " +
		"Диск — агрегат по файлам базы из раздела «Диски и файлы».</div>"
	return out
}

func sectionHTML(num, title, anchor, body string) string {
	return fmt.Sprintf("<h2 id='%s'><span class='num'>%s</span>%s</h2>%s", anchor, num, esc(title), body)
}

func detailsBlock(summary, pre string) string {
	return fmt.Sprintf("<details><summary>%s</summary><pre>%s</pre></details>", esc(summary), esc(pre))
}

// BuildReport формирует отчёт и записывает его в outPath (если не пусто).
func BuildReport(snap *Snapshot, hist *History, an *Analysis, s Settings, outPath, title string) (string, error) {
	st := an.Stats
	demo := snap.Source == "demo"
	samples := hist.Samples
	periodH := 0.0
	if len(samples) > 1 {
		periodH = samples[len(samples)-1].TS.Sub(samples[0].TS).Hours()
	}
	if title == "" {
		title = "Отчёт мониторинга — SQL Server " + snap.Instance["server_name"]
	}

	var b strings.Builder

	// ------------------------------------------------------------- header
	kpis := [][2]string{
		{fmt.Sprintf("%d", an.HealthScore), "Health score"},
		{fmt.Sprintf("%d", countSev(an, "critical")), "Критично"},
		{fmt.Sprintf("%d", countSev(an, "warning")), "Предупреждений"},
		{fmt.Sprintf("%d", countSev(an, "info")), "Рекомендаций"},
		{fmtNum(st.CPUAvg) + "%", "ЦП SQL (среднее)"},
		{fmtNum(st.BatchAvg), "Batch/sec (среднее)"},
	}
	kpiHTML := ""
	for _, k := range kpis {
		kpiHTML += fmt.Sprintf("<div class='kpi'><div class='v'>%s</div><div class='l'>%s</div></div>", k[0], k[1])
	}

	periodStr := "разовый снимок (суточный мониторинг не выполнялся)"
	if periodH > 0 {
		periodStr = fmt.Sprintf("%s — %s (%.1f ч, %d замеров)",
			fmtDT(samples[0].TS), fmtDT(samples[len(samples)-1].TS), periodH, len(samples))
	}
	infoRows := [][2]string{
		{"Сервер / экземпляр", orDash(snap.Instance["server_name"])},
		{"Версия", fmt.Sprintf("%s (%s %s)", orDash(snap.Instance["product_version"]),
			snap.Instance["product_level"], snap.Instance["edition"])},
		{"Время работы сервера", fmtDur(toI(snap.SysInfo["uptime_sec"]))},
		{"Процессоры", fmt.Sprintf("%s логических", fmtNum(toF(snap.SysInfo["cpu_count"])))},
		{"Память", fmtSizeMB(toF(snap.SysInfo["physical_memory_kb"]) / 1024)},
		{"Отчёт сформирован", fmtDT(time.Now())},
		{"Период наблюдений", periodStr},
		{"Режим", map[bool]string{true: "демо-данные (имитация)", false: "реальный сервер"}[demo]},
	}
	infoHTML := ""
	for _, r := range infoRows {
		infoHTML += fmt.Sprintf("<tr><td>%s</td><td>%s</td></tr>", esc(r[0]), esc(r[1]))
	}

	b.WriteString(fmt.Sprintf(`<!DOCTYPE html><html lang='ru'><head><meta charset='utf-8'>
<meta name='viewport' content='width=device-width, initial-scale=1'>
<title>%s</title><style>%s</style></head><body>
<header class='rep'><div class='wrap'>
<h1>%s</h1>
<div class='sub'>Мониторинг производительности, ресурсы, ожидания, настройки — с рекомендациями</div>
<div class='grid'>%s</div>
</div></header>
<nav><div class='wrap'><a href='#causes'>Причины замедлений</a>
<a href='#recs'>Рекомендации</a><a href='#charts'>Графики</a><a href='#waits'>Ожидания</a>
<a href='#queries'>Запросы</a><a href='#blocking'>Блокировки</a><a href='#resources'>Ресурсы</a>
<a href='#disks'>Диски</a><a href='#indexes'>Индексы</a><a href='#settings'>Настройки</a>
<a href='#method'>Методика</a></div></nav>
<div class='wrap'>
<div class='card score-wrap'>
  <div>%s</div>
  <div style='flex:1'>
    <b>Итоговая оценка: %d из 100.</b> %s
    <table style='margin-top:10px'>%s</table>
  </div>
</div>`,
		esc(title), reportCSS, esc(title), kpiHTML, gaugeSVG(an.HealthScore),
		an.HealthScore, esc(an.Summary), infoHTML))

	// ------------------------------------------------------- 1. причины
	var causes strings.Builder
	if periodH <= 0 {
		causes.WriteString("<div class='warnbox'><b>Внимание:</b> суточный мониторинг не выполнялся — " +
			"причины замедлений оценены по разовому снимку. Запустите суточный мониторинг в программе, " +
			"чтобы получить полный анализ за 24 часа.</div>")
	}
	for _, c := range an.Causes {
		ev := ""
		for _, e := range c.Evidence {
			ev += "<li>" + esc(e) + "</li>"
		}
		fx := ""
		for _, f := range c.Fixes {
			fx += "<li>" + esc(f) + "</li>"
		}
		causes.WriteString(fmt.Sprintf(
			"<div class='cause %s'><h4>%s %s</h4><div class='evidence'><b>Наблюдения:</b><ul>%s</ul></div>"+
				"<p>%s</p><b>Что делать:</b><ul>%s</ul></div>",
			c.Severity, badgeHTML(c.Severity), esc(c.Title), ev, esc(c.Explanation), fx))
	}
	b.WriteString(sectionHTML("1", "Причины замедления работы", "causes", causes.String()))

	// ------------------------------------------------------- 2. рекомендации
	var recs strings.Builder
	if len(an.Findings) == 0 {
		recs.WriteString("<div class='card'>Проблем не обнаружено. 🎉</div>")
	}
	for _, f := range an.Findings {
		script := ""
		if f.Script != "" {
			script = detailsBlock("Показать SQL-скрипт исправления", f.Script)
		}
		recs.WriteString(fmt.Sprintf(
			"<div class='finding %s'><h4>%s %s</h4><div class='meta'>Область: %s</div>"+
				"<p><b>Проблема:</b> %s</p><p><b>Рекомендация:</b> %s</p>%s</div>",
			f.Severity, badgeHTML(f.Severity), esc(f.Title), esc(f.Area),
			esc(orDash(f.Problem)), esc(orDash(f.Recommendation)), script))
	}
	b.WriteString(sectionHTML("2", "Рекомендации по устранению и оптимизации", "recs", recs.String()))

	// ------------------------------------------------------- 3. графики
	var charts strings.Builder
	if len(samples) > 0 {
		ts := make([]time.Time, len(samples))
		col := func(get func(SampleRow) float64) []float64 {
			out := make([]float64, len(samples))
			for i, s := range samples {
				out[i] = get(s)
			}
			return out
		}
		for i, s := range samples {
			ts[i] = s.TS
		}
		xl := timeLabels(ts)
		charts.WriteString("<h3>Загрузка ЦП, %</h3>" + areaChart([]Series{
			{"ЦП SQL Server", "#dc2626", col(func(s SampleRow) float64 { return s.SQLCPU }), false},
			{"ЦП других процессов", "#f59e0b", col(func(s SampleRow) float64 { return s.Other }), false},
		}, xl, 860, 240, 100))
		charts.WriteString("<h3>Интенсивность запросов</h3>" + lineChart([]Series{
			{"Batch Requests/sec", "#1d4ed8", col(func(s SampleRow) float64 { return s.Batch }), false},
			{"Transactions/sec", "#9333ea", col(func(s SampleRow) float64 { return s.Trans }), false},
		}, xl, 860, 240, 0, nil))
		charts.WriteString("<h3>Ввод-вывод страниц</h3>" + lineChart([]Series{
			{"Page reads/sec", "#2563eb", col(func(s SampleRow) float64 { return s.Reads }), false},
			{"Page writes/sec", "#0891b2", col(func(s SampleRow) float64 { return s.Writes }), false},
		}, xl, 860, 240, 0, nil))
		charts.WriteString("<h3>Время жизни страниц в кэше (PLE)</h3>" + lineChart([]Series{
			{"Page Life Expectancy, сек", "#16a34a", col(func(s SampleRow) float64 { return s.PLE }), false},
		}, xl, 860, 240, 0, []Threshold{{Value: 300, Color: "#dc2626", Label: "мин. норма 300"}}))
		charts.WriteString("<h3>Заблокированные процессы</h3>" + lineChart([]Series{
			{"Processes blocked", "#d97706", col(func(s SampleRow) float64 { return s.Blocked }), false},
		}, xl, 860, 240, 0, nil))

		// ожидания по категориям (минутные корзины)
		if len(hist.Waits) > 0 {
			buckets := map[time.Time]map[string]float64{}
			for _, w := range hist.Waits {
				if w.TS.IsZero() {
					continue
				}
				key := w.TS.Truncate(time.Minute)
				m := buckets[key]
				if m == nil {
					m = map[string]float64{}
					buckets[key] = m
				}
				m[categorizeWait(w.Type)] += w.WaitMS
			}
			keys := make([]time.Time, 0, len(buckets))
			for k := range buckets {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })
			topCats := make([]string, 0, 5)
			for _, c := range an.WaitByCat {
				if len(topCats) < 5 {
					topCats = append(topCats, c.Key)
				}
			}
			layers := []Series{}
			for _, cat := range topCats {
				vals := make([]float64, len(keys))
				for i, k := range keys {
					if v, ok := buckets[k][cat]; ok {
						vals[i] = v / 60.0
					}
				}
				layers = append(layers, Series{waitCats[cat].Title, waitCats[cat].Color, vals, false})
			}
			charts.WriteString("<h3>Ожидания по категориям (мс/сек, стек)</h3>" +
				stackedArea(layers, timeLabels(keys), 860, 240, 0))
		}
	} else {
		charts.WriteString("<div class='warnbox'>История мониторинга пуста. Запустите «Суточный мониторинг» " +
			"в программе — графики загрузки за 24 часа появятся в этом разделе.</div>")
	}
	b.WriteString(sectionHTML("3", "Графики загрузки за период", "charts", charts.String()))

	// ------------------------------------------------------- 4. ожидания
	var w strings.Builder
	if len(an.WaitByCat) > 0 {
		bars := []BarItem{}
		for _, c := range an.WaitByCat {
			bars = append(bars, BarItem{c.Title, c.Pct, c.Color})
		}
		w.WriteString("<div class='card'><h3>Распределение ожиданий по категориям</h3>" +
			hBars(bars, "%", 560, 0) + "</div>")
		rows := ""
		for _, c := range an.WaitByCat {
			info := waitCats[c.Key]
			rows += fmt.Sprintf(
				"<tr><td><span class='tag' style='background:%s22;color:%s'>●</span> <b>%s</b>"+
					"<div style='font-size:12px;color:var(--mut)'>%s</div></td><td class='r'>%.1f%%</td>"+
					"<td class='r'>%s мс</td></tr>",
				c.Color, c.Color, esc(c.Title), esc(info.Why), c.Pct, fmtNum(c.MS))
		}
		w.WriteString("<table><tr><th>Категория</th><th class='r'>Доля</th>" +
			"<th class='r'>Суммарно за период</th></tr>" + rows + "</table>")
		topRows := ""
		for _, t := range an.WaitTop {
			topRows += fmt.Sprintf("<tr><td class='mono'>%s</td><td><span class='tag'>%s</span></td>"+
				"<td class='r'>%s мс</td><td class='r'>%.1f%%</td></tr>",
				esc(t.Type), esc(waitCats[t.Cat].Title), fmtNum(t.MS), t.Pct)
		}
		w.WriteString("<h3>Топ-15 типов ожиданий</h3><table><tr><th>Тип ожидания</th><th>Категория</th>" +
			"<th class='r'>Время</th><th class='r'>Доля</th></tr>" + topRows + "</table>")
		for i, c := range an.WaitByCat {
			if i >= 4 {
				break
			}
			fx := ""
			for _, f := range waitCats[c.Key].Fixes {
				fx += "<li>" + esc(f) + "</li>"
			}
			w.WriteString(fmt.Sprintf("<div class='card'><h3>%s — что с этим делать</h3><ul>%s</ul></div>",
				esc(c.Title), fx))
		}
	} else {
		w.WriteString("<div class='card'>Статистика ожиданий за период недоступна (нет истории мониторинга).</div>")
	}
	b.WriteString(sectionHTML("4", "Анализ ожиданий (причины простоев)", "waits", w.String()))

	// ------------------------------------------------------- 5. запросы
	var q strings.Builder
	if len(snap.TopQueries) > 0 {
		q.WriteString("<div class='note'>Статистика накопительная: с момента запуска SQL Server или " +
			"последней очистки кэша планов (sys.dm_exec_query_stats).</div>")
		rows := ""
		for _, t := range snap.TopQueries {
			rows += fmt.Sprintf("<tr><td>%s</td><td class='r'>%s</td><td class='r'>%s</td><td class='r'>%s</td>"+
				"<td class='r'>%s</td><td>%s</td></tr>",
				esc(orDash(sstr(t["db_name"]))), fmtNum(toF(t["execution_count"])),
				fmtMS(toF(t["avg_cpu_ms"])), fmtMS(toF(t["avg_elapsed_ms"])),
				fmtNum(toF(t["avg_logical_reads"])), fmtDT(toTime(t["last_execution_time"])))
		}
		q.WriteString("<h3>Самые дорогие запросы по процессору</h3><table>" +
			"<tr><th>БД</th><th class='r'>Вызовов</th><th class='r'>ЦП (сред.)</th>" +
			"<th class='r'>Длит. (сред.)</th><th class='r'>Чтений (сред.)</th><th>Последний запуск</th></tr>" +
			rows + "</table>")
		for i, t := range snap.TopQueries {
			if i >= 10 {
				break
			}
			q.WriteString(detailsBlock("Текст запроса: "+truncStr(sstr(t["query_text"]), 100), sstr(t["query_text"])))
		}
	}
	if len(hist.Slow) > 0 {
		rows := ""
		for i, s := range hist.Slow {
			if i >= 30 {
				break
			}
			rows += fmt.Sprintf("<tr><td>%s</td><td>%s</td><td class='r'>%s</td><td class='r'>%s</td>"+
				"<td class='qtext mono'>%s</td></tr>",
				fmtDT(s.TS), esc(s.DBName), fmtMS(s.ElapsedMS), fmtMS(s.CPUMS), esc(truncStr(s.Text, 160)))
		}
		q.WriteString(fmt.Sprintf("<h3>Медленные запросы за период (выполнялись дольше %s мс)</h3>"+
			"<table><tr><th>Время</th><th>БД</th><th class='r'>Длительность</th><th class='r'>ЦП</th>"+
			"<th>Запрос</th></tr>%s</table>", fmtNum(float64(s.SlowQueryMS)), rows))
	}
	if q.Len() == 0 {
		q.WriteString("<div class='card'>Данные о запросах недоступны (требуются права VIEW SERVER STATE).</div>")
	}
	b.WriteString(sectionHTML("5", "Производительность запросов", "queries", q.String()))

	// ------------------------------------------------------- 6. блокировки
	var blk strings.Builder
	if len(hist.Blocking) > 0 {
		blk.WriteString(fmt.Sprintf("<p>За период зафиксировано <b>%d</b> ситуаций блокирования.</p>", len(hist.Blocking)))
		rows := ""
		for i, e := range hist.Blocking {
			if i >= 25 {
				break
			}
			who := e.HeadLogin
			if who == "" {
				who = e.HeadProgram
			}
			if who == "" {
				who = "—"
			}
			rows += fmt.Sprintf("<tr><td>%s</td><td class='r'>%d</td><td class='r'>%s</td><td>%s</td>"+
				"<td class='qtext mono'>%s</td><td>%s</td></tr>",
				fmtDT(e.TS), e.Victims, fmtMS(e.MaxWaitMS), esc(who), esc(truncStr(e.HeadQuery, 140)),
				detailsBlock("Запрос жертвы", orDash(e.VictimQuery)))
		}
		blk.WriteString("<table><tr><th>Время</th><th class='r'>Жертв</th><th class='r'>Макс. ожидание</th>" +
			"<th>Кто блокирует</th><th>Блокирующий запрос</th><th>Запрос жертвы</th></tr>" + rows + "</table>")
		blk.WriteString("<div class='note'>«Жертвы» — запросы, ожидающие блокировку. Логин/программа указывают " +
			"владельца блокирующей сессии; если запрос не показан — сессия удерживает блокировку открытой " +
			"транзакцией без активного запроса (проверьте приложение).</div>")
	} else if snap.Blocking.Count > 0 {
		blk.WriteString(fmt.Sprintf("<p>В момент снимка заблокировано процессов: <b>%d</b>.</p>", snap.Blocking.Count))
		rows := ""
		for _, c := range snap.Blocking.Chains {
			rows += fmt.Sprintf("<tr><td class='r'>%d</td><td class='r'>%s</td><td class='qtext mono'>%s</td></tr>",
				c.Victims, fmtMS(float64(c.MaxWaitMS)), esc(truncStr(c.HeadQuery, 160)))
		}
		blk.WriteString("<table><tr><th class='r'>Жертв</th><th class='r'>Макс. ожидание</th>" +
			"<th>Блокирующий запрос</th></tr>" + rows + "</table>")
	} else {
		blk.WriteString("<div class='card'>✅ За период наблюдений цепочек блокирования не зафиксировано.</div>")
	}
	b.WriteString(sectionHTML("6", "Блокировки", "blocking", blk.String()))

	// ------------------------------------------------------- 7. ресурсы
	p := snap.Points
	var r strings.Builder
	memRows := [][2]string{
		{"Целевая память SQL (target)", fmtSizeMB(ptrVal(p.TargetMemMB))},
		{"Занято SQL Server (total)", fmtSizeMB(ptrVal(p.TotalMemMB))},
		{"Служебные нужды (stolen)", fmtSizeMB(ptrVal(p.StolenMemMB))},
		{"PLE сейчас / среднее за период", fmt.Sprintf("%s / %s сек", fmtNumD(p.PLE, "—"), fmtNum(st.PLEAvg))},
		{"Попаданий в буферный кэш", fmtNumD(p.BufferHitPct, "—") + "%"},
		{"Физическая память сервера", fmtSizeMB(toF(snap.SysInfo["physical_memory_kb"]) / 1024)},
		{"Свободно на уровне ОС", fmtSizeMB(toF(snap.OSMem["available_physical_kb"]) / 1024)},
		{"Состояние памяти ОС", orDash(sstr(snap.OSMem["system_memory_state_desc"]))},
	}
	memHTML := ""
	for _, row := range memRows {
		memHTML += fmt.Sprintf("<tr><td>%s</td><td class='r'>%s</td></tr>", esc(row[0]), esc(row[1]))
	}
	ht := toI(snap.SysInfo["hyperthread_ratio"])
	if ht <= 0 {
		ht = 1
	}
	avgConn := 0.0
	if len(samples) > 0 {
		for _, s := range samples {
			avgConn += s.Connections
		}
		avgConn /= float64(len(samples))
	}
	cpuRows := [][2]string{
		{"Логических процессоров", fmtNum(toF(snap.SysInfo["cpu_count"]))},
		{"Физических процессоров (сокетов)", fmtNum(float64(toI(snap.SysInfo["cpu_count"]) / ht))},
		{"ЦП SQL: сейчас / среднее / макс", fmt.Sprintf("%s%% / %s%% / %s%%",
			fmtNum(snap.CPU.SQL), fmtNum(st.CPUAvg), fmtNum(st.CPUMax))},
		{"Подключений (сейчас / среднее)", fmt.Sprintf("%s / %s", fmtNumD(p.Connections, "—"), fmtNum(avgConn))},
		{"Batch Requests/sec: сейчас / среднее / макс", fmt.Sprintf("%s / %s / %s",
			fmtNum(snap.Rates["batch_sec"]), fmtNum(st.BatchAvg), fmtNum(st.BatchMax))},
		{"Компиляции / сек (среднее)", fmtNum(avgOf(samples, func(s SampleRow) float64 { return s.Compiles }))},
	}
	cpuHTML := ""
	for _, row := range cpuRows {
		cpuHTML += fmt.Sprintf("<tr><td>%s</td><td class='r'>%s</td></tr>", esc(row[0]), esc(row[1]))
	}
	r.WriteString("<div class='two'><div><h3>Память</h3><table>" + memHTML + "</table></div>" +
		"<div><h3>Процессор и сессии</h3><table>" + cpuHTML + "</table></div></div>")

	// --- потребление ресурсов по базам данных ---
	r.WriteString(perDBResources(snap))
	b.WriteString(sectionHTML("7", "Ресурсы: процессор и память", "resources", r.String()))

	// ------------------------------------------------------- 8. диски
	var d strings.Builder
	if len(snap.Files) > 0 {
		modeTxt := "Средние с момента запуска сервера (" + fmtDur(toI(snap.SysInfo["uptime_sec"])) + ")"
		if snap.FilesMode == "window" {
			modeTxt = "Средние значения за окно мониторинга"
		}
		d.WriteString("<div class='note'>" + modeTxt + " (sys.dm_io_virtual_file_stats).</div>")
		sorted := make([]FileStat, len(snap.Files))
		copy(sorted, snap.Files)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].WriteLatencyMS+sorted[i].ReadLatencyMS > sorted[j].WriteLatencyMS+sorted[j].ReadLatencyMS
		})
		rows := ""
		for _, f := range sorted {
			limit := 25.0
			if f.TypeDesc == "LOG" {
				limit = 15
			}
			flag := "<span class='tag green'>норма</span>"
			if f.ReadLatencyMS > limit || f.WriteLatencyMS > limit {
				flag = "<span class='tag red'>высокая</span>"
			}
			typ := "данные"
			if f.TypeDesc == "LOG" {
				typ = "журнал"
			}
			rows += fmt.Sprintf("<tr><td>%s</td><td class='mono'>%s</td><td>%s</td><td class='r'>%s</td>"+
				"<td class='r'>%s / %s</td><td class='r'>%s / %s</td><td class='r'>%.1f</td><td>%s</td></tr>",
				esc(f.DBName), esc(f.FileName), typ, fmtSizeMB(f.SizeMB),
				fmtNum(f.ReadsSec), fmtNum(f.WritesSec),
				fmtNum(f.ReadLatencyMS), fmtNum(f.WriteLatencyMS),
				f.MBReadSec+f.MBWriteSec, flag)
		}
		d.WriteString("<table><tr><th>БД</th><th>Файл</th><th>Тип</th><th class='r'>Размер</th>" +
			"<th class='r'>Чтений/записей в сек</th><th class='r'>Задержка чтения/записи, мс</th>" +
			"<th class='r'>МБ/сек</th><th>Оценка</th></tr>" + rows + "</table>")
		d.WriteString("<div class='note'>Ориентиры: журнал транзакций — задержка записи до 5–10 мс (лучше < 5); " +
			"файлы данных — чтение до 20–25 мс на HDD, до 5 мс на SSD.</div>")
	} else {
		d.WriteString("<div class='card'>Данные ввода-вывода недоступны.</div>")
	}
	b.WriteString(sectionHTML("8", "Диски и файлы баз данных", "disks", d.String()))

	// ------------------------------------------------------- 9. индексы
	var ix strings.Builder
	if len(snap.MissingIdx) > 0 {
		rows := ""
		for _, m := range snap.MissingIdx {
			rows += fmt.Sprintf("<tr><td>%s</td><td class='mono'>%s</td><td class='r'>%s</td><td class='r'>%.0f%%</td>"+
				"<td class='r'>%s</td><td>%s</td></tr>",
				esc(sstr(m["db_name"])), esc(sstr(m["table_ref"])), fmtNum(toF(m["user_seeks"])),
				toF(m["avg_user_impact"]), fmtNum(toF(m["score"])),
				detailsBlock("CREATE INDEX", MissingIndexScript(m)))
		}
		ix.WriteString("<h3>Недостающие индексы (по данным оптимизатора)</h3><table>" +
			"<tr><th>БД</th><th>Таблица</th><th class='r'>Поисков</th><th class='r'>Ожид. эффект</th>" +
			"<th class='r'>Приоритет</th><th>Скрипт</th></tr>" + rows + "</table>" +
			"<div class='note'>Создавайте индексы по одному и проверяйте эффект: каждый индекс замедляет " +
			"вставки/обновления. Объединяйте похожие индексы.</div>")
	}
	if len(snap.Frag) > 0 {
		rows := ""
		for _, f := range snap.Frag {
			rows += fmt.Sprintf("<tr><td>%s</td><td class='mono'>%s.%s</td><td class='mono'>%s</td>"+
				"<td class='r'>%.0f%%</td><td class='r'>%s</td><td class='r'>%s</td></tr>",
				esc(sstr(f["db_name"])), esc(sstr(f["schema_name"])), esc(sstr(f["table_name"])),
				esc(sstr(f["index_name"])), toF(f["avg_fragmentation_in_percent"]),
				fmtNum(toF(f["page_count"])), fmtSizeMB(toF(f["page_count"])*8/1024))
		}
		ix.WriteString("<h3>Фрагментированные индексы (≥ 30%)</h3><table><tr><th>БД</th><th>Таблица</th>" +
			"<th>Индекс</th><th class='r'>Фрагментация</th><th class='r'>Страниц</th>" +
			"<th class='r'>Размер</th></tr>" + rows + "</table>")
	}
	if len(snap.StaleStats) > 0 {
		rows := ""
		for _, s := range snap.StaleStats {
			rows += fmt.Sprintf("<tr><td>%s</td><td class='mono'>%s.%s</td><td class='mono'>%s</td>"+
				"<td class='r'>%s</td><td class='r'>%.0f%%</td><td>%s</td></tr>",
				esc(sstr(s["db_name"])), esc(sstr(s["schema_name"])), esc(sstr(s["table_name"])),
				esc(sstr(s["stats_name"])), fmtNum(toF(s["rows"])),
				100*toF(s["modification_counter"])/mathMaxF(1, toF(s["rows"])),
				fmtDT(toTime(s["last_updated"])))
		}
		ix.WriteString("<h3>Устаревшая статистика (изменено &gt; 20% строк)</h3><table>" +
			"<tr><th>БД</th><th>Таблица</th><th>Статистика</th><th class='r'>Строк</th>" +
			"<th class='r'>Изменено</th><th>Обновлялась</th></tr>" + rows + "</table>")
	}
	if ix.Len() == 0 {
		ix.WriteString("<div class='card'>✅ Проблем с индексами и статистикой не найдено " +
			"(или глубокий анализ отключён в настройках).</div>")
	}
	b.WriteString(sectionHTML("9", "Индексы и статистика", "indexes", ix.String()))

	// ------------------------------------------------------- 10. настройки
	var cfgS strings.Builder
	if len(an.ConfigAudit) > 0 {
		rows := ""
		for _, a := range an.ConfigAudit {
			rows += fmt.Sprintf("<tr><td class='mono'>%s</td><td class='r'>%s</td><td class='r'>%s</td><td>%s</td></tr>",
				esc(a.Param), esc(a.Current), esc(a.Rec), statusIcon[a.Status])
		}
		cfgS.WriteString("<h3>Аудит конфигурации экземпляра</h3><table><tr><th>Параметр</th>" +
			"<th class='r'>Текущее</th><th class='r'>Рекомендуется</th><th>Статус</th></tr>" + rows + "</table>")
	}
	if len(snap.Databases) > 0 {
		backups := map[string]map[string]any{}
		for _, bk := range snap.Backups {
			backups[sstr(bk["db_name"])] = bk
		}
		rows := ""
		for _, db := range snap.Databases {
			name := sstr(db["name"])
			var flags []string
			if bval(db["is_auto_shrink_on"]) {
				flags = append(flags, "<span class='tag red'>auto_shrink</span>")
			}
			if bval(db["is_auto_close_on"]) {
				flags = append(flags, "<span class='tag red'>auto_close</span>")
			}
			if toI(db["compatibility_level"]) <= 110 {
				flags = append(flags, fmt.Sprintf("<span class='tag amber'>compat %d</span>", toI(db["compatibility_level"])))
			}
			if strings.ToUpper(sstr(db["page_verify_option_desc"])) != "CHECKSUM" {
				flags = append(flags, "<span class='tag amber'>verify</span>")
			}
			if sstr(db["state_desc"]) != "ONLINE" {
				flags = append(flags, "<span class='tag red'>"+esc(sstr(db["state_desc"]))+"</span>")
			}
			system := name == "master" || name == "model" || name == "msdb" || name == "tempdb"
			if sstr(db["log_reuse_wait_desc"]) != "NOTHING" && !system {
				flags = append(flags, "<span class='tag amber'>log: "+esc(sstr(db["log_reuse_wait_desc"]))+"</span>")
			}
			flagStr := strings.Join(flags, " ")
			if flagStr == "" {
				flagStr = "<span class='tag green'>ok</span>"
			}
			sysTag := ""
			if system {
				sysTag = " <span class='tag'>системная</span>"
			}
			lastFull, lastLog := "—", "—"
			if bk, ok := backups[name]; ok {
				if !isTimeZero(bk["last_full"]) {
					lastFull = fmtDT(toTime(bk["last_full"]))
				}
				if !isTimeZero(bk["last_log"]) {
					lastLog = fmtDT(toTime(bk["last_log"]))
				}
			}
			rows += fmt.Sprintf("<tr><td>%s%s</td><td>%s</td><td class='r'>%d</td><td class='r'>%s</td>"+
				"<td class='r'>%s</td><td>%s</td></tr>",
				esc(name), sysTag, esc(orDash(sstr(db["recovery_model_desc"]))),
				toI(db["compatibility_level"]), lastFull, lastLog, flagStr)
		}
		cfgS.WriteString("<h3>Базы данных</h3><table><tr><th>База</th><th>Модель восст.</th>" +
			"<th class='r'>Compat</th><th class='r'>Посл. full</th><th class='r'>Посл. log</th>" +
			"<th>Флаги</th></tr>" + rows + "</table>")
	}
	if len(snap.Config) > 0 {
		keys := make([]string, 0, len(snap.Config))
		for k := range snap.Config {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		rows := ""
		for _, k := range keys {
			rows += fmt.Sprintf("<tr><td class='mono'>%s</td><td class='r mono'>%s</td></tr>", esc(k), esc(snap.Config[k]))
		}
		cfgS.WriteString("<details><summary>Полный список настроек сервера (sys.configurations)</summary>" +
			"<table><tr><th>Параметр</th><th class='r'>Значение</th></tr>" + rows + "</table></details>")
	}
	b.WriteString(sectionHTML("10", "Настройки сервера и баз данных", "settings", cfgS.String()))

	// ------------------------------------------------------- 11. методика
	method := `
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
</div>`
	b.WriteString(sectionHTML("11", "Методика и ограничения", "method", method))

	b.WriteString(fmt.Sprintf("<div class='footer'>Сформировано %s · MS SQL Monitor %s (Go)</div>",
		fmtDT(time.Now()), AppVersion))
	b.WriteString("</div></body></html>")
	out := b.String()

	if outPath != "" {
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return out, err
		}
		if err := os.WriteFile(outPath, []byte(out), 0o644); err != nil {
			return out, err
		}
	}
	return out, nil
}

func countSev(an *Analysis, sev string) int {
	n := 0
	for _, f := range an.Findings {
		if f.Severity == sev {
			n++
		}
	}
	return n
}

func ptrVal(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func avgOf(samples []SampleRow, get func(SampleRow) float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sum := 0.0
	for _, s := range samples {
		sum += get(s)
	}
	return sum / float64(len(samples))
}

func mathMaxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
