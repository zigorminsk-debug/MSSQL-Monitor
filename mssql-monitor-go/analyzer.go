package main

// Анализ собранных данных: проблемы, причины замедлений, рекомендации.
// Основано на методологии анализа ожиданий (waits-and-queues) и лучших практиках.

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
//  Ожидания: игнорируемые типы и категории
// ---------------------------------------------------------------------------

var waitIgnore = map[string]bool{
	"SLEEP_TASK": true, "SLEEP_BPOOL_FLUSH": true, "SLEEP_DBSTARTUP": true,
	"SLEEP_DCOMSTARTUP": true, "SLEEP_MASTERDBREADY": true, "SLEEP_MASTERMDREADY": true,
	"SLEEP_MASTERUPGRADED": true, "SLEEP_MSDBSTARTUP": true, "SLEEP_SYSTEMTASK": true,
	"SLEEP_TEMPDBSTARTUP": true, "SLEEP_WORKSPACEALLOCATE": true,
	"BROKER_EVENTHANDLER": true, "BROKER_RECEIVE_WAITFOR": true, "BROKER_TASK_STOP": true,
	"BROKER_TO_FLUSH": true, "BROKER_TRANSMITTER": true, "CHECKPOINT_QUEUE": true,
	"CHKPT": true, "DIRTY_PAGE_POLL": true, "DISPATCHER_QUEUE_SLEEP": true,
	"EXECSYNC": true, "KSOURCE_WAKEUP": true, "LAZYWRITER_SLEEP": true,
	"LOGMGR_QUEUE": true, "MISCELLANEOUS": true, "ONDEMAND_TASK_QUEUE": true,
	"REQUEST_FOR_DEADLOCK_SEARCH": true, "DEADLOCK_ENUM_MUTEX": true, "RESOURCE_QUEUE": true,
	"SERVER_IDLE_CHECK": true, "SOSHOST_SLEEP": true, "SQLTRACE_BUFFER_FLUSH": true,
	"SQLTRACE_INCREMENTAL_FLUSH_SLEEP": true, "SQLTRACE_WAIT_ENTRIES": true,
	"TRACEWRITE": true, "UCS_SESSION_REGISTRATION": true, "WAIT_FOR_RESULTS": true,
	"WAITFOR": true, "WAITFOR_TASKSHUTDOWN": true, "WAIT_XTP_OFFLINE_CKPT_NEW_LOG": true,
	"WAIT_XTP_CKPT_CLOSE": true, "XE_DISPATCHER_JOIN": true, "XE_DISPATCHER_WAIT": true,
	"XE_LIVE_TARGET_TVF": true, "XE_TIMER_EVENT": true, "XE_FILE_TARGET_TVF": true,
	"CLR_AUTO_EVENT": true, "CLR_MANUAL_EVENT": true, "INTERNAL": true,
	"SOS_TASK_DEMOTION": true, "SOS_WORKER": true,
}

var waitIgnorePrefixes = []string{
	"BROKER_", "HADR_", "QDS_", "XE_", "SQLTRACE_", "DIRTY_PAGE", "SLEEP_",
	"PREEMPTIVE_XE_", "PARALLEL_REDO_", "DATABASE_MIRRORING", "TRAN_MARKLATCH",
	"DTC_", "CLR_", "FT_", "DPTMP_", "MIRROR_", "LOW_PRIORITY",
}

func isIgnorableWait(wt string) bool {
	if waitIgnore[wt] {
		return true
	}
	for _, p := range waitIgnorePrefixes {
		if strings.HasPrefix(wt, p) {
			return true
		}
	}
	return false
}

type waitCatInfo struct {
	Title string
	Color string
	Why   string
	Fixes []string
}

var waitCats = map[string]waitCatInfo{
	"cpu": {"ЦП и параллелизм", "#dc2626",
		"Запросы ждут освобождения планировщиков ЦП либо потоки параллельного плана обмениваются данными (CXPACKET). Высокая доля таких ожиданий говорит о нехватке процессорных ресурсов или неверной настройке параллелизма.",
		[]string{"Настройте MAXDOP (для OLTP обычно ≤ 8) и 'cost threshold for parallelism' (25–50).",
			"Оптимизируйте самые дорогие по ЦП запросы (раздел «Топ запросов»).",
			"Проверьте частоту компиляций (Compilations/sec > 10% от Batch Requests/sec)."}},
	"memory": {"Память (гранты запросов)", "#9333ea",
		"Запросы ждут предоставления памяти (memory grant) под сортировки и хеширования. Частая причина — запросы с завышенными оценками кардинальности или нехватка памяти.",
		[]string{"Оптимизируйте запросы с большими сортировками/хешированиями.",
			"Проверьте актуальность статистики (устаревшая статистика → завышенные гранты).",
			"Проверьте настройки 'max server memory' и объём ОЗУ сервера."}},
	"data_io": {"Чтение страниц с диска", "#2563eb",
		"Данные не находятся в буферном кэше — SQL Server читает страницы с диска (PAGEIOLATCH_*). Причины: нехватка памяти, медленные диски, неоптимальные планы запросов (сканирования таблиц).",
		[]string{"Проверьте Page Life Expectancy и объём буферного кэша (раздел «Память»).",
			"Проверьте задержки дисков (раздел «Диски и файлы»).",
			"Создайте недостающие индексы, устраните сканирования больших таблиц."}},
	"log_io": {"Запись журнала транзакций", "#0891b2",
		"Медленная запись журнала транзакций на диск (WRITELOG). Журнал должен лежать на быстром диске (SSD/NVMe), запись — быть последовательной.",
		[]string{"Разместите файлы журнала (.ldf) на отдельном быстром диске.",
			"Проверьте задержки записи журнала в разделе «Диски и файлы».",
			"Сократите объём длинных транзакций, избегайте частых мелких autogrowth."}},
	"locks": {"Блокировки и цепочки блокирования", "#d97706",
		"Транзакции удерживают блокировки и мешают друг другу (LCK_M_*). Обычно причина — длинные или незавершённые транзакции и неоптимальные планы (сканирования).",
		[]string{"Найдите головные блокировки (head blockers) в разделе «Блокировки».",
			"Рассмотрите READ_COMMITTED_SNAPSHOT для OLTP-баз.",
			"Проверьте приложение: короткие транзакции, одинаковый порядок доступа к таблицам.",
			"Оптимизируйте запросы — меньше сканирований, меньше блокировок."}},
	"network": {"Сеть и клиентские приложения", "#65a30d",
		"SQL Server отправил результат клиенту и ждёт, пока приложение заберёт данные (ASYNC_NETWORK_IO). Узкое место — на стороне приложения или сети.",
		[]string{"Проверьте, не обрабатывает ли приложение результат построчно (без буферизации).",
			"Уменьшайте объём выбираемых данных (TOP, WHERE, пагинация).",
			"Проверьте сеть между сервером приложений и SQL Server."}},
	"backup": {"Резервное копирование", "#64748b",
		"Ожидания, связанные с резервным копированием (BACKUPIO/BACKUPBUFFER). Влияет, если копирование идёт в рабочие часы.",
		[]string{"Перенесите резервное копирование на ночное время.",
			"Включите сжатие резервных копий (backup compression default).",
			"Проверьте скорость диска, на который пишутся копии."}},
	"preemptive": {"Внешние вызовы (XP, linked servers)", "#78716c",
		"SQL Server ожидает внешних вызовов ОС/внешних процедур (PREEMPTIVE_*): linked servers, xp_cmdshell, полнотекстовый поиск и т.п.",
		[]string{"Проверьте производительность связанных серверов и внешних процедур.",
			"По возможности заменяйте linked server запросы на локальные."}},
	"other": {"Прочие ожидания", "#94a3b8", "Суммарные прочие ожидания.",
		[]string{"Изучите конкретные типы ожиданий в таблице ниже."}},
}

var catTypes = map[string]map[string]bool{
	"cpu":     {"CXPACKET": true, "CXCONSUMER": true, "SOS_SCHEDULER_YIELD": true, "THREADPOOL": true, "CPU": true},
	"memory":  {"RESOURCE_SEMAPHORE": true, "RESOURCE_SEMAPHORE_QUERY_COMPILE": true, "RESOURCE_SEMAPHORE_MUTEX": true, "CMEMTHREAD": true, "MEMORY_ALLOCATION_EXT": true, "RESERVED_MEMORY_ALLOCATION_EXT": true, "MEMORY_GRANT_INSERT": true},
	"log_io":  {"WRITELOG": true, "LOGBUFFER": true, "LOGMGR_FLUSH": true, "LOG_RATE_GOVERNOR": true, "PREEMPTIVE_LOGFLUSH": true},
	"network": {"ASYNC_NETWORK_IO": true},
}

var catPrefixes = map[string][]string{
	"data_io":    {"PAGEIOLATCH_", "IO_COMPLETION", "ASYNC_IO_COMPLETION", "DISKIO"},
	"locks":      {"LCK_M_"},
	"backup":     {"BACKUP"},
	"preemptive": {"PREEMPTIVE_"},
}

func categorizeWait(wt string) string {
	for cat, types := range catTypes {
		if types[wt] {
			return cat
		}
	}
	for cat, prefixes := range catPrefixes {
		for _, p := range prefixes {
			if strings.HasPrefix(wt, p) {
				return cat
			}
		}
	}
	return "other"
}

// ---------------------------------------------------------------------------
//  Результаты анализа
// ---------------------------------------------------------------------------

type Finding struct {
	Severity       string `json:"severity"` // critical | warning | info | ok
	Area           string `json:"area"`
	Title          string `json:"title"`
	Problem        string `json:"problem"`
	Recommendation string `json:"recommendation"`
	Script         string `json:"script"`
}

type Cause struct {
	Severity    string   `json:"severity"`
	Title       string   `json:"title"`
	Evidence    []string `json:"evidence"`
	Explanation string   `json:"explanation"`
	Fixes       []string `json:"fixes"`
}

type CatShare struct {
	Key   string  `json:"key"`
	Title string  `json:"title"`
	Color string  `json:"color"`
	MS    float64 `json:"ms"`
	Pct   float64 `json:"pct"`
}

type WaitTop struct {
	Type string  `json:"wait_type"`
	MS   float64 `json:"ms"`
	Pct  float64 `json:"pct"`
	Cat  string  `json:"cat"`
}

type AuditRow struct {
	Param, Current, Rec, Status string
}

type Stats struct {
	N                  int
	Hours              float64
	CPUAvg, CPUMax     float64
	BatchAvg, BatchMax float64
	PLEAvg, PLEMin     float64
	BlockedMax         int
	BlockedEvents      int
	GrantsMax          float64
	CompileRatio       float64 // средняя доля компиляций
	ErrorsAvg          float64
	ReadsAvg           float64
	WritesAvg          float64
	WaitsByType        map[string]float64
	CatMS              map[string]float64
	CatPct             map[string]float64
	WaitsTop           []WaitTop
}

type Analysis struct {
	Findings    []Finding  `json:"findings"`
	Causes      []Cause    `json:"causes"`
	HealthScore int        `json:"health_score"`
	Summary     string     `json:"summary"`
	WaitByCat   []CatShare `json:"wait_by_cat"`
	WaitTop     []WaitTop  `json:"wait_top"`
	ConfigAudit []AuditRow `json:"config_audit"`
	Stats       Stats      `json:"-"`
}

var sevRu = map[string]string{"critical": "Критично", "warning": "Важно", "info": "Рекомендуется", "ok": "Норма"}
var sevWeight = map[string]float64{"critical": 12, "warning": 5, "info": 1.5, "ok": 0}

// ---------------------------------------------------------------------------
//  Статистика по истории
// ---------------------------------------------------------------------------

func computeStats(h History) Stats {
	st := Stats{WaitsByType: map[string]float64{}, CatMS: map[string]float64{}, CatPct: map[string]float64{}}
	st.N = len(h.Samples)
	st.Hours = h.Hours
	if len(h.Samples) > 0 {
		var cpuSum, batchSum, pleSum, compSum, errSum, rSum, wSum float64
		var cpuN, batchN, pleN, compN int
		for _, s := range h.Samples {
			if s.SQLCPU > 0 || s.Other > 0 {
				cpuSum += s.SQLCPU
				cpuN++
				if s.SQLCPU > st.CPUMax {
					st.CPUMax = s.SQLCPU
				}
			}
			if s.Batch > 0 || true {
				batchSum += s.Batch
				batchN++
				if s.Batch > st.BatchMax {
					st.BatchMax = s.Batch
				}
			}
			if s.PLE > 0 {
				pleSum += s.PLE
				pleN++
				if st.PLEMin == 0 || s.PLE < st.PLEMin {
					st.PLEMin = s.PLE
				}
			}
			if s.Compiles > 0 {
				compSum += s.Compiles
				compN++
			}
			errSum += s.Errors
			rSum += s.Reads
			wSum += s.Writes
			if int(s.Blocked) > st.BlockedMax {
				st.BlockedMax = int(s.Blocked)
			}
			if s.Blocked > 0 {
				st.BlockedEvents++
			}
			if s.Grants > st.GrantsMax {
				st.GrantsMax = s.Grants
			}
		}
		if cpuN > 0 {
			st.CPUAvg = cpuSum / float64(cpuN)
		}
		if batchN > 0 {
			st.BatchAvg = batchSum / float64(batchN)
		}
		if pleN > 0 {
			st.PLEAvg = pleSum / float64(pleN)
		}
		st.ErrorsAvg = errSum / float64(len(h.Samples))
		st.ReadsAvg = rSum / float64(len(h.Samples))
		st.WritesAvg = wSum / float64(len(h.Samples))
		if compN > 0 && batchN > 0 {
			ratioSum, rN := 0.0, 0
			for _, s := range h.Samples {
				if s.Batch > 0 && s.Compiles > 0 {
					ratioSum += s.Compiles / s.Batch
					rN++
				}
			}
			if rN > 0 {
				st.CompileRatio = ratioSum / float64(rN)
			}
		}
	}
	for _, w := range h.Waits {
		if w.WaitMS > 0 && !isIgnorableWait(w.Type) {
			st.WaitsByType[w.Type] += w.WaitMS
		}
	}
	for wt, ms := range st.WaitsByType {
		st.CatMS[categorizeWait(wt)] += ms
	}
	var total float64
	for _, ms := range st.CatMS {
		total += ms
	}
	if total > 0 {
		for cat, ms := range st.CatMS {
			st.CatPct[cat] = ms / total * 100
		}
	}
	top := []WaitTop{}
	for wt, ms := range st.WaitsByType {
		top = append(top, WaitTop{Type: wt, MS: ms, Cat: categorizeWait(wt)})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].MS > top[j].MS })
	if len(top) > 15 {
		top = top[:15]
	}
	for i := range top {
		top[i].Pct = top[i].MS / total * 100
	}
	st.WaitsTop = top
	return st
}

// ---------------------------------------------------------------------------
//  Главная функция анализа
// ---------------------------------------------------------------------------

func Analyze(snap *Snapshot, hist *History) *Analysis {
	an := &Analysis{}
	st := computeStats(*hist)
	an.Stats = st
	findings := &an.Findings
	audit := &an.ConfigAudit

	add := func(sev, area, title, problem, rec, script string, _ ...string) {
		*findings = append(*findings, Finding{sev, area, title, problem, rec, script})
	}
	cfgInt := func(name string) (int64, bool) {
		if v, ok := snap.Config[name]; ok {
			return toI(v), true
		}
		return 0, false
	}
	ramMB := toF(snap.SysInfo["physical_memory_kb"]) / 1024
	cpuCount := toI(snap.SysInfo["cpu_count"])
	now := time.Now()

	// -------------------------------------------------------------- права
	for _, e := range snap.Errors {
		if strings.Contains(strings.ToUpper(e), "VIEW SERVER STATE") {
			add("warning", "Права доступа", "Недостаточно прав для полного мониторинга",
				"Учётной записи запрещён доступ к DMV: "+e,
				"Предоставьте учётной записи право VIEW SERVER STATE и роль msdb db_datareader для полного снимка состояния.",
				"USE master;\nGRANT VIEW SERVER STATE TO [домен\\пользователь];\n"+
					"USE msdb;\nEXEC sp_addrolemember 'db_datareader', 'домен\\пользователь';")
			break
		}
	}

	// ------------------------------------------------------- конфигурация
	if maxmem, ok := cfgInt("max server memory (MB)"); ok && ramMB > 0 {
		recMem := int((ramMB-math.Max(4096, ramMB*0.15))/64) * 64
		if recMem < 2048 {
			recMem = 2048
		}
		recMemStr := fmtNum(float64(recMem))
		if maxmem >= 2147483647 {
			if ramMB >= 8192 {
				add("warning", "Конфигурация", "Память сервера не ограничена (max server memory по умолчанию)",
					"SQL Server может занять всю память ОС, оставив системе недостаточно ресурсов — это приводит к вытеснению в файл подкачки и деградации всей машины.",
					fmt.Sprintf("Ограничьте 'max server memory' значением ≈ ОЗУ минус 4 ГБ (или минус 10–20%%). Для этого сервера (ОЗУ %s ГБ) рекомендуется ≈ %s МБ.",
						fmt.Sprintf("%.0f", ramMB/1024), recMemStr),
					"EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"+
						fmt.Sprintf("EXEC sys.sp_configure 'max server memory (MB)', %d;\nRECONFIGURE;", recMem))
			}
			*audit = append(*audit, AuditRow{"max server memory", "не ограничено", "≈ " + recMemStr + " МБ",
				map[bool]string{true: "warn", false: "ok"}[ramMB >= 8192]})
		} else if float64(maxmem) > ramMB {
			add("critical", "Конфигурация", "max server memory больше физической памяти",
				fmt.Sprintf("Значение (%s МБ) превышает ОЗУ сервера (%.0f ГБ) — риск нехватки памяти на уровне ОС.",
					fmtNum(float64(maxmem)), ramMB/1024),
				"Установите значение ниже объёма ОЗУ.",
				"EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"+
					fmt.Sprintf("EXEC sys.sp_configure 'max server memory (MB)', %d;\nRECONFIGURE;", recMem))
			*audit = append(*audit, AuditRow{"max server memory", fmtNum(float64(maxmem)) + " МБ", "≈ " + recMemStr + " МБ", "bad"})
		} else {
			*audit = append(*audit, AuditRow{"max server memory", fmtNum(float64(maxmem)) + " МБ", "≈ " + recMemStr + " МБ", "ok"})
		}
	}

	if maxdop, ok := cfgInt("max degree of parallelism"); ok {
		if maxdop == 0 && cpuCount >= 8 {
			add("warning", "Конфигурация", fmt.Sprintf("MAXDOP не ограничен (0) при %d логических ядрах", cpuCount),
				"Параллельные планы могут занять все ядра, вызывая волны деградации (типично для CXPACKET).",
				"Для смешанной/OLTP нагрузки установите MAXDOP = 8 или число ядер одного NUMA-узла.",
				"EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"+
					"EXEC sys.sp_configure 'max degree of parallelism', 8;\nRECONFIGURE;")
			*audit = append(*audit, AuditRow{"max degree of parallelism", "0 (все ядра)", "8", "warn"})
		} else if maxdop > 8 {
			add("info", "Конфигурация", fmt.Sprintf("MAXDOP = %d — слишком высокое значение", maxdop),
				"Для типичной OLTP-нагрузки рекомендуется MAXDOP ≤ 8.",
				"Снизьте значение до 8.", "")
			*audit = append(*audit, AuditRow{"max degree of parallelism", fmt.Sprintf("%d", maxdop), "≤ 8", "warn"})
		} else {
			*audit = append(*audit, AuditRow{"max degree of parallelism", fmt.Sprintf("%d", maxdop), "≤ 8", "ok"})
		}
	}

	if ct, ok := cfgInt("cost threshold for parallelism"); ok {
		if ct <= 5 {
			add("warning", "Конфигурация", fmt.Sprintf("Порог параллелизма оставлен по умолчанию (%d)", ct),
				"Мелкие запросы уходят в параллельные планы — рост CXPACKET и накладных расходов.",
				"Увеличьте 'cost threshold for parallelism' до 25–50.",
				"EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"+
					"EXEC sys.sp_configure 'cost threshold for parallelism', 50;\nRECONFIGURE;")
			*audit = append(*audit, AuditRow{"cost threshold for parallelism", fmt.Sprintf("%d", ct), "25–50", "warn"})
		} else {
			*audit = append(*audit, AuditRow{"cost threshold for parallelism", fmt.Sprintf("%d", ct), "25–50", "ok"})
		}
	}

	plans := map[string]map[string]any{}
	var totalPlanMB float64
	for _, p := range snap.CachedPlans {
		plans[sstr(p["objtype"])] = p
		totalPlanMB += toF(p["size_mb"])
	}
	adhoc, hasAdhoc := plans["Adhoc"]
	adhocShare := 0.0
	if hasAdhoc {
		adhocShare = toF(adhoc["size_mb"]) / totalPlanMB * 100
	}
	optAdhoc, _ := cfgInt("optimize for ad hoc workloads")
	if optAdhoc == 0 && adhocShare > 25 && toI(adhoc["plan_count"]) > 5000 {
		add("warning", "Конфигурация", "Кэш планов засорён ad-hoc запросами",
			fmt.Sprintf("Ad-hoc планы занимают %.0f%% кэша (%s МБ, %s планов) — память тратится впустую.",
				adhocShare, fmtNum(toF(adhoc["size_mb"])), fmtNum(toF(adhoc["plan_count"]))),
			"Включите 'optimize for ad hoc workloads' — при первом выполнении будет компилироваться только заглушка плана.",
			"EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"+
				"EXEC sys.sp_configure 'optimize for ad hoc workloads', 1;\nRECONFIGURE;")
	}
	optStatus := "ok"
	if optAdhoc != 1 && adhocShare > 25 {
		optStatus = "warn"
	}
	*audit = append(*audit, AuditRow{"optimize for ad hoc workloads",
		orDash(snap.Config["optimize for ad hoc workloads"]), "1", optStatus})

	if pb, _ := cfgInt("priority boost"); pb == 1 {
		add("warning", "Конфигурация", "Включён priority boost",
			"Повышение приоритета потока SQL Server устарело и может дестабилизировать сервер.",
			"Отключите 'priority boost'.",
			"EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"+
				"EXEC sys.sp_configure 'priority boost', 0;\nRECONFIGURE;")
	}
	if lp, _ := cfgInt("lightweight pooling"); lp == 1 {
		add("warning", "Конфигурация", "Включён lightweight pooling (fibers)",
			"Режим волокон ухудшает работу и не решает проблему ЦП.",
			"Отключите 'lightweight pooling'.",
			"EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"+
				"EXEC sys.sp_configure 'lightweight pooling', 0;\nRECONFIGURE;")
	}
	if ramMB >= 61440 && !strings.Contains(strings.ToUpper(sstr(snap.SysInfo["sql_memory_model_desc"])), "LOCK_PAGES") {
		add("info", "Конфигурация", "Lock Pages in Memory не включён",
			fmt.Sprintf("На сервере %.0f ГБ ОЗУ рекомендуется разрешить SQL Server фиксировать страницы в памяти (защита от вытеснения ОС).", ramMB/1024),
			"Выдайте учётной записи служб SQL Server право «Lock pages in memory» (групповая политика) и перезапустите службу.", "")
	}

	// ------------------------------------------------------------- tempdb
	if snap.TempdbFiles > 0 && cpuCount >= 4 && snap.TempdbFiles < int(math.Min(8, float64(cpuCount))) {
		need := int(math.Min(8, float64(cpuCount)))
		script := "ALTER DATABASE tempdb MODIFY FILE (NAME = N'tempdev', SIZE = 8GB);\n"
		for i := 2; i <= need; i++ {
			script += fmt.Sprintf("ALTER DATABASE tempdb ADD FILE (NAME = N'temp%d', FILENAME = "+
				"N'D:\\MSSQL\\DATA\\tempdb_mssql_%d.ndf', SIZE = 8GB, FILEGROWTH = 512MB);\n", i, i)
		}
		add("warning", "Конфигурация", fmt.Sprintf("tempdb: всего %d файл(а) данных", snap.TempdbFiles),
			fmt.Sprintf("При %d ядрах один файл tempdb создаёт конкуренцию за страницы GAM/SGAM (PFS/GAM-конфликты).", cpuCount),
			fmt.Sprintf("Создайте %d файлов данных одинакового размера с одинаковым autogrowth.", need), script)
		*audit = append(*audit, AuditRow{"Файлы данных tempdb", fmt.Sprintf("%d", snap.TempdbFiles),
			fmt.Sprintf("%d (по числу ядер, до 8)", need), "warn"})
	} else if snap.TempdbFiles > 0 {
		*audit = append(*audit, AuditRow{"Файлы данных tempdb", fmt.Sprintf("%d", snap.TempdbFiles),
			"min(8, ядра)", "ok"})
	}

	// -------------------------------------------------------- базы данных
	var offline, autoShrink, autoClose, badVerify, noFull, fullNoLog []string
	var lowCompat []string
	backups := map[string]map[string]any{}
	for _, b := range snap.Backups {
		backups[sstr(b["db_name"])] = b
	}
	for _, d := range snap.Databases {
		name := sstr(d["name"])
		if name == "master" || name == "model" || name == "msdb" || name == "tempdb" {
			continue
		}
		if sstr(d["state_desc"]) != "ONLINE" {
			offline = append(offline, name)
			continue
		}
		if bval(d["is_auto_shrink_on"]) {
			autoShrink = append(autoShrink, name)
		}
		if bval(d["is_auto_close_on"]) {
			autoClose = append(autoClose, name)
		}
		if strings.ToUpper(sstr(d["page_verify_option_desc"])) != "CHECKSUM" {
			badVerify = append(badVerify, name)
		}
		if cl := toI(d["compatibility_level"]); cl > 0 && cl <= 110 {
			lowCompat = append(lowCompat, name)
		}
		if b, ok := backups[name]; ok {
			lf := b["last_full"]
			ll := b["last_log"]
			if sstr(d["recovery_model_desc"]) == "FULL" {
				if ll == nil || isTimeZero(ll) || now.Sub(toTime(ll)) > 24*time.Hour {
					fullNoLog = append(fullNoLog, name)
				}
			}
			if lf == nil || isTimeZero(lf) || now.Sub(toTime(lf)) > 7*24*time.Hour {
				noFull = append(noFull, name)
			}
		} else {
			noFull = append(noFull, name)
			if sstr(d["recovery_model_desc"]) == "FULL" {
				fullNoLog = append(fullNoLog, name)
			}
		}
	}

	if len(offline) > 0 {
		add("critical", "Базы данных", "Базы не в сети: "+strings.Join(offline, ", "),
			"База данных в состоянии не ONLINE — недоступна приложениям.",
			"Проверьте причину (журналы SQL Server, состояние файлов, ALTER DATABASE).", "")
	}
	if len(autoShrink) > 0 {
		add("critical", "Базы данных", "Включён AUTO_SHRINK: "+strings.Join(autoShrink, ", "),
			"Автосжатие файлов вызывает постоянное уменьшение/рост файлов, фрагментацию и нагрузку на ЦП — одна из самых вредных настроек.",
			"Отключите AUTO_SHRINK и управляйте размером файлов вручную.",
			strings.Join(mapStr(autoShrink, func(n string) string { return "ALTER DATABASE [" + n + "] SET AUTO_SHRINK OFF;" }), "\n"))
	}
	if len(autoClose) > 0 {
		add("warning", "Базы данных", "Включён AUTO_CLOSE: "+strings.Join(autoClose, ", "),
			"База закрывается после отключения последнего пользователя: каждый новый коннект платит за открытие файлов и очистку кэша процедур.",
			"Отключите AUTO_CLOSE.",
			strings.Join(mapStr(autoClose, func(n string) string { return "ALTER DATABASE [" + n + "] SET AUTO_CLOSE OFF;" }), "\n"))
	}
	if len(badVerify) > 0 {
		add("warning", "Базы данных", "CHECKSUM отключён: "+strings.Join(badVerify, ", "),
			"Без CHECKSUM повреждения страниц могут оставаться незамеченными до резервного копирования (когда уже поздно).",
			"Включите проверку страниц и пересоздайте бэкапы.",
			strings.Join(mapStr(badVerify, func(n string) string { return "ALTER DATABASE [" + n + "] SET PAGE_VERIFY CHECKSUM;" }), "\n"))
	}
	if len(lowCompat) > 0 {
		add("info", "Базы данных", "Устаревший уровень совместимости",
			"Базы: "+strings.Join(lowCompat, ", ")+".",
			"Новый уровень совместимости включает улучшенный оптимизатор (новый cardinality estimator). Проверьте на тестовой среде и повысьте.",
			strings.Join(mapStr(lowCompat, func(n string) string { return "ALTER DATABASE [" + n + "] SET COMPATIBILITY_LEVEL = 150;" }), "\n"))
	}
	if len(noFull) > 0 {
		add("critical", "Резервное копирование",
			"Нет полной резервной копии за последние 7 дней: "+strings.Join(noFull, ", "),
			"Восстановление после сбоя будет невозможно. Проверьте задания резервного копирования.",
			"Настройте регулярное резервное копирование (полное — еженедельно/ежедневно, разностное — ежедневно, журнал — каждые 15–30 минут для FULL-модели).",
			strings.Join(mapStr(noFull, func(n string) string {
				return fmt.Sprintf("BACKUP DATABASE [%s] TO DISK = N'D:\\Backup\\%s.bak' WITH COMPRESSION, INIT, CHECKSUM;", n, n)
			}), "\n"))
	}
	if len(fullNoLog) > 0 {
		add("critical", "Резервное копирование",
			"FULL-модель без копий журнала: "+strings.Join(fullNoLog, ", "),
			"Журнал транзакций растёт бесконечно (log_reuse_wait = LOG_BACKUP). Возможны переполнение диска и потеря данных на точку отказа.",
			"Настройте регулярное резервное копирование журнала транзакций.",
			strings.Join(mapStr(fullNoLog, func(n string) string {
				return fmt.Sprintf("BACKUP LOG [%s] TO DISK = N'D:\\Backup\\%s_log.trn' WITH COMPRESSION, INIT;", n, n)
			}), "\n"))
	}

	// autogrowth
	var badGrowth []string
	for _, f := range snap.FileCfg {
		db := sstr(f["db_name"])
		if db == "master" || db == "model" || db == "msdb" || db == "tempdb" {
			continue
		}
		sizeMB := toF(f["size_mb"])
		if bval(f["is_percent_growth"]) && sizeMB > 8192 {
			badGrowth = append(badGrowth, fmt.Sprintf("%s!%s: рост %d%%", db, sstr(f["file_name"]), int(toI(f["growth"]))))
		} else if !bval(f["is_percent_growth"]) && sstr(f["type_desc"]) == "ROWS" &&
			toI(f["growth"])*8 < 65536 && sizeMB > 4096 {
			badGrowth = append(badGrowth, fmt.Sprintf("%s!%s: шаг %d МБ", db, sstr(f["file_name"]), int(toI(f["growth"])*8/1024)))
		}
	}
	if len(badGrowth) > 0 {
		if len(badGrowth) > 8 {
			badGrowth = badGrowth[:8]
		}
		add("warning", "Базы данных", "Неоптимальный autogrowth файлов",
			strings.Join(badGrowth, "; "),
			"Процентный рост на больших файлах приводит к длительным паузам и фрагментации; мелкий шаг (1–64 МБ) у больших файлов вызывает тысячи расширений. Задайте фиксированный шаг 256–512 МБ (данные) / 512–1024 МБ (журнал) и следите за свободным местом.", "")
	}

	// VLF
	var bigVLF []string
	for _, v := range snap.VLF {
		if c := toI(v["vlf_count"]); c > 1000 {
			bigVLF = append(bigVLF, fmt.Sprintf("%s: %d VLF", sstr(v["db_name"]), c))
		}
	}
	if len(bigVLF) > 0 {
		add("warning", "Базы данных", "Слишком много VLF в журнале транзакций",
			strings.Join(bigVLF, "; "),
			"Больше 1000 виртуальных файлов журнала замедляют восстановление и операции с журналом (crash recovery, log reader).",
			"Уменьшите журнал до минимума и заново увеличьте крупными порциями (ALTER DATABASE ... MODIFY FILE (SIZE=...)).", "")
	}

	// ------------------------------------------------------------- память
	availKB := toF(snap.OSMem["available_physical_kb"])
	totalKB := toF(snap.OSMem["total_physical_kb"])
	if totalKB > 0 && availKB > 0 && availKB/totalKB < 0.05 {
		add("warning", "Память", fmt.Sprintf("Свободная память ОС на грани (%.1f ГБ из %.0f ГБ)", availKB/1048576, totalKB/1048576),
			"Системе не хватает памяти: возможно вытеснение в pagefile и общая деградация. Проверьте настройки max server memory и сторонние процессы.",
			"Уменьшите max server memory или добавьте ОЗУ.", "")
	}
	if st.PLEMin > 0 && st.PLEMin < 300 && st.N > 10 {
		add("warning", "Память", "Низкое время жизни страниц в кэше (PLE)",
			fmt.Sprintf("Минимальный PLE за период: %.0f сек (норма — сотни и тысячи). SQL Server сбрасывает страницы из буферного кэша из-за нехватки памяти.", st.PLEMin),
			"Увеличьте память сервера/max server memory, проверьте запросы с большим числом чтений.", "")
	}
	if st.GrantsMax > 0 && st.N > 10 {
		add("warning", "Память", "Запросы стоят в очереди за памятью (Memory Grants Pending)",
			fmt.Sprintf("Максимум ожидающих грантов за период: %.0f. Запросам не хватает памяти под сортировки/хеши.", st.GrantsMax),
			"Оптимизируйте тяжёлые запросы, проверьте статистику; при необходимости увеличьте память.", "")
	}
	if st.CompileRatio > 0.15 && st.BatchAvg > 20 {
		add("warning", "Производительность", "Высокая доля компиляций",
			fmt.Sprintf("SQL Compilations/sec составляют %.0f%% от Batch Requests/sec (норма < 10–15%%). Процессор тратится на компиляцию планов.", st.CompileRatio*100),
			"Включите «optimize for ad hoc workloads», проверьте параметризацию запросов в приложении и однотипные ad-hoc запросы (разные литералы).", "")
	}
	if st.CPUAvg > 80 {
		add("warning", "Процессор", fmt.Sprintf("Высокая средняя загрузка ЦП SQL Server (%.0f%%)", st.CPUAvg),
			"Процессор — текущее узкое место: средняя за период загрузка выше 80%.",
			"Оптимизируйте самые дорогие по CPU запросы, проверьте параллелизм (MAXDOP) и компиляции.", "")
	} else if st.CPUMax > 95 {
		add("info", "Процессор", fmt.Sprintf("Пиковые значения загрузки ЦП до %.0f%%", st.CPUMax),
			"Кратковременные пики — проверить природу нагрузки.",
			"Если пики регулярны, оптимизируйте запросы, создающие их.", "")
	}
	if st.ErrorsAvg > 1 && st.N > 5 {
		add("warning", "Надёжность", fmt.Sprintf("Постоянные ошибки (Errors/sec ≈ %.1f)", st.ErrorsAvg),
			"В счётчике ошибок устойчивое значение выше нуля.",
			"Проверьте журнал SQL Server (XE/ERRORLOG): возможны неудачные входы, ошибки ограничения или разрывы соединений.", "")
	}

	// ------------------------------------------------------------- ожидания
	cats := []CatShare{}
	for cat, ms := range st.CatMS {
		info := waitCats[cat]
		cats = append(cats, CatShare{Key: cat, Title: info.Title, Color: info.Color, MS: ms, Pct: st.CatPct[cat]})
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].MS > cats[j].MS })
	an.WaitByCat = cats
	an.WaitTop = st.WaitsTop

	if st.CatPct["cpu"] > 25 && st.CatMS["cpu"] > 60000 {
		add("warning", "Ожидания", fmt.Sprintf("Высокая доля ожиданий параллелизма (CXPACKET и др.) — %.0f%%", st.CatPct["cpu"]),
			"Запросы активно работают параллельно и конкурируют за планировщики.",
			"Настройте MAXDOP и cost threshold for parallelism; проверьте тяжёлые отчётные запросы.", "")
	}
	if st.CatPct["locks"] > 10 && st.CatMS["locks"] > 30000 {
		add("warning", "Ожидания", fmt.Sprintf("Значимые ожидания блокировок — %.0f%%", st.CatPct["locks"]),
			"Транзакции ожидают снятия блокировок друг другом.",
			"См. раздел «Блокировки»: найдите head-blocker и длинные транзакции.", "")
	}
	if st.CatPct["network"] > 20 && st.CatMS["network"] > 60000 {
		add("warning", "Ожидания", fmt.Sprintf("Высокая доля ASYNC_NETWORK_IO — %.0f%%", st.CatPct["network"]),
			"Сервер ждёт, пока приложения заберут результаты запросов.",
			"Оптимизируйте обработку результатов на стороне приложения (буферизация, меньшие выборки).", "")
	}

	// ------------------------------------------------------------- файлы
	var hotFiles []string
	for _, f := range snap.Files {
		limit := 25.0
		kind := "чтение данных"
		lat := f.ReadLatencyMS
		if f.TypeDesc == "LOG" {
			limit = 15
			kind = "запись журнала"
			lat = f.WriteLatencyMS
		}
		if lat > limit && f.ReadsSec+f.WritesSec > 1 {
			hotFiles = append(hotFiles, fmt.Sprintf("%s!%s: %s %.0f мс", f.DBName, f.FileName, kind, lat))
		}
	}
	if len(hotFiles) > 0 {
		if len(hotFiles) > 8 {
			hotFiles = hotFiles[:8]
		}
		add("warning", "Диски и файлы", "Медленные файлы (задержки ввода-вывода)",
			strings.Join(hotFiles, "; "),
			"Задержки выше нормы (журнал > 15 мс, данные > 25 мс). Разместите файлы на более быстрых дисках (SSD/NVMe), журнал — отдельно от данных и tempdb.", "")
	}

	// ------------------------------------------------------- индексы/статистика
	if len(snap.MissingIdx) > 0 {
		top := snap.MissingIdx[0]
		add("warning", "Индексы", fmt.Sprintf("Обнаружены недостающие индексы (%d)", len(snap.MissingIdx)),
			fmt.Sprintf("Самый значимый: %s — %.0f поисков, влияние %.0f%%.",
				sstr(top["table_ref"]), toF(top["user_seeks"]), toF(top["avg_user_impact"])),
			"Создайте индексы из раздела «Индексы и статистика» (проверив на тестовой среде и объединив с существующими).", "")
	}
	if len(snap.Frag) > 0 {
		f0 := snap.Frag[0]
		script := ""
		for i, f := range snap.Frag {
			if i >= 5 {
				break
			}
			script += fmt.Sprintf("USE [%s];\nALTER INDEX [%s] ON [%s].[%s] REBUILD;\n",
				sstr(f["db_name"]), sstr(f["index_name"]), sstr(f["schema_name"]), sstr(f["table_name"]))
		}
		add("warning", "Индексы", fmt.Sprintf("Фрагментированные индексы (%d)", len(snap.Frag)),
			fmt.Sprintf("Худший: %s.%s — %.0f%% фрагментации (%s страниц).",
				sstr(f0["schema_name"]), sstr(f0["table_name"]),
				toF(f0["avg_fragmentation_in_percent"]), fmtNum(toF(f0["page_count"]))),
			"Выполните обслуживание: REORGANIZE при 10–30%, REBUILD при > 30% фрагментации.", script)
	}
	if len(snap.StaleStats) > 0 {
		s0 := snap.StaleStats[0]
		script := ""
		for i, s := range snap.StaleStats {
			if i >= 5 {
				break
			}
			script += fmt.Sprintf("USE [%s];\nUPDATE STATISTICS [%s].[%s] [%s];\n",
				sstr(s["db_name"]), sstr(s["schema_name"]), sstr(s["table_name"]), sstr(s["stats_name"]))
		}
		add("warning", "Индексы", fmt.Sprintf("Устаревшая статистика (%d объектов)", len(snap.StaleStats)),
			fmt.Sprintf("Например: %s.%s — изменено %s из %s строк (%.0f%%), обновлялась %s.",
				sstr(s0["schema_name"]), sstr(s0["table_name"]),
				fmtNum(toF(s0["modification_counter"])), fmtNum(toF(s0["rows"])),
				100*toF(s0["modification_counter"])/mathMax(1, toF(s0["rows"])),
				fmtDT(toTime(s0["last_updated"]))),
			"Обновите статистику — устаревшие оценки приводят к плохим планам и избыточным чтениям.", script)
	}

	// uptime
	if up := toI(snap.SysInfo["uptime_sec"]); up > 0 && up < 86400 {
		add("info", "Надёжность", "Сервер перезапускался менее суток назад",
			"Накопительная статистика (DMV) могла обнулиться.",
			"Рекомендуется наблюдать за сервером не менее суток для достоверных выводов.", "")
	}

	// ------------------------------------------------------- причины замедлений
	an.Causes = buildCauses(st, snap)
	// ------------------------------------------------------- health score
	cnt := map[string]int{}
	for _, f := range an.Findings {
		cnt[f.Severity]++
	}
	penalty := sevWeight["critical"]*float64(minI(3, cnt["critical"])) +
		sevWeight["warning"]*float64(minI(6, cnt["warning"])) +
		sevWeight["info"]*float64(minI(4, cnt["info"]))
	if st.CPUAvg > 90 {
		penalty += 10
	}
	an.HealthScore = int(clamp(math.Round(100-penalty), 5, 100))
	an.Summary = fmt.Sprintf("Критичных проблем: %d, предупреждений: %d, рекомендаций: %d. %s",
		cnt["critical"], cnt["warning"], cnt["info"], verdict(an.HealthScore))

	sort.SliceStable(an.Findings, func(i, j int) bool {
		a, b := an.Findings[i], an.Findings[j]
		if sevWeight[a.Severity] != sevWeight[b.Severity] {
			return sevWeight[a.Severity] > sevWeight[b.Severity]
		}
		return a.Area < b.Area
	})
	// причины — по важности (как в Python-версии: critical, warning, info)
	causeOrder := map[string]int{"critical": 0, "warning": 1, "info": 2, "ok": 3}
	sort.SliceStable(an.Causes, func(i, j int) bool {
		return causeOrder[an.Causes[i].Severity] < causeOrder[an.Causes[j].Severity]
	})
	return an
}

func verdict(score int) string {
	switch {
	case score >= 85:
		return "Сервер в целом в хорошем состоянии."
	case score >= 70:
		return "Есть замечания, требующие внимания."
	case score >= 50:
		return "Обнаружены серьёзные проблемы производительности."
	default:
		return "Критическое состояние — требуется срочное вмешательство."
	}
}

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func mapStr(list []string, f func(string) string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, f(s))
	}
	return out
}

func toTime(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x
	case string:
		for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999",
			"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
			if t, err := time.ParseInLocation(layout, x, time.Local); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

func isTimeZero(v any) bool { return toTime(v).IsZero() }

func fmtDT(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("02.01.2006 15:04")
}

// buildCauses формирует раздел «Причины замедления работы».
func buildCauses(st Stats, snap *Snapshot) []Cause {
	var causes []Cause
	pct := st.CatPct

	topWaitMS := func(types ...string) float64 {
		for _, w := range st.WaitsTop {
			for _, t := range types {
				if w.Type == t {
					return w.MS
				}
			}
		}
		return 0
	}

	// 1. ЦП
	if st.CPUAvg > 65 || pct["cpu"] > 30 {
		ev := []string{fmt.Sprintf("Средняя загрузка ЦП SQL: %s%%, пиковая: %s%%",
			fmtNum(st.CPUAvg), fmtNum(st.CPUMax))}
		if pct["cpu"] > 0 {
			ev = append(ev, fmt.Sprintf("Доля ожиданий ЦП/параллелизма: %.0f%% от всех ожиданий", pct["cpu"]))
		}
		if st.CompileRatio > 0.15 {
			ev = append(ev, fmt.Sprintf("Компиляции: %.0f%% от Batch Requests/sec", st.CompileRatio*100))
		}
		if len(snap.TopQueries) > 0 {
			ev = append(ev, fmt.Sprintf("Самый дорогой запрос: %.0f мс ЦП в среднем (%s)",
				toF(snap.TopQueries[0]["avg_cpu_ms"]), sstr(snap.TopQueries[0]["db_name"])))
		}
		fixes := []string{"Оптимизировать топ-запросы по ЦП (раздел «Топ запросов»)."}
		if topWaitMS("CXPACKET") > 0 {
			fixes = append(fixes, "Настроить MAXDOP ≤ 8 и cost threshold for parallelism 25–50.")
		}
		if st.CompileRatio > 0.15 {
			fixes = append(fixes, "Включить optimize for ad hoc workloads, параметризовать запросы.")
		}
		sev := "warning"
		if st.CPUAvg > 85 {
			sev = "critical"
		}
		causes = append(causes, Cause{sev, "Процессор перегружен", ev,
			"SQL Server не хватает вычислительных ресурсов: запросы стоят в очереди на планировщики, растут ожидания SOS_SCHEDULER_YIELD/CXPACKET.", fixes})
	}

	// 2. Память / буферный кэш
	if (st.PLEMin > 0 && st.PLEMin < 300) || pct["data_io"] > 20 {
		ev := []string{}
		if st.PLEMin > 0 {
			ev = append(ev, fmt.Sprintf("PLE: мин %.0f, среднее %s сек (низкие значения = сброс кэша)",
				st.PLEMin, fmtNum(st.PLEAvg)))
		}
		if pct["data_io"] > 0 {
			ev = append(ev, fmt.Sprintf("Доля ожиданий чтения с диска (PAGEIOLATCH и др.): %.0f%%", pct["data_io"]))
		}
		ev = append(ev, fmt.Sprintf("Чтений с диска в среднем: %s стр/сек", fmtNum(st.ReadsAvg)))
		sev := "warning"
		if st.PLEMin > 0 && st.PLEMin <= 150 {
			sev = "critical"
		}
		causes = append(causes, Cause{sev, "Нехватка памяти для буферного кэша (или лишние чтения с диска)", ev,
			"Данные не помещаются в буферный пул: SQL Server постоянно читает страницы с диска. Либо памяти мало, либо запросы читают слишком много лишнего (сканирования, отсутствующие индексы).",
			[]string{"Проверить/увеличить max server memory и объём ОЗУ сервера.",
				"Создать недостающие индексы, устранить сканирования больших таблиц.",
				"Проверить задержки чтения по файлам (раздел «Диски и файлы»)."}})
	}

	// 3. Журнал транзакций
	worstLog := 0.0
	for _, f := range snap.Files {
		if f.TypeDesc == "LOG" && f.WriteLatencyMS > worstLog {
			worstLog = f.WriteLatencyMS
		}
	}
	if pct["log_io"] > 10 || worstLog > 15 {
		ev := []string{}
		if pct["log_io"] > 0 {
			ev = append(ev, fmt.Sprintf("Доля WRITELOG и связанных ожиданий: %.0f%%", pct["log_io"]))
		}
		if worstLog > 0 {
			ev = append(ev, fmt.Sprintf("Средняя задержка записи журнала: до %.0f мс (норма < 10–15 мс)", worstLog))
		}
		causes = append(causes, Cause{"warning", "Медленная запись журнала транзакций", ev,
			"Диск не успевает фиксировать транзакции — все записи в БД замедляются (ожидания WRITELOG).",
			[]string{"Перенести .ldf на более быстрый диск (SSD/NVMe), отдельно от данных.",
				"Проверить частые мелкие autogrowth журнала.",
				"Сократить объём/длительность транзакций приложения."}})
	}

	// 4. Блокировки
	if pct["locks"] > 10 || st.BlockedEvents > 0 || st.BlockedMax >= 3 {
		ev := []string{}
		if st.BlockedEvents > 0 {
			ev = append(ev, fmt.Sprintf("Заблокированные процессы наблюдались в %d замерах (максимум %d)",
				st.BlockedEvents, st.BlockedMax))
		}
		if pct["locks"] > 0 {
			ev = append(ev, fmt.Sprintf("Доля ожиданий блокировок (LCK_M_*): %.0f%%", pct["locks"]))
		}
		causes = append(causes, Cause{"warning", "Блокировки между транзакциями", ev,
			"Одни транзакции удерживают блокировки, другие ждут. Обычно виновата длинная транзакция (head blocker) или сканирования, захватывающие множество строк.",
			[]string{"Найти head-blocker в разделе «Блокировки» и оптимизировать его транзакцию.",
				"Сократить длительность транзакций в приложении (коммитить чаще).",
				"Рассмотреть READ_COMMITTED_SNAPSHOT для OLTP-баз.",
				"Оптимизировать запросы-жертвы (индексы)."}})
	}

	// 5. Гранты памяти
	if pct["memory"] > 5 || st.GrantsMax > 0 {
		causes = append(causes, Cause{"warning", "Конкуренция за память запросов",
			[]string{fmt.Sprintf("Доля ожиданий RESOURCE_SEMAPHORE и связанных: %.0f%%", pct["memory"]),
				fmt.Sprintf("Максимум ожидающих грантов: %s", fmtNum(st.GrantsMax))},
			"Тяжёлым запросам (сортировки, хеширования) не хватает памяти — они ждут освобождения грантов, время выполнения растёт.",
			[]string{"Оптимизировать запросы с большими сортировками/группировками.",
				"Обновить статистику (завышенные оценки → завышенные гранты).",
				"Проверить достаточность max server memory."}})
	}

	// 6. Сеть/клиент
	if pct["network"] > 15 {
		causes = append(causes, Cause{"info", "Клиентские приложения забирают данные медленно",
			[]string{fmt.Sprintf("Доля ASYNC_NETWORK_IO: %.0f%%", pct["network"])},
			"Сервер простаивает, ожидая, пока приложение прочитает результат запроса.",
			[]string{"Буферизовать чтение результатов на клиенте.",
				"Уменьшать объём выборок (TOP/WHERE/пагинация).",
				"Проверить сеть между приложением и SQL Server."}})
	}

	// 7. Бэкапы в рабочие часы
	if pct["backup"] > 8 {
		causes = append(causes, Cause{"info", "Резервное копирование конкурирует за ресурсы",
			[]string{fmt.Sprintf("Доля ожиданий BACKUPIO/BACKUPBUFFER: %.0f%%", pct["backup"])},
			"Копирование создаёт заметную нагрузку на диски в наблюдаемый период.",
			[]string{"Перенести бэкапы на ночное окно.", "Включить сжатие бэкапов.",
				"Проверить скорость целевого диска."}})
	}

	if len(causes) == 0 {
		causes = append(causes, Cause{"ok", "Явных узких мест не обнаружено",
			[]string{"Суммарные ожидания за период невелики и равномерно распределены."},
			"В наблюдаемый период нет выраженного дефицита процессора, памяти, дисков или блокировок. Если пользователи всё же жалуются на медленную работу — проверьте конкретные запросы и время их выполнения.",
			[]string{"Продолжать суточный мониторинг для накопления статистики.",
				"Изучить раздел «Топ запросов» — возможно, дело в конкретных запросах."}})
	}
	return causes
}

// MissingIndexScript генерирует CREATE INDEX по данным dm_db_missing_index_*.
func MissingIndexScript(mi map[string]any) string {
	table := orDash(sstr(mi["table_ref"]))
	eq := strings.TrimSpace(sstr(mi["equality_columns"]))
	ineq := strings.TrimSpace(sstr(mi["inequality_columns"]))
	inc := strings.TrimSpace(sstr(mi["included_columns"]))
	keys := joinNonEmpty(eq, ineq)
	if keys == "" {
		keys = "[id]"
	}
	name := "IX_" + strings.NewReplacer("[", "", "]", "", ".", "_", " ", "_").Replace(table)
	if len(name) > 90 {
		name = name[:90]
	}
	script := fmt.Sprintf("USE [%s];\nCREATE NONCLUSTERED INDEX [%s]\n    ON %s (%s)",
		sstr(mi["db_name"]), name, table, keys)
	if inc != "" {
		script += "\n    INCLUDE (" + inc + ")"
	}
	script += ";\n-- Перед созданием проверьте на тестовой среде и объедините с существующими индексами."
	return script
}
