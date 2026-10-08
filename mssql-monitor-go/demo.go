package main

// Демо-движок: реалистичная имитация нагруженного SQL Server (SQLSRV-PROD-01).
// Реализует интерфейс Querier — отвечает на те же SQL-запросы, что и реальная БД,
// поэтому демонстрационный режим проходит весь боевой путь коллектора.

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// Clock — источник времени коллектора (демо подставляет виртуальные часы).
type Clock interface{ Now() time.Time }

// DemoEngine — имитация сервера.
type DemoEngine struct {
	mu        sync.Mutex
	realtime  bool
	interval  time.Duration
	sim       time.Time
	lastT     time.Time
	rnd       *rand.Rand
	cum       map[string]float64 // кумулятивные счётчики (как dm_os_performance_counters)
	waitCum   map[string]float64 // кумулятивные ожидания, мс
	blocking  bool               // активна ли цепочка блокирования
	victims   int
	uptimeDay int
}

// NewDemoEngine создаёт движок. historyHours>0 — режим генерации истории (виртуальные
// сутки), иначе — live-режим (время реальное, двигается Tick'ом мониторинга).
func NewDemoEngine(intervalSec int, historyHours int) *DemoEngine {
	now := time.Now()
	e := &DemoEngine{
		realtime:  historyHours <= 0,
		interval:  time.Duration(intervalSec) * time.Second,
		rnd:       rand.New(rand.NewSource(42)),
		cum:       map[string]float64{},
		waitCum:   map[string]float64{},
		uptimeDay: 45,
	}
	if historyHours > 0 {
		e.sim = now.Add(-time.Duration(historyHours) * time.Hour)
	} else {
		e.sim = now
	}
	e.lastT = e.sim
	// стартовая база: 45 дней работы со средней нагрузкой
	uptimeSec := float64(e.uptimeDay * 86400)
	for k, v := range e.rateMap(0.35) {
		e.cum[k] = v * uptimeSec
	}
	e.cum["batch"] += 18_400_000
	e.prefillWaits(uptimeSec)
	return e
}

func (e *DemoEngine) Name() string { return "демо-данные (SQLSRV-PROD-01)" }
func (e *DemoEngine) Close()       {}

// Now — виртуальное время.
func (e *DemoEngine) Now() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sim
}

// Tick продвигает виртуальное время и обновляет кумулятивные счётчики.
func (e *DemoEngine) Tick() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.realtime {
		e.sim = time.Now()
	} else {
		e.sim = e.sim.Add(e.interval)
	}
	dt := e.sim.Sub(e.lastT).Seconds()
	if dt <= 0 {
		return
	}
	mid := e.lastT.Add(e.sim.Sub(e.lastT) / 2)
	for k, v := range e.rateMapAt(mid) {
		e.cum[k] += v * dt
	}
	for wt, v := range e.waitRates(mid) {
		e.waitCum[wt] += v * dt
	}
	e.lastT = e.sim
	h := float64(e.sim.Hour()) + float64(e.sim.Minute())/60
	e.blocking = (10.9 <= h && h < 12.4) && e.rnd.Float64() < 0.22
	if e.blocking {
		e.victims = 2 + e.rnd.Intn(10)
	} else {
		e.victims = 0
	}
}

// ------------------------------------------------------------------ нагрузка
func (e *DemoEngine) load(t time.Time) float64 {
	h := float64(t.Hour()) + float64(t.Minute())/60 + float64(t.Second())/3600
	peak := func(c, w, a float64) float64 { return a * math.Exp(-((h-c)*(h-c))/(2*w*w)) }
	l := 0.05 + peak(9.6, 1.6, 0.55) + peak(11.4, 0.8, 0.80) + peak(15.5, 1.3, 0.95) + peak(18.3, 0.9, 0.28)
	if h >= 3 && h < 4 {
		l += 0.25
	}
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		l *= 0.35
	}
	l *= 0.88 + 0.24*e.rnd.Float64()
	return clamp(l, 0.02, 1.0)
}

// rateMap — мгновенные скорости счётчиков (в секунду) при нагрузке load.
func (e *DemoEngine) rateMap(load float64) map[string]float64 {
	n := func() float64 { return 1 + e.rnd.NormFloat64()*0.08 }
	batch := (15 + 780*load) * n()
	compRatio := 0.05
	if h := float64(e.sim.Hour()) + float64(e.sim.Minute())/60; h >= 14 && h < 16.5 {
		compRatio = 0.21
	}
	lw := 0.02
	if h := float64(e.sim.Hour()) + float64(e.sim.Minute())/60; h >= 10.8 && h < 12.4 {
		lw = 2.6 * load
	}
	return map[string]float64{
		"batch":   math.Max(2, batch),
		"trans":   math.Max(3, 2.4*batch),
		"comp":    math.Max(1, batch*compRatio*n()),
		"recomp":  math.Max(0, batch*compRatio*0.12),
		"preads":  math.Max(1, (620*load+10)*n()),
		"pwrites": math.Max(0.3, 240*load),
		"lflush":  math.Max(1, 170*load*n()),
		"lwaits":  lw,
		"fscans":  math.Max(0.5, 95*load),
		"isearch": math.Max(10, 2600*load),
		"psplits": math.Max(0.1, 24*load),
		"lazy":    math.Max(0, 60*load*(load-0.55)),
	}
}

func (e *DemoEngine) rateMapAt(t time.Time) map[string]float64 {
	saved := e.sim
	e.sim = t
	m := e.rateMap(e.load(t))
	e.sim = saved
	return m
}

// waitRates — скорость накопления ожиданий (мс/сек) в момент t.
func (e *DemoEngine) waitRates(t time.Time) map[string]float64 {
	load := e.load(t)
	h := float64(t.Hour()) + float64(t.Minute())/60
	w := map[string]float64{
		"PAGEIOLATCH_SH":      640 * load / 30,
		"PAGEIOLATCH_EX":      95 * load / 30,
		"WRITELOG":            235 * (0.35 + load) / 30,
		"LCK_M_S":             18 * load / 30,
		"ASYNC_NETWORK_IO":    300 * load / 30,
		"SOS_SCHEDULER_YIELD": 210 * load / 30,
		"BACKUPIO":            4 / 30,
		"BACKUPBUFFER":        2 / 30,
	}
	if h >= 10.8 && h < 12.4 {
		w["LCK_M_S"] = 1250 * load / 30
	}
	if h >= 13.8 && h < 16.8 {
		w["CXPACKET"] = 1750 * load / 30
		w["CXCONSUMER"] = 640 * load / 30
	} else {
		w["CXPACKET"] = 120 * load / 30
		w["CXCONSUMER"] = 60 * load / 30
	}
	if h >= 10.5 && h < 15.5 {
		w["RESOURCE_SEMAPHORE"] = 170 * load / 30
	}
	if load > 0.85 {
		w["THREADPOOL"] = 140 * load / 30
	}
	if h >= 3 && h < 4 {
		w["BACKUPIO"] = 2400 / 30
		w["BACKUPBUFFER"] = 900 / 30
	}
	// «мусорные» ожидания — должны отфильтровываться коллектором
	w["SLEEP_TASK"] = 60
	w["BROKER_TASK_STOP"] = 40
	w["LAZYWRITER_SLEEP"] = 1000
	return w
}

func (e *DemoEngine) prefillWaits(uptimeSec float64) {
	// средняя интенсивность за сутки при типовой нагрузке
	t := time.Date(2000, 1, 3, 12, 0, 0, 0, time.UTC) // условный полдень
	for wt, v := range e.waitRates(t) {
		e.waitCum[wt] += v * uptimeSec * 0.5
	}
}

type fileRate struct{ readsPS, writesPS, mbRPS, mbWPS, readLat, writeLat float64 }

func (e *DemoEngine) fileRates(load float64) map[string]fileRate {
	return map[string]fileRate{
		"SalesDb_data01": {210*load + 3, 85*load + 1, 1.9*load + 0.02, 0.6*load + 0.01,
			clamp(17.5*load+2, 2, 40), clamp(7*load+1, 1, 20)},
		"SalesDb_log": {2 + 3*load, 170*load + 2, 0.01, 3.4*load + 0.05,
			4, clamp(44*load+6, 4, 70)},
		"ReportsDb_data": {400*load + 4, 30*load + 0.5, 4.2*load + 0.05, 0.2*load + 0.01,
			clamp(24*load+3, 2, 48), 6},
		"tempdev": {60*load + 2, 310*load + 5, 0.4 * load, 2.2*load + 0.02,
			clamp(5*load+1, 0.5, 12), clamp(9*load+1, 1, 20)},
		"templog": {0.4, 120*load + 3, 0, 1.1*load + 0.01, 3, clamp(12*load+2, 1, 25)},
		"StatsDb_data": {260*load + 2, 140*load + 2, 2.1 * load, 1.4*load + 0.01,
			clamp(28*load+4, 2, 55), clamp(11*load+2, 1, 25)},
	}
}

// ------------------------------------------------------------------ запросы
// Query отвечает на SQL коллектора, опознавая запрос по содержимому.
func (e *DemoEngine) Query(_ context.Context, sql string) ([]map[string]any, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case strings.Contains(sql, "RING_BUFFER_SCHEDULER_MONITOR"):
		load := e.load(e.sim)
		cpu := clamp(5+88*load+e.rnd.NormFloat64()*3, 2, 97)
		other := clamp(2+9*load+e.rnd.NormFloat64()*1.5, 0.5, 22)
		idle := clamp(100-cpu-other, 0, 100)
		return []map[string]any{{
			"sql_cpu": math.Round(cpu), "system_idle": math.Round(idle),
		}}, nil

	case strings.Contains(sql, "dm_exec_input_buffer"): // ДО dm_exec_requests!
		if !e.blocking {
			return []map[string]any{}, nil
		}
		return []map[string]any{{
			"session_id": 58, "login_name": "CORP\\svc_1c", "host_name": "APP-SRV-03",
			"program_name": "1C:Enterprise 8.3", "session_status": "sleeping",
			"blocking_session_id": nil,
			"input_buffer": "UPDATE dbo.Warehouse.StockUnits SET ReservedQty = ReservedQty + @p1 " +
				"WHERE Sku IN (SELECT Sku FROM dbo.Warehouse.Reserve WHERE OrderId = @p2)",
		}}, nil

	case strings.Contains(sql, "dm_exec_requests"):
		return e.requestsRows(), nil

	case strings.Contains(sql, "dm_os_performance_counters"):
		rows := []map[string]any{}
		mk := func(name, inst string, val float64) {
			rows = append(rows, map[string]any{
				"object_name": "SQLServer:X", "counter_name": name, "instance_name": inst,
				"cntr_value": math.Round(val), "cntr_type": int64(272696576)})
		}
		mk("Batch Requests/sec", "", e.cum["batch"])
		mk("SQL Compilations/sec", "", e.cum["comp"])
		mk("SQL Re-Compilations/sec", "", e.cum["recomp"])
		// два экземпляра: должен выбраться _Total
		mk("Transactions/sec", "SalesDb", e.cum["trans"]/3)
		mk("Transactions/sec", "_Total", e.cum["trans"])
		mk("Page reads/sec", "", e.cum["preads"])
		mk("Page writes/sec", "", e.cum["pwrites"])
		mk("Log Flushes/sec", "", e.cum["lflush"])
		mk("Lock Waits/sec", "", e.cum["lwaits"])
		mk("Full Scans/sec", "", e.cum["fscans"])
		mk("Index Searches/sec", "", e.cum["isearch"])
		mk("Errors/sec", "", 5)
		mk("Page Splits/sec", "", e.cum["psplits"])
		mk("Lazy Writes/sec", "", e.cum["lazy"])
		mk("Page Life Expectancy", "", 0) // заменяется ниже
		rows[len(rows)-1]["cntr_value"] = int64(clamp(2500-2150*e.load(e.sim)+e.rnd.NormFloat64()*90, 35, 3600))
		conn := 35 + 230*e.load(e.sim) + e.rnd.NormFloat64()*8
		mk("User Connections", "", conn)
		blk := 0.0
		if e.blocking {
			blk = float64(e.victims)
		}
		mk("Processes blocked", "", blk)
		mk("Memory Grants Pending", "", map[bool]float64{true: 1 + 2*e.load(e.sim), false: 0}[e.load(e.sim) > 0.6 && 11 <= float64(e.sim.Hour()) && float64(e.sim.Hour()) < 15.5 && e.rnd.Float64() < 0.3])
		mk("Target Server Memory (KB)", "", 60000*1024)
		mk("Total Server Memory (KB)", "", clamp(59000+900*e.load(e.sim), 30000, 60000)*1024)
		mk("Stolen Server Memory (KB)", "", clamp(6100+2800*e.load(e.sim), 4000, 12000)*1024)
		mk("Buffer cache hit ratio", "", 9410)
		mk("Buffer cache hit ratio base", "", 10000)
		return rows, nil

	case strings.Contains(sql, "dm_os_wait_stats"):
		rows := []map[string]any{}
		for wt, ms := range e.waitCum {
			rows = append(rows, map[string]any{
				"wait_type": wt, "waiting_tasks_count": int64(ms / 40),
				"wait_time_ms": int64(ms), "signal_wait_time_ms": int64(ms * 0.12),
				"max_wait_time_ms": int64(900),
			})
		}
		return rows, nil

	case strings.Contains(sql, "dm_io_virtual_file_stats"):
		type fmeta struct {
			db, typ, phys string
			sizeMB        float64
		}
		meta := map[string]fmeta{
			"SalesDb_data01": {"SalesDb", "ROWS", "D:\\MSSQL\\DATA\\SalesDb_data01.mdf", 122880},
			"SalesDb_log":    {"SalesDb", "LOG", "E:\\MSSQL\\LOG\\SalesDb_log.ldf", 24576},
			"ReportsDb_data": {"ReportsDb", "ROWS", "D:\\MSSQL\\DATA\\ReportsDb.mdf", 73728},
			"tempdev":        {"tempdb", "ROWS", "D:\\MSSQL\\DATA\\tempdb.mdf", 8192},
			"templog":        {"tempdb", "LOG", "D:\\MSSQL\\LOG\\templog.ldf", 2048},
			"StatsDb_data":   {"StatsDb", "ROWS", "D:\\MSSQL\\DATA\\StatsDb.mdf", 51200},
		}
		// значения как «средние с запуска сервера», равные мгновенным скоростям
		// при текущей нагрузке (паритет с Python-демо, где files заданы напрямую)
		uptimeSec := float64(e.uptimeDay * 86400)
		load := e.load(e.sim)
		rows := []map[string]any{}
		for name, fr := range e.fileRates(load) {
			m := meta[name]
			rows = append(rows, map[string]any{
				"db_name": m.db, "file_id": int64(1), "file_name": name, "type_desc": m.typ,
				"size_mb": m.sizeMB, "physical_name": m.phys,
				"num_of_reads": int64(fr.readsPS * uptimeSec), "num_of_writes": int64(fr.writesPS * uptimeSec),
				"mb_read": fr.mbRPS * uptimeSec, "mb_written": fr.mbWPS * uptimeSec,
				"io_stall_read_ms":  int64(fr.readsPS * fr.readLat * uptimeSec),
				"io_stall_write_ms": int64(fr.writesPS * fr.writeLat * uptimeSec),
				"io_stall":          int64((fr.readsPS*fr.readLat + fr.writesPS*fr.writeLat) * uptimeSec),
			})
		}
		return rows, nil

	case strings.Contains(sql, "log_reuse_wait_desc"):
		return []map[string]any{
			{"name": "master", "recovery_model_desc": "SIMPLE", "is_auto_shrink_on": int64(0),
				"is_auto_close_on": int64(0), "compatibility_level": int64(150),
				"page_verify_option_desc": "CHECKSUM", "log_reuse_wait_desc": "NOTHING",
				"state_desc": "ONLINE", "user_access_desc": "MULTI_USER", "is_read_only": int64(0),
				"collation_name": "Cyrillic_General_CI_AS", "is_read_committed_snapshot_on": int64(0),
				"snapshot_isolation_state_desc": "OFF", "target_recovery_time_in_seconds": int64(60),
				"is_query_store_on": int64(0), "is_accelerated_database_recovery_on": int64(0)},
			{"name": "SalesDb", "recovery_model_desc": "FULL", "is_auto_shrink_on": int64(0),
				"is_auto_close_on": int64(0), "compatibility_level": int64(150),
				"page_verify_option_desc": "CHECKSUM", "log_reuse_wait_desc": "LOG_BACKUP",
				"state_desc": "ONLINE", "user_access_desc": "MULTI_USER", "is_read_only": int64(0),
				"collation_name": "Cyrillic_General_CI_AS", "is_read_committed_snapshot_on": int64(0),
				"snapshot_isolation_state_desc": "OFF", "target_recovery_time_in_seconds": int64(0),
				"is_query_store_on": int64(1), "is_accelerated_database_recovery_on": int64(0)},
			{"name": "ReportsDb", "recovery_model_desc": "SIMPLE", "is_auto_shrink_on": int64(1),
				"is_auto_close_on": int64(0), "compatibility_level": int64(110),
				"page_verify_option_desc": "CHECKSUM", "log_reuse_wait_desc": "NOTHING",
				"state_desc": "ONLINE", "user_access_desc": "MULTI_USER", "is_read_only": int64(0),
				"collation_name": "Cyrillic_General_CI_AS", "is_read_committed_snapshot_on": int64(0),
				"snapshot_isolation_state_desc": "OFF", "target_recovery_time_in_seconds": int64(0),
				"is_query_store_on": int64(1), "is_accelerated_database_recovery_on": int64(0)},
			{"name": "StatsDb", "recovery_model_desc": "FULL", "is_auto_shrink_on": int64(0),
				"is_auto_close_on": int64(1), "compatibility_level": int64(150),
				"page_verify_option_desc": "CHECKSUM", "log_reuse_wait_desc": "NOTHING",
				"state_desc": "ONLINE", "user_access_desc": "MULTI_USER", "is_read_only": int64(0),
				"collation_name": "Cyrillic_General_CI_AS", "is_read_committed_snapshot_on": int64(0),
				"snapshot_isolation_state_desc": "OFF", "target_recovery_time_in_seconds": int64(0),
				"is_query_store_on": int64(0), "is_accelerated_database_recovery_on": int64(0)},
			{"name": "Legacy2014", "recovery_model_desc": "FULL", "is_auto_shrink_on": int64(0),
				"is_auto_close_on": int64(0), "compatibility_level": int64(100),
				"page_verify_option_desc": "NONE", "log_reuse_wait_desc": "NOTHING",
				"state_desc": "ONLINE", "user_access_desc": "MULTI_USER", "is_read_only": int64(1),
				"collation_name": "Cyrillic_General_CI_AS", "is_read_committed_snapshot_on": int64(0),
				"snapshot_isolation_state_desc": "OFF", "target_recovery_time_in_seconds": int64(0),
				"is_query_store_on": int64(0), "is_accelerated_database_recovery_on": int64(0)},
		}, nil

	case strings.Contains(sql, "COUNT(*) AS data_files"):
		return []map[string]any{{"data_files": int64(1)}}, nil

	case strings.Contains(sql, "sys.configurations"):
		return []map[string]any{
			{"name": "max server memory (MB)", "value_in_use": "2147483647"},
			{"name": "max degree of parallelism", "value_in_use": "0"},
			{"name": "cost threshold for parallelism", "value_in_use": "5"},
			{"name": "optimize for ad hoc workloads", "value_in_use": "0"},
			{"name": "priority boost", "value_in_use": "0"},
			{"name": "lightweight pooling", "value_in_use": "0"},
			{"name": "max worker threads", "value_in_use": "0"},
			{"name": "backup compression default", "value_in_use": "0"},
			{"name": "remote query timeout (s)", "value_in_use": "600"},
			{"name": "recovery interval (min)", "value_in_use": "0"},
			{"name": "fill factor (%)", "value_in_use": "0"},
			{"name": "nested triggers", "value_in_use": "1"},
		}, nil

	case strings.Contains(sql, "IsIntegratedSecurityOnly"):
		return []map[string]any{{
			"windows_only": int64(0), "sql_logins": int64(4), "sql_sysadmin": int64(2),
			"sa_disabled": int64(0), "win_sysadmin": int64(2),
		}}, nil

	case strings.Contains(sql, "SERVERPROPERTY"):
		return []map[string]any{{
			"machine_name": "SQLSRV-PROD-01", "server_name": "SQLSRV-PROD-01\\PROD",
			"instance_name": "PROD", "product_version": "15.0.4360.2", "product_level": "RTM CU12",
			"edition": "SQL Server 2019 Standard Edition (64-bit)", "is_clustered": int64(0),
			"collation": "Cyrillic_General_CI_AS",
		}}, nil

	case strings.Contains(sql, "dm_os_sys_info"):
		return []map[string]any{{
			"cpu_count": int64(16), "hyperthread_ratio": int64(1),
			"physical_memory_kb": int64(67108864), "committed_kb": int64(58000000),
			"sql_memory_model_desc": "CONVENTIONAL",
			"virtual_machine_type_desc": "HYPERVISOR",
			"socket_count":              int64(2), "cores_per_socket": int64(8), "numa_node_count": int64(2),
			"sqlserver_start_time": time.Now().AddDate(0, 0, -45),
			"uptime_sec":           int64(45 * 86400),
		}}, nil

	case strings.Contains(sql, "dm_os_sys_memory"):
		return []map[string]any{{
			"total_physical_kb": int64(67108864), "available_physical_kb": int64(2932736),
			"system_memory_state_desc": "Available physical memory is low",
		}}, nil

	case strings.Contains(sql, "dm_os_buffer_descriptors"):
		// память по базам: буферный кэш ~51–52 ГБ из ~59 занятых SQL Server
		return []map[string]any{
			{"db_name": "SalesDb", "cached_pages": int64(3763200), "cached_mb": int64(29400)},
			{"db_name": "StatsDb", "cached_pages": int64(1446400), "cached_mb": int64(11300)},
			{"db_name": "ReportsDb", "cached_pages": int64(870400), "cached_mb": int64(6800)},
			{"db_name": "tempdb", "cached_pages": int64(371200), "cached_mb": int64(2900)},
			{"db_name": "mssqlsystemresource", "cached_pages": int64(60160), "cached_mb": int64(470)},
			{"db_name": "msdb", "cached_pages": int64(37120), "cached_mb": int64(290)},
			{"db_name": "master", "cached_pages": int64(15360), "cached_mb": int64(120)},
		}, nil

	case strings.Contains(sql, "dm_exec_query_stats") && strings.Contains(sql, "total_worker_time"):
		// ЦП по базам (накопительно из кэша планов, 45 дней работы)
		return []map[string]any{
			{"db_name": "SalesDb", "cpu_ms": int64(5_800_000_000), "exec_count": int64(41_200_000)},
			{"db_name": "StatsDb", "cpu_ms": int64(2_300_000_000), "exec_count": int64(8_900_000)},
			{"db_name": "ReportsDb", "cpu_ms": int64(1_700_000_000), "exec_count": int64(3_400_000)},
			{"db_name": "msdb", "cpu_ms": int64(64_000_000), "exec_count": int64(520_000)},
			{"db_name": "master", "cpu_ms": int64(21_000_000), "exec_count": int64(180_000)},
		}, nil

	case strings.Contains(sql, "dm_exec_query_stats"):
		now := time.Now()
		return []map[string]any{
			{"db_name": "ReportsDb", "execution_count": int64(5420),
				"total_cpu_ms": 28_940_000, "avg_cpu_ms": 5340, "avg_elapsed_ms": 7120,
				"total_elapsed_ms": 38_610_000, "avg_logical_reads": 1_420_000,
				"total_physical_reads": 984_000_000, "total_writes": int64(0),
				"creation_time": now.AddDate(0, 0, -40), "last_execution_time": now.Add(-2 * time.Hour),
				"query_text": "SELECT d.Date, p.Name, SUM(f.Amount) AS Total FROM Reports.FactSales f " +
					"JOIN Reports.DimDate d ON d.DateKey = f.DateKey " +
					"JOIN Reports.DimProduct p ON p.ProductKey = f.ProductKey " +
					"WHERE d.Year = @y GROUP BY d.Date, p.Name"},
			{"db_name": "StatsDb", "execution_count": int64(128900),
				"total_cpu_ms": 19_450_000, "avg_cpu_ms": 150.9, "avg_elapsed_ms": 540,
				"total_elapsed_ms": 69_600_000, "avg_logical_reads": 210_000,
				"total_physical_reads": 4_120_000_000, "total_writes": int64(12),
				"creation_time": now.AddDate(0, 0, -44), "last_execution_time": now.Add(-3 * time.Minute),
				"query_text": "SELECT e.EventId, e.Payload FROM dbo.Events e " +
					"WHERE e.CreatedAt >= DATEADD(HOUR, -24, SYSUTCDATETIME()) AND e.Payload LIKE @pattern"},
			{"db_name": "SalesDb", "execution_count": int64(2_100_000),
				"total_cpu_ms": 15_200_000, "avg_cpu_ms": 7.2, "avg_elapsed_ms": 9.5,
				"total_elapsed_ms": 19_950_000, "avg_logical_reads": 420,
				"total_physical_reads": 8_900_000, "total_writes": int64(4),
				"creation_time": now.AddDate(0, 0, -45), "last_execution_time": now.Add(-10 * time.Second),
				"query_text": "UPDATE dbo.Warehouse.StockUnits SET ReservedQty = ReservedQty + @delta WHERE Sku = @sku"},
			{"db_name": "SalesDb", "execution_count": int64(184000),
				"total_cpu_ms": 12_800_000, "avg_cpu_ms": 69.6, "avg_elapsed_ms": 210,
				"total_elapsed_ms": 38_640_000, "avg_logical_reads": 26_500,
				"total_physical_reads": 220_000_000, "total_writes": int64(0),
				"creation_time": now.AddDate(0, 0, -38), "last_execution_time": now.Add(-1 * time.Minute),
				"query_text": "SELECT TOP 100 o.Id, c.Name, o.TotalAmount FROM Sales.Orders o " +
					"JOIN Sales.Customers c ON c.CustomerId = o.CustomerId " +
					"WHERE o.CreatedAt >= @FromDate AND o.StatusId = @Status ORDER BY o.TotalAmount DESC"},
			{"db_name": "SalesDb", "execution_count": int64(410),
				"total_cpu_ms": 4_300_000, "avg_cpu_ms": 10487.8, "avg_elapsed_ms": 25800,
				"total_elapsed_ms": 10_578_000, "avg_logical_reads": 4_900_000,
				"total_physical_reads": 820_000_000, "total_writes": int64(0),
				"creation_time": now.AddDate(0, 0, -30), "last_execution_time": now.Add(-3 * time.Hour),
				"query_text": "EXEC dbo.usp_CalcOrderTotals @FromDate = @p0, @ToDate = @p1"},
		}, nil

	// --- проверки по методике настройки (диски, службы, безопасность, копии) ---

	case strings.Contains(sql, "dm_server_services"):
		return []map[string]any{
			{"servicename": "SQL Server (PROD)", "startup_type_desc": "Automatic",
				"status_desc": "Running", "service_account": "NT AUTHORITY\\SYSTEM",
				"instant_file_initialization_enabled": "NO"},
			{"servicename": "SQL Server Agent (PROD)", "startup_type_desc": "Automatic",
				"status_desc": "Running", "service_account": "NT AUTHORITY\\SYSTEM",
				"instant_file_initialization_enabled": "NO"},
		}, nil

	case strings.Contains(sql, "dm_os_volume_stats"):
		return []map[string]any{
			{"volume_mount_point": "C:\\", "logical_volume_name": "", "total_gb": 120.0, "free_gb": 24.0},
			{"volume_mount_point": "D:\\", "logical_volume_name": "DATA", "total_gb": 400.0, "free_gb": 42.0},
			{"volume_mount_point": "E:\\", "logical_volume_name": "LOG", "total_gb": 300.0, "free_gb": 118.0},
		}, nil

	case strings.Contains(sql, "TOP 30 name, type_desc, is_disabled"):
		return []map[string]any{
			{"name": "1C_Admin", "type_desc": "SQL_LOGIN", "is_disabled": int64(0)},
			{"name": "report_svc", "type_desc": "SQL_LOGIN", "is_disabled": int64(0)},
		}, nil

	case strings.Contains(sql, "ActivePowerScheme"):
		return []map[string]any{{"Value": "381b4222-f694-41f0-9685-ff5bb260df2e"}}, nil

	case strings.Contains(sql, "SuperSocketNetLib\\Tcp"):
		return []map[string]any{{"Value": "1"}}, nil

	case strings.Contains(sql, "SuperSocketNetLib\\Np"):
		return []map[string]any{{"Value": "1"}}, nil

	case strings.Contains(sql, "database_id = 2 AND type = 0"):
		return []map[string]any{
			{"name": "tempdev", "size_mb": 8192.0, "is_percent_growth": int64(1),
				"growth": int64(10), "physical_name": "D:\\MSSQL\\DATA\\tempdb.mdf"},
		}, nil

	case strings.Contains(sql, "backupmediafamily"):
		now := time.Now()
		return []map[string]any{
			{"db_name": "SalesDb", "physical_device_name": "D:\\Backup\\SalesDb_full.bak",
				"backup_type": "D", "backup_finish_date": now.AddDate(0, 0, -2).Add(-5 * time.Hour),
				"encryptor_type": nil},
			{"db_name": "ReportsDb", "physical_device_name": "\\\\backup-srv\\sqlbackup\\ReportsDb.bak",
				"backup_type": "D", "backup_finish_date": now.AddDate(0, 0, -9), "encryptor_type": nil},
			{"db_name": "StatsDb", "physical_device_name": "D:\\Backup\\StatsDb_full.bak",
				"backup_type": "D", "backup_finish_date": now.Add(-20 * time.Hour), "encryptor_type": nil},
		}, nil

	case strings.Contains(sql, "backupset"):
		now := time.Now()
		return []map[string]any{
			{"db_name": "SalesDb", "last_full": now.AddDate(0, 0, -2).Add(-5 * time.Hour),
				"last_diff": nil, "last_log": now.AddDate(0, 0, -3).Add(-2 * time.Hour)},
			{"db_name": "ReportsDb", "last_full": now.AddDate(0, 0, -9), "last_diff": nil, "last_log": nil},
			{"db_name": "StatsDb", "last_full": now.Add(-20 * time.Hour), "last_diff": nil,
				"last_log": now.Add(-20 * time.Hour)},
		}, nil

	case strings.Contains(sql, "dm_exec_cached_plans"):
		return []map[string]any{
			{"objtype": "Adhoc", "plan_count": int64(41230), "size_mb": 1987.4},
			{"objtype": "Prepared", "plan_count": int64(8145), "size_mb": 512.9},
			{"objtype": "Proc", "plan_count": int64(3120), "size_mb": 745.1},
		}, nil

	case strings.Contains(sql, "master_files"):
		return []map[string]any{
			{"db_name": "SalesDb", "file_name": "SalesDb_data01", "type_desc": "ROWS",
				"size_mb": 122880.0, "is_percent_growth": int64(0), "growth": int64(256),
				"physical_name": "D:\\MSSQL\\DATA\\SalesDb_data01.mdf"},
			{"db_name": "SalesDb", "file_name": "SalesDb_log", "type_desc": "LOG",
				"size_mb": 24576.0, "is_percent_growth": int64(1), "growth": int64(10),
				"physical_name": "E:\\MSSQL\\LOG\\SalesDb_log.ldf"},
			{"db_name": "ReportsDb", "file_name": "ReportsDb_data", "type_desc": "ROWS",
				"size_mb": 73728.0, "is_percent_growth": int64(0), "growth": int64(128),
				"physical_name": "D:\\MSSQL\\DATA\\ReportsDb.mdf"},
			{"db_name": "tempdb", "file_name": "tempdev", "type_desc": "ROWS",
				"size_mb": 8192.0, "is_percent_growth": int64(1), "growth": int64(10),
				"physical_name": "D:\\MSSQL\\DATA\\tempdb.mdf"},
			{"db_name": "StatsDb", "file_name": "StatsDb_data", "type_desc": "ROWS",
				"size_mb": 51200.0, "is_percent_growth": int64(0), "growth": int64(64),
				"physical_name": "D:\\MSSQL\\DATA\\StatsDb.mdf"},
		}, nil

	case strings.Contains(sql, "sp_executesql"):
		return e.perDBQuery(sql)
	}
	return []map[string]any{}, nil
}

// perDBQuery отвечает на запросы по конкретной базе (missing index / frag / stats / vlf).
func (e *DemoEngine) perDBQuery(sql string) ([]map[string]any, error) {
	db := "SalesDb"
	if strings.Contains(sql, "[StatsDb]") {
		db = "StatsDb"
	} else if strings.Contains(sql, "[ReportsDb]") {
		db = "ReportsDb"
	}
	switch {
	case strings.Contains(sql, "database_query_store_options"):
		if db == "SalesDb" {
			return []map[string]any{{"db_name": db, "actual_state_desc": "READ_WRITE",
				"readonly_reason": int64(0), "query_capture_mode_desc": "AUTO",
				"current_mb": int64(420), "max_mb": int64(1024)}}, nil
		}
		if db == "ReportsDb" {
			return []map[string]any{{"db_name": db, "actual_state_desc": "READ_ONLY",
				"readonly_reason": int64(8), "query_capture_mode_desc": "AUTO",
				"current_mb": int64(1024), "max_mb": int64(1024)}}, nil
		}
		return []map[string]any{}, nil
	case strings.Contains(sql, "missing_index"):
		if db != "SalesDb" && db != "StatsDb" {
			return []map[string]any{}, nil
		}
		if db == "StatsDb" {
			return []map[string]any{{
				"db_name": db, "table_ref": "[dbo].[Events]", "equality_columns": nil,
				"inequality_columns": "[CreatedAt]", "included_columns": "[Payload]",
				"user_seeks": int64(96400), "user_scans": int64(1204),
				"avg_total_user_cost": 14.872, "avg_user_impact": 74.10, "score": 107200.0,
			}}, nil
		}
		return []map[string]any{
			{"db_name": db, "table_ref": "[Sales].[Orders]", "equality_columns": "[StatusId], [CustomerId]",
				"inequality_columns": "[CreatedAt]", "included_columns": "[TotalAmount]",
				"user_seeks": int64(184212), "user_scans": int64(0),
				"avg_total_user_cost": 0.912, "avg_user_impact": 88.34, "score": 148500.0},
			{"db_name": db, "table_ref": "[Sales].[OrderLines]", "equality_columns": "[ProductId]",
				"inequality_columns": nil, "included_columns": "[Qty], [Price]",
				"user_seeks": int64(51800), "user_scans": int64(0),
				"avg_total_user_cost": 0.445, "avg_user_impact": 91.20, "score": 21000.0},
		}, nil
	case strings.Contains(sql, "index_physical_stats"):
		frag := map[string][]map[string]any{
			"ReportsDb": {
				{"db_name": "ReportsDb", "schema_name": "Reports", "table_name": "FactSales",
					"index_name": "IX_FactSales_Date", "index_type_desc": "NONCLUSTERED INDEX",
					"avg_fragmentation_in_percent": 74.2, "page_count": int64(92300)},
				{"db_name": "ReportsDb", "schema_name": "Reports", "table_name": "DimProduct",
					"index_name": "IX_DimProduct_Name", "index_type_desc": "NONCLUSTERED INDEX",
					"avg_fragmentation_in_percent": 33.9, "page_count": int64(1050)},
			},
			"SalesDb": {{"db_name": "SalesDb", "schema_name": "Sales", "table_name": "Orders",
				"index_name": "IX_Orders_CreatedAt", "index_type_desc": "NONCLUSTERED INDEX",
				"avg_fragmentation_in_percent": 57.8, "page_count": int64(48100)}},
			"StatsDb": {{"db_name": "StatsDb", "schema_name": "dbo", "table_name": "Events",
				"index_name": "PK_Events", "index_type_desc": "CLUSTERED INDEX",
				"avg_fragmentation_in_percent": 45.1, "page_count": int64(12400)}},
		}
		return frag[db], nil
	case strings.Contains(sql, "dm_db_stats_properties"):
		now := time.Now()
		st := map[string][]map[string]any{
			"SalesDb": {
				{"db_name": "SalesDb", "schema_name": "Sales", "table_name": "Orders",
					"stats_name": "_WA_Sys_00000007_1273C1CD", "last_updated": now.AddDate(0, 0, -17),
					"rows": 4_920_000.0, "modification_counter": 1_610_000.0},
				{"db_name": "SalesDb", "schema_name": "Sales", "table_name": "OrderLines",
					"stats_name": "PK_OrderLines", "last_updated": now.AddDate(0, 0, -23),
					"rows": 19_400_000.0, "modification_counter": 5_900_000.0},
			},
			"StatsDb": {{"db_name": "StatsDb", "schema_name": "dbo", "table_name": "Events",
				"stats_name": "IX_Events_CreatedAt", "last_updated": now.AddDate(0, 0, -9),
				"rows": 88_000_000.0, "modification_counter": 31_000_000.0}},
		}
		return st[db], nil
	case strings.Contains(sql, "dm_db_log_info"):
		vlf := map[string]int64{"SalesDb": 1184, "StatsDb": 342, "ReportsDb": 97}
		if c, ok := vlf[db]; ok {
			return []map[string]any{{"vlf_count": c, "db_name": db}}, nil
		}
		return []map[string]any{{"vlf_count": int64(50), "db_name": db}}, nil
	}
	return []map[string]any{}, nil
}

// requestsRows — активные запросы (аналог sys.dm_exec_requests).
func (e *DemoEngine) requestsRows() []map[string]any {
	load := e.load(e.sim)
	sid := int64(51)
	rows := []map[string]any{}
	type tpl struct {
		cmd, db, text, wait string
		reads, cpu, elapsed int64
	}
	base := []tpl{
		{"SELECT", "SalesDb", "SELECT TOP 100 o.Id, c.Name, o.TotalAmount FROM Sales.Orders o " +
			"JOIN Sales.Customers c ON c.CustomerId = o.CustomerId " +
			"WHERE o.CreatedAt >= @FromDate AND o.StatusId = @Status ORDER BY o.TotalAmount DESC",
			"", 26500, 140, 900},
		{"UPDATE", "SalesDb", "UPDATE dbo.Warehouse.StockUnits SET ReservedQty = ReservedQty + @delta WHERE Sku = @sku",
			"", 210, 18, 45},
		{"SELECT", "ReportsDb", "SELECT d.Date, p.Name, SUM(f.Amount) AS Total FROM Reports.FactSales f " +
			"JOIN Reports.DimDate d ON d.DateKey = f.DateKey " +
			"JOIN Reports.DimProduct p ON p.ProductKey = f.ProductKey WHERE d.Year = @y AND d.Month = @m GROUP BY d.Date, p.Name",
			"CXPACKET", 1_420_000, 9500, 11500},
		{"SELECT", "StatsDb", "SELECT e.EventId, e.Payload FROM dbo.Events e " +
			"WHERE e.CreatedAt >= DATEADD(HOUR, -24, SYSUTCDATETIME()) AND e.Payload LIKE @pattern",
			"PAGEIOLATCH_SH", 890000, 4300, 9400},
		{"INSERT", "SalesDb", "INSERT INTO dbo.AuditLog (Entity, ActionId, At, Detail) " +
			"VALUES (@entity, @action, SYSUTCDATETIME(), @detail)", "", 12, 2, 8},
		{"EXEC", "SalesDb", "EXEC dbo.usp_CalcOrderTotals @FromDate = @p0, @ToDate = @p1",
			"LCK_M_S", 96000, 780, 2600},
	}
	for _, q := range base {
		if e.rnd.Float64() > 0.25+0.7*load {
			continue
		}
		sid++
		waitTime := int64(0)
		blocking := int64(0)
		if q.wait != "" {
			waitTime = int64(20 + e.rnd.Intn(8980))
			if q.wait == "LCK_M_S" && e.blocking {
				blocking = 58
			}
		}
		status := "running"
		if q.wait != "" {
			status = "suspended"
		}
		rows = append(rows, map[string]any{
			"session_id": sid, "status": status, "blocking_session_id": blocking,
			"wait_type": q.wait, "wait_time": waitTime, "wait_resource": "14:5:3312",
			"cpu_time":           int64(float64(q.cpu) * (0.5 + load)),
			"total_elapsed_time": int64(float64(q.elapsed) * (0.4 + load)),
			"reads":              int64(float64(q.reads) * (0.5 + load)),
			"writes":             int64(e.rnd.Intn(30)), "logical_reads": int64(float64(q.reads) * 2.2),
			"command": q.cmd, "db_name": q.db,
			"host_name":    fmt.Sprintf("APP-SRV-0%d", 1+e.rnd.Intn(5)),
			"login_name":   []string{"CORP\\svc_web", "CORP\\app_user", "sa"}[e.rnd.Intn(3)],
			"program_name": []string{".Net SqlClient Data Provider", "Microsoft JDBC Driver", "SSMS"}[e.rnd.Intn(3)],
			"query_text":   q.text,
		})
	}
	if e.blocking {
		for i := 0; i < e.victims && i < 4; i++ {
			sid++
			rows = append(rows, map[string]any{
				"session_id": sid, "status": "suspended", "blocking_session_id": int64(58),
				"wait_type": "LCK_M_S", "wait_time": int64(500 + e.rnd.Intn(41500)),
				"wait_resource": fmt.Sprintf("14:5:%d", 100+e.rnd.Intn(9899)),
				"cpu_time":      int64(e.rnd.Intn(200)), "total_elapsed_time": int64(2000 + e.rnd.Intn(58000)),
				"reads": int64(10 + e.rnd.Intn(490)), "writes": int64(0),
				"logical_reads": int64(20 + e.rnd.Intn(880)), "command": "UPDATE",
				"db_name": "SalesDb", "host_name": "APP-SRV-02", "login_name": "CORP\\svc_web",
				"program_name": ".Net SqlClient Data Provider",
				"query_text":   "UPDATE dbo.Warehouse.StockUnits SET ReservedQty = ReservedQty - @d WHERE Sku = @sku",
			})
		}
	}
	return rows
}
