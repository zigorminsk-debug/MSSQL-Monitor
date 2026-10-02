package main

// Сбор показателей с SQL Server: лёгкие (частые) и полные (тяжёлые) снимки.
// Работает с любым Querier — реальной БД или демо-движком.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// CPUInfo — загрузка ЦП, %.
type CPUInfo struct {
	SQL   float64 `json:"sql"`
	Other float64 `json:"other"`
	Idle  float64 `json:"idle"`
}

// LivePoints — точечные счётчики.
type LivePoints struct {
	PLE           *float64 `json:"ple"`
	Connections   *float64 `json:"connections"`
	Blocked       *float64 `json:"blocked"`
	GrantsPending *float64 `json:"grants_pending"`
	TargetMemMB   *float64 `json:"target_mem_mb"`
	TotalMemMB    *float64 `json:"total_mem_mb"`
	StolenMemMB   *float64 `json:"stolen_mem_mb"`
	BufferHitPct  *float64 `json:"buffer_hit_pct"`
}

// WaitDelta — приращение ожидания за интервал.
type WaitDelta struct {
	Type     string  `json:"wait_type"`
	WaitMS   float64 `json:"wait_ms"`
	SignalMS float64 `json:"signal_ms"`
	Tasks    float64 `json:"tasks"`
}

// RequestInfo — активный запрос.
type RequestInfo struct {
	SessionID         int64  `json:"session_id"`
	Status            string `json:"status"`
	BlockingSessionID int64  `json:"blocking_session_id"`
	WaitType          string `json:"wait_type"`
	WaitTimeMS        int64  `json:"wait_time"`
	WaitResource      string `json:"wait_resource"`
	CPUTimeMS         int64  `json:"cpu_time"`
	ElapsedMS         int64  `json:"total_elapsed_time"`
	Reads             int64  `json:"reads"`
	Writes            int64  `json:"writes"`
	LogicalReads      int64  `json:"logical_reads"`
	Command           string `json:"command"`
	DBName            string `json:"db_name"`
	HostName          string `json:"host_name"`
	LoginName         string `json:"login_name"`
	ProgramName       string `json:"program_name"`
	QueryText         string `json:"query_text"`
}

// BlockingChain — цепочка блокирования.
type BlockingChain struct {
	Head        int64  `json:"head"`
	HeadQuery   string `json:"head_query"`
	HeadLogin   string `json:"head_login"`
	HeadProgram string `json:"head_program"`
	Victims     int    `json:"victims"`
	MaxWaitMS   int64  `json:"max_wait_ms"`
	VictimQuery string `json:"victim_query"`
}

// SlowQueryRec — медленный запрос (снимок в моменте).
type SlowQueryRec struct {
	SessionID    int64  `json:"session_id"`
	ElapsedMS    int64  `json:"elapsed_ms"`
	CPUMS        int64  `json:"cpu_ms"`
	LogicalReads int64  `json:"logical_reads"`
	DBName       string `json:"db_name"`
	Text         string `json:"text"`
	WaitType     string `json:"wait_type"`
}

// BlockingInfo — блокировки в снимке.
type BlockingInfo struct {
	Count  int             `json:"count"`
	Chains []BlockingChain `json:"chains"`
}

// Sample — лёгкий снимок (раз в интервал).
type Sample struct {
	TS         time.Time          `json:"ts"`
	Source     string             `json:"source"`
	CPU        CPUInfo            `json:"cpu"`
	Points     LivePoints         `json:"points"`
	Rates      map[string]float64 `json:"rates"`
	WaitsDelta []WaitDelta        `json:"waits_delta"`
	Requests   []RequestInfo      `json:"requests"`
	Blocking   BlockingInfo       `json:"blocking"`
	Slow       []SlowQueryRec     `json:"slow"`
}

// FileStat — файл БД со скоростями и задержками.
type FileStat struct {
	DBName         string  `json:"db_name"`
	FileName       string  `json:"file_name"`
	TypeDesc       string  `json:"type_desc"`
	SizeMB         float64 `json:"size_mb"`
	PhysicalName   string  `json:"physical_name"`
	ReadsSec       float64 `json:"reads_sec"`
	WritesSec      float64 `json:"writes_sec"`
	MBReadSec      float64 `json:"mb_read_sec"`
	MBWriteSec     float64 `json:"mb_write_sec"`
	ReadLatencyMS  float64 `json:"read_latency_ms"`
	WriteLatencyMS float64 `json:"write_latency_ms"`
}

// Snapshot — полный снимок (лёгкий + тяжёлые запросы).
type Snapshot struct {
	Sample
	Instance    map[string]string
	SysInfo     map[string]any
	OSMem       map[string]any
	Config      map[string]string
	Databases   []map[string]any
	Files       []FileStat
	FilesMode   string
	PerDBMem    []map[string]any // память (буферный кэш) по базам
	PerDBCPU    []map[string]any // накопленный ЦП по базам (кэш планов)
	TempdbFiles int
	FileCfg     []map[string]any
	Backups     []map[string]any
	CachedPlans []map[string]any
	TopQueries  []map[string]any
	MissingIdx  []map[string]any
	Frag        []map[string]any
	StaleStats  []map[string]any
	VLF         []map[string]any
	Errors      []string
}

// ---------------------------------------------------------------------------
var deltaCounters = map[string]string{
	"batch_sec":          "Batch Requests/sec",
	"compilations_sec":   "SQL Compilations/sec",
	"recompilations_sec": "SQL Re-Compilations/sec",
	"trans_sec":          "Transactions/sec",
	"page_reads_sec":     "Page reads/sec",
	"page_writes_sec":    "Page writes/sec",
	"log_flushes_sec":    "Log Flushes/sec",
	"lock_waits_sec":     "Lock Waits/sec",
	"full_scans_sec":     "Full Scans/sec",
	"index_searches_sec": "Index Searches/sec",
	"errors_sec":         "Errors/sec",
	"page_splits_sec":    "Page Splits/sec",
	"lazy_writes_sec":    "Lazy Writes/sec",
}

var pointCounters = map[string]string{
	"ple":            "Page Life Expectancy",
	"connections":    "User Connections",
	"blocked":        "Processes blocked",
	"grants_pending": "Memory Grants Pending",
}

var memCounters = map[string]string{
	"target_mem_mb": "Target Server Memory (KB)",
	"total_mem_mb":  "Total Server Memory (KB)",
	"stolen_mem_mb": "Stolen Server Memory (KB)",
}

// pickCounter выбирает значение счётчика (приоритет экземпляра _Total, затем ”).
func pickCounter(rows []map[string]any, name string) *float64 {
	var first, empty *float64
	for _, r := range rows {
		if sstr(r["counter_name"]) != name {
			continue
		}
		v := toF(r["cntr_value"])
		switch sstr(r["instance_name"]) {
		case "_Total":
			return &v
		case "":
			if empty == nil {
				empty = &v
			}
		}
		if first == nil {
			first = &v
		}
	}
	if empty != nil {
		return empty
	}
	return first
}

// Collector собирает снимки.
type Collector struct {
	q          Querier
	settings   Settings
	prevTS     time.Time
	prevCnt    map[string]float64
	prevWaits  map[string][3]float64 // type -> tasks, wait_ms, signal_ms
	prevFiles  map[string][6]float64 // "db|file" -> reads, writes, mbR, mbW, stallR, stallW
	prevFilesT time.Time
	errs       *[]string
}

func NewCollector(q Querier, s Settings) *Collector {
	return &Collector{q: q, settings: s}
}

// Tick продвигает демо-движок (для реальной БД — no-op).
func (c *Collector) Tick() {
	if t, ok := c.q.(interface{ Tick() }); ok {
		t.Tick()
	}
}

// now — виртуальное или реальное время.
func (c *Collector) now() time.Time {
	if ck, ok := c.q.(Clock); ok {
		return ck.Now()
	}
	return time.Now()
}

func (c *Collector) try(ctx context.Context, key, sql string) []map[string]any {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	rows, err := c.q.Query(ctx, sql)
	if err != nil {
		desc := queryDescriptions[key]
		logf("Не удалось получить %s: %v", desc, err)
		if c.errs != nil {
			*c.errs = append(*c.errs, fmt.Sprintf("%s: %s", desc, shortErr(err)))
		}
		return nil
	}
	return rows
}

func shortErr(err error) string {
	msg := strings.ToUpper(err.Error())
	if strings.Contains(msg, "VIEW SERVER STATE") || strings.Contains(msg, "WAS DENIED") {
		return "нет прав VIEW SERVER STATE (данные неполные)"
	}
	if strings.Contains(msg, "INVALID OBJECT NAME") {
		return "объект не поддерживается этой версией SQL Server"
	}
	s := err.Error()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// LightSample — быстрый снимок (ЦП, счётчики, ожидания, запросы, блокировки).
func (c *Collector) LightSample(ctx context.Context) Sample {
	ts := c.now()
	cpuRows := c.try(ctx, "Q_CPU", qCPU)
	cntRows := c.try(ctx, "Q_COUNTERS", qCounters)
	waitRows := c.try(ctx, "Q_WAITS", qWaits)
	reqRows := c.try(ctx, "Q_REQUESTS", qRequests)

	smp := Sample{TS: ts, Source: "live", Rates: map[string]float64{}}
	if c.q != nil {
		if _, isDemo := c.q.(*DemoEngine); isDemo {
			smp.Source = "demo"
		}
	}

	// ЦП
	cpu := CPUInfo{}
	if len(cpuRows) > 0 {
		sqlCPU := toF(cpuRows[0]["sql_cpu"])
		idle := toF(cpuRows[0]["system_idle"])
		cpu.SQL = sqlCPU
		cpu.Idle = idle
		cpu.Other = clamp(100-idle-sqlCPU, 0, 100)
	}
	smp.CPU = cpu

	// точечные счётчики и память
	counters := map[string]float64{}
	for k, name := range pointCounters {
		if v := pickCounter(cntRows, name); v != nil {
			counters[k] = *v
		}
	}
	for k, name := range memCounters {
		if v := pickCounter(cntRows, name); v != nil {
			counters[k] = *v / 1024 // KB -> MB
		}
	}
	p := LivePoints{}
	for _, k := range []string{"ple", "connections", "blocked", "grants_pending",
		"target_mem_mb", "total_mem_mb", "stolen_mem_mb"} {
		if v, ok := counters[k]; ok {
			f := v
			switch k {
			case "ple":
				p.PLE = &f
			case "connections":
				p.Connections = &f
			case "blocked":
				p.Blocked = &f
			case "grants_pending":
				p.GrantsPending = &f
			case "target_mem_mb":
				p.TargetMemMB = &f
			case "total_mem_mb":
				p.TotalMemMB = &f
			case "stolen_mem_mb":
				p.StolenMemMB = &f
			}
		}
	}
	if r := pickCounter(cntRows, "Buffer cache hit ratio"); r != nil {
		if b := pickCounter(cntRows, "Buffer cache hit ratio base"); b != nil && *b != 0 {
			v := clamp(*r / *b * 100, 0, 100)
			p.BufferHitPct = &v
		}
	}
	smp.Points = p

	// кумулятивы для дельт
	cum := map[string]float64{}
	for k, name := range deltaCounters {
		if v := pickCounter(cntRows, name); v != nil {
			cum[k] = *v
		}
	}

	// скорости и ожидания как дельты от прошлого снимка
	waitsDelta := []WaitDelta{}
	if !c.prevTS.IsZero() {
		dt := ts.Sub(c.prevTS).Seconds()
		if dt > 0 {
			for k := range deltaCounters {
				cur, ok1 := cum[k]
				old, ok2 := c.prevCnt[k]
				if ok1 && ok2 && cur >= old {
					smp.Rates[k] = mathMax(0, (cur-old)/dt)
				}
			}
			for _, w := range waitRows {
				wt := sstr(w["wait_type"])
				if isIgnorableWait(wt) {
					continue
				}
				old, ok := c.prevWaits[wt]
				if !ok {
					continue
				}
				dMS := toF(w["wait_time_ms"]) - old[1]
				if dMS > 0.5 {
					waitsDelta = append(waitsDelta, WaitDelta{
						Type: wt, WaitMS: dMS,
						SignalMS: mathMax(0, toF(w["signal_wait_time_ms"])-old[2]),
						Tasks:    mathMax(0, toF(w["waiting_tasks_count"])-old[0]),
					})
				}
			}
		}
	}
	smp.WaitsDelta = waitsDelta

	// head-blocker'ы
	reqs := make([]RequestInfo, 0, len(reqRows))
	blockedIDs := map[int64]bool{}
	for _, r := range reqRows {
		ri := requestFromRow(r)
		reqs = append(reqs, ri)
		if ri.BlockingSessionID > 0 {
			blockedIDs[ri.BlockingSessionID] = true
		}
	}
	smp.Requests = reqs
	headInfo := map[int64]map[string]any{}
	if len(blockedIDs) > 0 {
		ids := make([]string, 0, len(blockedIDs))
		for id := range blockedIDs {
			ids = append(ids, fmt.Sprintf("%d", id))
		}
		sort.Strings(ids)
		if len(ids) > 32 {
			ids = ids[:32]
		}
		rows := c.try(ctx, "Q_HEAD_BLOCKERS", fmt.Sprintf(qHeadBlockers, strings.Join(ids, ", ")))
		for _, r := range rows {
			headInfo[int64(toI(r["session_id"]))] = r
		}
	}

	finalizeSample(&smp, c.settings.SlowQueryMS)

	// сохранить кумулятивы
	c.prevTS = ts
	c.prevCnt = cum
	c.prevWaits = map[string][3]float64{}
	for _, w := range waitRows {
		c.prevWaits[sstr(w["wait_type"])] = [3]float64{
			toF(w["waiting_tasks_count"]), toF(w["wait_time_ms"]), toF(w["signal_wait_time_ms"])}
	}
	return smp
}

func requestFromRow(r map[string]any) RequestInfo {
	return RequestInfo{
		SessionID:         toI(r["session_id"]),
		Status:            sstr(r["status"]),
		BlockingSessionID: toI(r["blocking_session_id"]),
		WaitType:          sstr(r["wait_type"]),
		WaitTimeMS:        toI(r["wait_time"]),
		WaitResource:      sstr(r["wait_resource"]),
		CPUTimeMS:         toI(r["cpu_time"]),
		ElapsedMS:         toI(r["total_elapsed_time"]),
		Reads:             toI(r["reads"]),
		Writes:            toI(r["writes"]),
		LogicalReads:      toI(r["logical_reads"]),
		Command:           sstr(r["command"]),
		DBName:            sstr(r["db_name"]),
		HostName:          sstr(r["host_name"]),
		LoginName:         sstr(r["login_name"]),
		ProgramName:       sstr(r["program_name"]),
		QueryText:         sstr(r["query_text"]),
	}
}

// finalizeSample достраивает блокировки и медленные запросы.
func finalizeSample(s *Sample, slowMS int) {
	slowMS = int(mathMax(1000, float64(slowMS)))
	blocked := []RequestInfo{}
	for _, r := range s.Requests {
		if r.BlockingSessionID > 0 {
			blocked = append(blocked, r)
		}
	}
	chains := []BlockingChain{}
	if len(blocked) > 0 {
		blockedIDs := map[int64]bool{}
		for _, r := range blocked {
			blockedIDs[r.SessionID] = true
		}
		headIDs := map[int64]bool{}
		for _, r := range blocked {
			if !blockedIDs[r.BlockingSessionID] {
				headIDs[r.BlockingSessionID] = true
			}
		}
		if len(headIDs) == 0 { // цикл — все блокирующие сами заблокированы
			var maxWait int64
			for _, r := range blocked {
				if r.WaitTimeMS > maxWait {
					maxWait = r.WaitTimeMS
				}
			}
			chains = append(chains, BlockingChain{Head: 0, HeadQuery: "—",
				Victims: len(blocked), MaxWaitMS: maxWait})
		} else {
			for _, hid := range sortedKeys(headIDs) {
				victims := []RequestInfo{}
				for _, r := range blocked {
					if r.BlockingSessionID == hid {
						victims = append(victims, r)
					}
				}
				ch := BlockingChain{Head: hid, Victims: len(victims)}
				var maxWait int64
				for _, v := range victims {
					if v.WaitTimeMS > maxWait {
						maxWait = v.WaitTimeMS
					}
				}
				ch.MaxWaitMS = maxWait
				if len(victims) > 0 {
					ch.VictimQuery = truncStr(victims[0].QueryText, 800)
				}
				chains = append(chains, ch)
			}
		}
	}
	s.Blocking = BlockingInfo{Count: len(blocked), Chains: chains}

	s.Slow = []SlowQueryRec{}
	for _, r := range s.Requests {
		if r.ElapsedMS >= int64(slowMS) {
			s.Slow = append(s.Slow, SlowQueryRec{
				SessionID: r.SessionID, ElapsedMS: r.ElapsedMS, CPUMS: r.CPUTimeMS,
				LogicalReads: r.LogicalReads, DBName: r.DBName,
				Text: truncStr(r.QueryText, 1500), WaitType: r.WaitType,
			})
		}
	}
}

func sortedKeys(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func mathMax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// FullSnapshot — полный снимок для отчёта.
func (c *Collector) FullSnapshot(ctx context.Context) *Snapshot {
	errs := []string{}
	c.errs = &errs
	smp := c.LightSample(ctx)
	c.errs = nil

	snap := &Snapshot{Sample: smp, Errors: errs,
		Instance: map[string]string{}, SysInfo: map[string]any{}, OSMem: map[string]any{},
		Config: map[string]string{}, Databases: []map[string]any{}, Files: []FileStat{},
		FileCfg: []map[string]any{}, Backups: []map[string]any{}, CachedPlans: []map[string]any{},
		TopQueries: []map[string]any{}, MissingIdx: []map[string]any{}, Frag: []map[string]any{},
		StaleStats: []map[string]any{}, VLF: []map[string]any{}}

	if rows := c.try(ctx, "Q_INSTANCE", qInstance); len(rows) > 0 {
		for k, v := range rows[0] {
			snap.Instance[k] = sstr(v)
		}
	}
	sysRows := c.try(ctx, "Q_SYS_INFO", qSysInfo)
	if sysRows == nil {
		sysRows = c.try(ctx, "Q_SYS_INFO", qSysInfoOld)
	}
	if len(sysRows) > 0 {
		snap.SysInfo = sysRows[0]
	}
	if rows := c.try(ctx, "Q_OS_MEMORY", qOSMemory); len(rows) > 0 {
		snap.OSMem = rows[0]
	}
	snap.Databases = c.try(ctx, "Q_DATABASES", qDatabases)
	if snap.Databases == nil {
		snap.Databases = []map[string]any{}
	}
	for _, r := range c.try(ctx, "Q_CONFIG", qConfig) {
		snap.Config[sstr(r["name"])] = sstr(r["value_in_use"])
	}
	snap.FileCfg = orEmpty(c.try(ctx, "Q_FILE_CFG", qFileCfg))
	snap.Backups = orEmpty(c.try(ctx, "Q_BACKUPS", qBackups))
	snap.CachedPlans = orEmpty(c.try(ctx, "Q_CACHED_PLANS", qCachedPlans))
	snap.TopQueries = orEmpty(c.try(ctx, "Q_TOP_QUERIES", qTopQueries))
	if rows := c.try(ctx, "Q_TEMPDB_FILES", qTempdbFiles); len(rows) > 0 {
		snap.TempdbFiles = int(toI(rows[0]["data_files"]))
	}

	// файлы: дельты с прошлого полного снимка или средние с запуска сервера
	files := c.try(ctx, "Q_FILES", qFiles)
	hadPrev := c.prevFiles != nil
	snap.Files = c.filesRates(files, toF(snap.SysInfo["uptime_sec"]))
	if hadPrev {
		snap.FilesMode = "window"
	} else {
		snap.FilesMode = "startup"
	}

	// потребление ресурсов по базам данных
	snap.PerDBMem = orEmpty(c.try(ctx, "Q_BUFFER_PER_DB", qBufferPerDB))
	snap.PerDBCPU = orEmpty(c.try(ctx, "Q_CPU_PER_DB", qCPUPerDB))

	// тяжёлые запросы по базам
	if c.settings.DeepIndexScan {
		for _, db := range snap.Databases {
			name := sstr(db["name"])
			if name == "master" || name == "model" || name == "msdb" || name == "tempdb" ||
				sstr(db["state_desc"]) != "ONLINE" || bval(db["is_read_only"]) {
				continue
			}
			for _, r := range orEmpty(c.try(ctx, "Q_MISSING_INDEX_DB", forDB(name, qMissingIndexDB))) {
				r["db_name"] = name
				snap.MissingIdx = append(snap.MissingIdx, r)
			}
			for _, r := range orEmpty(c.try(ctx, "Q_FRAG_DB", forDB(name, qFragDB))) {
				r["db_name"] = name
				snap.Frag = append(snap.Frag, r)
			}
			for _, r := range orEmpty(c.try(ctx, "Q_STALE_STATS_DB", forDB(name, qStaleStatsDB))) {
				r["db_name"] = name
				snap.StaleStats = append(snap.StaleStats, r)
			}
			for _, r := range orEmpty(c.try(ctx, "Q_VLF_DB", forDB(name, qVLFDB))) {
				r["db_name"] = name
				snap.VLF = append(snap.VLF, r)
			}
		}
		sort.SliceStable(snap.MissingIdx, func(i, j int) bool {
			return toF(snap.MissingIdx[i]["score"]) > toF(snap.MissingIdx[j]["score"])
		})
		if len(snap.MissingIdx) > 8 {
			snap.MissingIdx = snap.MissingIdx[:8]
		}
		sort.SliceStable(snap.Frag, func(i, j int) bool {
			return toI(snap.Frag[i]["page_count"]) > toI(snap.Frag[j]["page_count"])
		})
		if len(snap.Frag) > 10 {
			snap.Frag = snap.Frag[:10]
		}
		sort.SliceStable(snap.StaleStats, func(i, j int) bool {
			return toF(snap.StaleStats[i]["modification_counter"])/mathMax(1, toF(snap.StaleStats[i]["rows"])) >
				toF(snap.StaleStats[j]["modification_counter"])/mathMax(1, toF(snap.StaleStats[j]["rows"]))
		})
		if len(snap.StaleStats) > 10 {
			snap.StaleStats = snap.StaleStats[:10]
		}
	}
	return snap
}

func orEmpty(rows []map[string]any) []map[string]any {
	if rows == nil {
		return []map[string]any{}
	}
	return rows
}

// filesRates считает скорости и задержки по файлам.
func (c *Collector) filesRates(rows []map[string]any, uptimeSec float64) []FileStat {
	out := []FileStat{}
	now := c.now()
	window := 0.0
	if c.prevFiles != nil {
		window = now.Sub(c.prevFilesT).Seconds()
	}
	for _, r := range rows {
		key := sstr(r["db_name"]) + "|" + sstr(r["file_name"])
		fs := FileStat{
			DBName: sstr(r["db_name"]), FileName: sstr(r["file_name"]),
			TypeDesc: sstr(r["type_desc"]), SizeMB: toF(r["size_mb"]),
			PhysicalName: sstr(r["physical_name"]),
		}
		cur := [6]float64{toF(r["num_of_reads"]), toF(r["num_of_writes"]),
			toF(r["mb_read"]), toF(r["mb_written"]),
			toF(r["io_stall_read_ms"]), toF(r["io_stall_write_ms"])}
		old, hasOld := c.prevFiles[key]
		if hasOld && window > 0 {
			dr := mathMax(0, cur[0]-old[0])
			dw := mathMax(0, cur[1]-old[1])
			dsr := mathMax(0, cur[4]-old[4])
			dsw := mathMax(0, cur[5]-old[5])
			dmr := mathMax(0, cur[2]-old[2])
			dmw := mathMax(0, cur[3]-old[3])
			fs.ReadsSec, fs.WritesSec = dr/window, dw/window
			fs.MBReadSec, fs.MBWriteSec = dmr/window, dmw/window
			if dr > 0 {
				fs.ReadLatencyMS = dsr / dr
			}
			if dw > 0 {
				fs.WriteLatencyMS = dsw / dw
			}
		} else {
			t := mathMax(1, uptimeSec)
			fs.ReadsSec, fs.WritesSec = cur[0]/t, cur[1]/t
			fs.MBReadSec, fs.MBWriteSec = cur[2]/t, cur[3]/t
			if cur[0] > 0 {
				fs.ReadLatencyMS = cur[4] / cur[0]
			}
			if cur[1] > 0 {
				fs.WriteLatencyMS = cur[5] / cur[1]
			}
		}
		fs.ReadsSec = mathRound2(fs.ReadsSec)
		fs.WritesSec = mathRound2(fs.WritesSec)
		fs.MBReadSec = mathRound2(fs.MBReadSec)
		fs.MBWriteSec = mathRound2(fs.MBWriteSec)
		fs.ReadLatencyMS = mathRound2(fs.ReadLatencyMS)
		fs.WriteLatencyMS = mathRound2(fs.WriteLatencyMS)
		out = append(out, fs)
	}
	c.prevFiles = map[string][6]float64{}
	for _, r := range rows {
		c.prevFiles[sstr(r["db_name"])+"|"+sstr(r["file_name"])] = [6]float64{
			toF(r["num_of_reads"]), toF(r["num_of_writes"]), toF(r["mb_read"]),
			toF(r["mb_written"]), toF(r["io_stall_read_ms"]), toF(r["io_stall_write_ms"])}
	}
	c.prevFilesT = now
	return out
}

func mathRound2(v float64) float64 { return mathRound(v*100) / 100 }

func mathRound(v float64) float64 {
	if v < 0 {
		return math.Floor(v - 0.5)
	}
	return math.Floor(v + 0.5)
}
