package main

// SQL-запросы к системным представлениям (DMV) MS SQL Server.
// Совместимо с SQL Server 2012+ (для старых есть запасные варианты).

import (
	"fmt"
	"strings"
)

const qInstance = `
SELECT CAST(SERVERPROPERTY('MachineName')    AS nvarchar(128)) AS machine_name,
       CAST(SERVERPROPERTY('ServerName')     AS nvarchar(128)) AS server_name,
       CAST(SERVERPROPERTY('InstanceName')   AS nvarchar(128)) AS instance_name,
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(32))  AS product_version,
       CAST(SERVERPROPERTY('ProductLevel')   AS nvarchar(32))  AS product_level,
       CAST(SERVERPROPERTY('Edition')        AS nvarchar(128)) AS edition,
       CAST(SERVERPROPERTY('IsClustered')    AS int)           AS is_clustered,
       CAST(SERVERPROPERTY('Collation')      AS nvarchar(128)) AS collation
`

const qSysInfo = `
SELECT cpu_count, hyperthread_ratio,
       CAST(physical_memory_kb AS bigint)  AS physical_memory_kb,
       CAST(committed_memory_kb AS bigint) AS committed_kb,
       sql_memory_model_desc, sqlserver_start_time,
       virtual_machine_type_desc, socket_count, cores_per_socket, numa_node_count,
       DATEDIFF(SECOND, sqlserver_start_time, SYSDATETIME()) AS uptime_sec
FROM sys.dm_os_sys_info
`

const qSysInfoOld = `
SELECT cpu_count, hyperthread_ratio,
       CAST(physical_memory_in_bytes / 1024 AS bigint) AS physical_memory_kb,
       CAST(committed_memory_kb AS bigint)             AS committed_kb,
       CAST('' AS nvarchar(60))                        AS sql_memory_model_desc,
       sqlserver_start_time,
       CAST('' AS nvarchar(60)) AS virtual_machine_type_desc,
       socket_count, cores_per_socket, numa_node_count,
       DATEDIFF(SECOND, sqlserver_start_time, SYSDATETIME()) AS uptime_sec
FROM sys.dm_os_sys_info
`

const qOSMemory = `
SELECT CAST(total_physical_memory_kb AS bigint)     AS total_physical_kb,
       CAST(available_physical_memory_kb AS bigint) AS available_physical_kb,
       system_memory_state_desc
FROM sys.dm_os_sys_memory
`

const qCPU = `
SELECT TOP 1
  rec.value('(./Record/SchedulerMonitorEvent/SystemHealth/ProcessUtilization)[1]', 'int') AS sql_cpu,
  rec.value('(./Record/SchedulerMonitorEvent/SystemHealth/SystemIdle)[1]', 'int')         AS system_idle
FROM (
  SELECT TOP 60 CAST(record AS XML) AS rec
  FROM sys.dm_os_ring_buffers
  WHERE ring_buffer_type = N'RING_BUFFER_SCHEDULER_MONITOR'
  ORDER BY timestamp DESC
) AS t
ORDER BY t.rec.value('(./Record/@time)[1]', 'bigint') DESC
`

var perfCounterNames = []string{
	"Batch Requests/sec", "SQL Compilations/sec", "SQL Re-Compilations/sec",
	"Transactions/sec", "Page Splits/sec", "Lock Waits/sec",
	"Page Life Expectancy", "User Connections", "Processes blocked",
	"Errors/sec", "Full Scans/sec", "Index Searches/sec",
	"Log Flushes/sec", "Page reads/sec", "Page writes/sec",
	"Lazy Writes/sec", "Checkpoint pages/sec", "Memory Grants Pending",
	"Free list stalls/sec",
	"Target Server Memory (KB)", "Total Server Memory (KB)",
	"Stolen Server Memory (KB)",
	"Buffer cache hit ratio", "Buffer cache hit ratio base",
}

var qCounters = fmt.Sprintf(`
SELECT RTRIM(object_name)   AS object_name,
       RTRIM(counter_name)  AS counter_name,
       RTRIM(instance_name) AS instance_name,
       cntr_value, cntr_type
FROM sys.dm_os_performance_counters
WHERE counter_name IN (%s)
`, "'"+strings.Join(perfCounterNames, "', N'")+"'")

const qWaits = `
SELECT wait_type, waiting_tasks_count, wait_time_ms, signal_wait_time_ms, max_wait_time_ms
FROM sys.dm_os_wait_stats
`

const qRequests = `
SELECT r.session_id, r.status, r.blocking_session_id, r.wait_type, r.wait_time,
       r.wait_resource, r.cpu_time, r.total_elapsed_time, r.reads, r.writes,
       r.logical_reads, r.command, DB_NAME(r.database_id) AS db_name,
       s.host_name, s.login_name, s.program_name,
       t.text AS query_text
FROM sys.dm_exec_requests r
LEFT JOIN sys.dm_exec_sessions s ON s.session_id = r.session_id
OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) t
WHERE r.session_id >= 50 AND r.session_id <> @@SPID
`

const qHeadBlockers = `
SELECT s.session_id, s.login_name, s.host_name, s.program_name, s.status AS session_status,
       r.blocking_session_id, ib.event_info AS input_buffer
FROM sys.dm_exec_sessions s
LEFT JOIN sys.dm_exec_requests r ON r.session_id = s.session_id
OUTER APPLY sys.dm_exec_input_buffer(s.session_id, NULL) ib
WHERE s.session_id IN (%s)
`

const qFiles = `
SELECT DB_NAME(vfs.database_id) AS db_name, mf.file_id, mf.name AS file_name,
       mf.type_desc, CAST(mf.size * 8.0 / 1024 AS float) AS size_mb, mf.physical_name,
       vfs.num_of_reads, vfs.num_of_writes,
       CAST(vfs.num_of_bytes_read / 1048576.0 AS float)    AS mb_read,
       CAST(vfs.num_of_bytes_written / 1048576.0 AS float) AS mb_written,
       vfs.io_stall_read_ms, vfs.io_stall_write_ms, vfs.io_stall
FROM sys.dm_io_virtual_file_stats(NULL, NULL) vfs
JOIN sys.master_files mf
  ON mf.database_id = vfs.database_id AND mf.file_id = vfs.file_id
`

// Память по базам данных: сколько буферного кэша занимает каждая БД.
const qBufferPerDB = `
SELECT CASE WHEN database_id = 32767 THEN 'mssqlsystemresource'
            ELSE DB_NAME(database_id) END AS db_name,
       COUNT_BIG(*) AS cached_pages,
       CAST(COUNT_BIG(*) * 8 / 1024 AS bigint) AS cached_mb
FROM sys.dm_os_buffer_descriptors
GROUP BY database_id
HAVING COUNT_BIG(*) >= 128
ORDER BY cached_pages DESC
`

// ЦП по базам данных: накопленное время CPU из кэша планов (dm_exec_query_stats).
const qCPUPerDB = `
SELECT TOP 15 ISNULL(d.name, 'db_id=' + CAST(t.dbid AS nvarchar(20))) AS db_name,
       CAST(SUM(s.total_worker_time) AS bigint) AS cpu_ms,
       CAST(SUM(s.execution_count) AS bigint) AS exec_count
FROM sys.dm_exec_query_stats s
CROSS APPLY sys.dm_exec_sql_text(s.sql_handle) t
LEFT JOIN sys.databases d ON d.database_id = t.dbid
WHERE t.dbid IS NOT NULL
GROUP BY t.dbid, d.name
ORDER BY SUM(s.total_worker_time) DESC
`

const qDatabases = `
SELECT name, recovery_model_desc, is_auto_shrink_on, is_auto_close_on,
       compatibility_level, page_verify_option_desc, log_reuse_wait_desc,
       state_desc, user_access_desc, is_read_only,
       collation_name, is_read_committed_snapshot_on, snapshot_isolation_state_desc,
       target_recovery_time_in_seconds
FROM sys.databases
`

// SQL Server 2016+ (13.x): Query Store.
const qDatabases2016 = `
SELECT name, recovery_model_desc, is_auto_shrink_on, is_auto_close_on,
       compatibility_level, page_verify_option_desc, log_reuse_wait_desc,
       state_desc, user_access_desc, is_read_only,
       collation_name, is_read_committed_snapshot_on, snapshot_isolation_state_desc,
       target_recovery_time_in_seconds, is_query_store_on
FROM sys.databases
`

// SQL Server 2019+ (15.x): ускоренное восстановление (ADR).
const qDatabases2019 = `
SELECT name, recovery_model_desc, is_auto_shrink_on, is_auto_close_on,
       compatibility_level, page_verify_option_desc, log_reuse_wait_desc,
       state_desc, user_access_desc, is_read_only,
       collation_name, is_read_committed_snapshot_on, snapshot_isolation_state_desc,
       target_recovery_time_in_seconds, is_query_store_on,
       is_accelerated_database_recovery_on
FROM sys.databases
`

const qFileCfg = `
SELECT DB_NAME(database_id) AS db_name, name AS file_name, type_desc,
       CAST(size * 8.0 / 1024 AS float) AS size_mb,
       is_percent_growth, growth, physical_name
FROM sys.master_files
`

const qTempdbFiles = `
SELECT COUNT(*) AS data_files FROM sys.master_files WHERE database_id = 2 AND type = 0
`

// Детали файлов tempdb: размеры и авторост (для проверки равного размера и преаллокации).
const qTempdbDetail = `
SELECT name, CAST(size * 8.0 / 1024 AS float) AS size_mb,
       is_percent_growth, growth, physical_name
FROM sys.master_files
WHERE database_id = 2 AND type = 0
`

// Службы SQL Server: учётные записи, Instant File Initialization, состав компонентов.
const qServices = `
SELECT servicename, startup_type_desc, status_desc, service_account,
       CAST(instant_file_initialization_enabled AS nvarchar(16)) AS instant_file_initialization_enabled
FROM sys.dm_server_services
`

// Для версий до 2016 SP1 колонки instant_file_initialization_enabled нет.
const qServicesOld = `
SELECT servicename, startup_type_desc, status_desc, service_account,
       CAST('' AS nvarchar(16)) AS instant_file_initialization_enabled
FROM sys.dm_server_services
`

// Тома, на которых лежат файлы баз: свободное место (риск переполнения журнала/tempdb).
const qVolumes = `
SELECT DISTINCT vs.volume_mount_point, vs.logical_volume_name,
       CAST(vs.total_bytes / 1073741824.0 AS float)     AS total_gb,
       CAST(vs.available_bytes / 1073741824.0 AS float) AS free_gb
FROM sys.master_files mf
CROSS APPLY sys.dm_os_volume_stats(mf.database_id, mf.file_id) vs
`

// Аутентификация и привилегированные учётные записи (гигиена безопасности).
const qSecurity = `
SELECT CAST(SERVERPROPERTY('IsIntegratedSecurityOnly') AS int) AS windows_only,
       (SELECT CAST(COUNT(*) AS int) FROM sys.server_principals
         WHERE type = 'S' AND name <> 'sa' AND is_disabled = 0) AS sql_logins,
       (SELECT CAST(COUNT(*) AS int) FROM sys.server_principals
         WHERE type = 'S' AND IS_SRVROLEMEMBER('sysadmin', name) = 1 AND is_disabled = 0) AS sql_sysadmin,
       (SELECT CAST(MAX(CASE WHEN name = 'sa' THEN is_disabled ELSE NULL END) AS int)
          FROM sys.server_principals WHERE name = 'sa') AS sa_disabled,
       (SELECT CAST(COUNT(*) AS int) FROM sys.server_principals
         WHERE type = 'U' AND IS_SRVROLEMEMBER('sysadmin', name) = 1) AS win_sysadmin
`

// Логины с правами sysadmin (кроме sa) — для отчёта по безопасности.
const qSysadminLogins = `
SELECT TOP 30 name, type_desc, is_disabled
FROM sys.server_principals
WHERE IS_SRVROLEMEMBER('sysadmin', name) = 1
  AND name <> 'sa' AND name NOT LIKE '##%' AND name NOT LIKE 'NT %'
ORDER BY name
`

// Активная схема электропитания Windows (High performance = 8c5e7fda-...).
// Требует прав на чтение реестра; при ошибке проверка просто пропускается.
const qPowerPlan = `EXEC master..xp_instance_regread N'HKEY_LOCAL_MACHINE', ` +
	`N'SYSTEM\CurrentControlSet\Control\Power\User\PowerSchemes', N'ActivePowerScheme'`

// Сетевые протоколы экземпляра (TCP/IP должен быть включён; именованные каналы — как правило, нет).
const qTcpEnabled = `EXEC master..xp_instance_regread N'HKEY_LOCAL_MACHINE', ` +
	`N'SOFTWARE\Microsoft\MSSQLServer\MSSQLServer\SuperSocketNetLib\Tcp', N'Enabled'`

const qNpEnabled = `EXEC master..xp_instance_regread N'HKEY_LOCAL_MACHINE', ` +
	`N'SOFTWARE\Microsoft\MSSQLServer\MSSQLServer\SuperSocketNetLib\Np', N'Enabled'`

// Куда пишутся резервные копии (сетевой ресурс или локальные диски сервера).
const qBackupMedia = `
SELECT d.name AS db_name, bmf.physical_device_name, bs.type AS backup_type,
       bs.backup_finish_date, bs.encryptor_type
FROM sys.databases d
JOIN msdb.dbo.backupset bs        ON bs.database_name = d.name
JOIN msdb.dbo.backupmediafamily bmf ON bmf.media_set_id = bs.media_set_id
WHERE d.database_id > 4
  AND bs.backup_finish_date = (SELECT MAX(bs2.backup_finish_date)
                               FROM msdb.dbo.backupset bs2
                               WHERE bs2.database_name = d.name)
`

// Для SQL Server 2012/2014 колонки encryptor_type нет.
const qBackupMediaOld = `
SELECT d.name AS db_name, bmf.physical_device_name, bs.type AS backup_type,
       bs.backup_finish_date, CAST(NULL AS nvarchar(32)) AS encryptor_type
FROM sys.databases d
JOIN msdb.dbo.backupset bs        ON bs.database_name = d.name
JOIN msdb.dbo.backupmediafamily bmf ON bmf.media_set_id = bs.media_set_id
WHERE d.database_id > 4
  AND bs.backup_finish_date = (SELECT MAX(bs2.backup_finish_date)
                               FROM msdb.dbo.backupset bs2
                               WHERE bs2.database_name = d.name)
`

// Состояние Query Store в текущей базе (2016+): включён ли и не перешёл ли в READ_ONLY.
const qQueryStoreDB = `
SELECT DB_NAME() AS db_name, actual_state_desc, readonly_reason, query_capture_mode_desc,
       CAST(current_storage_size_mb AS bigint) AS current_mb,
       CAST(max_storage_size_mb AS bigint)     AS max_mb
FROM sys.database_query_store_options
`

var configNames = []string{
	"max server memory (MB)", "max degree of parallelism", "cost threshold for parallelism",
	"optimize for ad hoc workloads", "priority boost", "lightweight pooling",
	"max worker threads", "backup compression default", "remote query timeout (s)",
	"recovery interval (min)", "fill factor (%)", "nested triggers",
}

var qConfig = fmt.Sprintf(`
SELECT name, CAST(value_in_use AS nvarchar(100)) AS value_in_use,
       CAST(value AS nvarchar(100)) AS value_pending,
       is_dynamic, is_advanced, CAST(description AS nvarchar(600)) AS description
FROM sys.configurations
WHERE name IN (%s)
`, "'"+strings.Join(configNames, "', N'")+"'")

const qTopQueries = `
SELECT TOP 25 qs.execution_count,
       CAST(qs.total_worker_time / 1000.0 AS float) AS total_cpu_ms,
       CAST(qs.total_worker_time / qs.execution_count / 1000.0 AS float) AS avg_cpu_ms,
       CAST(qs.total_elapsed_time / qs.execution_count / 1000.0 AS float) AS avg_elapsed_ms,
       CAST(qs.total_elapsed_time / 1000.0 AS float) AS total_elapsed_ms,
       qs.total_logical_reads / qs.execution_count AS avg_logical_reads,
       qs.total_physical_reads, qs.total_writes,
       qs.creation_time, qs.last_execution_time,
       DB_NAME(t.dbid) AS db_name, t.text AS query_text
FROM sys.dm_exec_query_stats qs
OUTER APPLY sys.dm_exec_sql_text(qs.sql_handle) t
ORDER BY qs.total_worker_time DESC
`

const qCachedPlans = `
SELECT objtype, COUNT(*) AS plan_count,
       CAST(SUM(CAST(size_in_bytes AS BIGINT)) / 1048576.0 AS float) AS size_mb
FROM sys.dm_exec_cached_plans
GROUP BY objtype
`

const qBackups = `
SELECT d.name AS db_name,
       MAX(CASE WHEN bs.type = 'D' THEN bs.backup_finish_date END) AS last_full,
       MAX(CASE WHEN bs.type = 'I' THEN bs.backup_finish_date END) AS last_diff,
       MAX(CASE WHEN bs.type = 'L' THEN bs.backup_finish_date END) AS last_log
FROM sys.databases d
LEFT JOIN msdb.dbo.backupset bs ON bs.database_name = d.name
WHERE d.database_id > 4 AND d.state_desc = 'ONLINE'
GROUP BY d.name
`

// forDB выполняет SQL в контексте указанной базы данных.
func forDB(db, sql string) string {
	safe := strings.ReplaceAll(db, "]", "]]")
	return fmt.Sprintf("EXEC [%s].sys.sp_executesql N'%s'", safe, strings.ReplaceAll(sql, "'", "''"))
}

const qMissingIndexDB = `
SELECT TOP 10 d.statement AS table_ref, d.equality_columns, d.inequality_columns,
       d.included_columns, gs.user_seeks, gs.user_scans,
       gs.avg_total_user_cost, gs.avg_user_impact,
       CAST(gs.avg_total_user_cost * gs.avg_user_impact *
            (gs.user_seeks + gs.user_scans) / 100.0 AS float) AS score
FROM sys.dm_db_missing_index_groups g
JOIN sys.dm_db_missing_index_group_stats gs ON gs.group_handle = g.index_group_handle
JOIN sys.dm_db_missing_index_details d      ON d.index_handle  = g.index_handle
ORDER BY score DESC
`

const qFragDB = `
SELECT TOP 15 OBJECT_SCHEMA_NAME(ips.object_id) AS schema_name,
       OBJECT_NAME(ips.object_id) AS table_name, i.name AS index_name,
       ips.index_type_desc, ips.avg_fragmentation_in_percent, ips.page_count
FROM sys.dm_db_index_physical_stats(DB_ID(), NULL, NULL, NULL, 'LIMITED') ips
JOIN sys.indexes i ON i.object_id = ips.object_id AND i.index_id = ips.index_id
WHERE ips.index_id > 0 AND ips.avg_fragmentation_in_percent >= 30 AND ips.page_count >= 1000
ORDER BY ips.page_count DESC
`

const qStaleStatsDB = `
SELECT TOP 15 OBJECT_SCHEMA_NAME(s.object_id) AS schema_name,
       OBJECT_NAME(s.object_id) AS table_name, s.name AS stats_name,
       sp.last_updated, sp.rows, sp.modification_counter
FROM sys.stats s
CROSS APPLY sys.dm_db_stats_properties(s.object_id, s.stats_id) sp
WHERE sp.rows >= 5000 AND sp.modification_counter > 0
ORDER BY CAST(sp.modification_counter AS float) / sp.rows DESC
`

const qVLFDB = `
SELECT COUNT(*) AS vlf_count FROM sys.dm_db_log_info(DB_ID())
`

// queryDescriptions — описания запросов для понятных сообщений об ошибках.
var queryDescriptions = map[string]string{
	"Q_CPU":              "загрузка ЦП",
	"Q_COUNTERS":         "счётчики производительности",
	"Q_WAITS":            "статистика ожиданий",
	"Q_REQUESTS":         "активные запросы",
	"Q_FILES":            "ввод-вывод файлов",
	"Q_DATABASES":        "список баз данных",
	"Q_CONFIG":           "конфигурация сервера",
	"Q_INSTANCE":         "информация об экземпляре",
	"Q_SYS_INFO":         "параметры системы",
	"Q_OS_MEMORY":        "память ОС",
	"Q_TOP_QUERIES":      "статистика запросов",
	"Q_BACKUPS":          "резервные копии",
	"Q_CACHED_PLANS":     "кэш планов",
	"Q_TEMPDB_FILES":     "файлы tempdb",
	"Q_TEMPDB_DETAIL":    "файлы tempdb (детали)",
	"Q_SERVICES":         "службы SQL Server",
	"Q_VOLUMES":          "тома с файлами баз",
	"Q_SECURITY":         "аутентификация и привилегии",
	"Q_SYSADMIN_LOGINS":  "логины sysadmin",
	"Q_POWER_PLAN":       "схема электропитания",
	"Q_TCP_ENABLED":      "протокол TCP/IP",
	"Q_NP_ENABLED":       "именованные каналы",
	"Q_BACKUP_MEDIA":     "размещение резервных копий",
	"Q_QUERYSTORE_DB":    "Query Store",
	"Q_FILE_CFG":         "настройки файлов",
	"Q_MISSING_INDEX_DB": "недостающие индексы",
	"Q_FRAG_DB":          "фрагментация индексов",
	"Q_STALE_STATS_DB":   "устаревшая статистика",
	"Q_VLF_DB":           "VLF журнала транзакций",
	"Q_HEAD_BLOCKERS":    "блокирующие сессии",
}
