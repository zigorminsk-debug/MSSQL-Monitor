# -*- coding: utf-8 -*-
"""SQL-запросы к системным представлениям (DMV) MS SQL Server.

Все запросы совместимы с SQL Server 2012+ (для более старых версий есть
запасные варианты *_OLD). Запросы выполняются по одному; ошибки отдельных
запросов не ломают сбор — попадают в журнал и раздел ограничений отчёта.
"""

# --- Информация об экземпляре ----------------------------------------------
Q_INSTANCE = """
SELECT CAST(SERVERPROPERTY('MachineName')  AS nvarchar(128)) AS machine_name,
       CAST(SERVERPROPERTY('ServerName')   AS nvarchar(128)) AS server_name,
       CAST(SERVERPROPERTY('InstanceName') AS nvarchar(128)) AS instance_name,
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(32)) AS product_version,
       CAST(SERVERPROPERTY('ProductLevel')   AS nvarchar(32)) AS product_level,
       CAST(SERVERPROPERTY('Edition')        AS nvarchar(128)) AS edition,
       CAST(SERVERPROPERTY('IsClustered')    AS int) AS is_clustered,
       CAST(SERVERPROPERTY('Collation')      AS nvarchar(128)) AS collation
"""

# --- Система: ЦП, память, время работы -------------------------------------
Q_SYS_INFO = """
SELECT cpu_count, hyperthread_ratio,
       CAST(physical_memory_kb AS bigint)   AS physical_memory_kb,
       CAST(committed_memory_kb AS bigint)  AS committed_kb,
       sql_memory_model_desc, sqlserver_start_time,
       DATEDIFF(SECOND, sqlserver_start_time, SYSDATETIME()) AS uptime_sec
FROM sys.dm_os_sys_info
"""

Q_SYS_INFO_OLD = """
SELECT cpu_count, hyperthread_ratio,
       CAST(physical_memory_in_bytes / 1024 AS bigint)  AS physical_memory_kb,
       CAST(committed_memory_kb AS bigint)              AS committed_kb,
       CAST('' AS nvarchar(60))                         AS sql_memory_model_desc,
       sqlserver_start_time,
       DATEDIFF(SECOND, sqlserver_start_time, SYSDATETIME()) AS uptime_sec
FROM sys.dm_os_sys_info
"""

Q_OS_MEMORY = """
SELECT CAST(total_physical_memory_kb AS bigint)    AS total_physical_kb,
       CAST(available_physical_memory_kb AS bigint) AS available_physical_kb,
       system_memory_state_desc
FROM sys.dm_os_sys_memory
"""

# --- Загрузка ЦП (кольцевой буфер монитора планировщика) --------------------
Q_CPU = """
SELECT TOP 1
  rec.value('(./Record/SchedulerMonitorEvent/SystemHealth/ProcessUtilization)[1]', 'int') AS sql_cpu,
  rec.value('(./Record/SchedulerMonitorEvent/SystemHealth/SystemIdle)[1]', 'int')        AS system_idle
FROM (
  SELECT TOP 60 CAST(record AS XML) AS rec
  FROM sys.dm_os_ring_buffers
  WHERE ring_buffer_type = N'RING_BUFFER_SCHEDULER_MONITOR'
  ORDER BY timestamp DESC
) AS t
ORDER BY t.rec.value('(./Record/@time)[1]', 'bigint') DESC
"""

# --- Счётчики производительности -------------------------------------------
PERF_COUNTER_NAMES = [
    'Batch Requests/sec', 'SQL Compilations/sec', 'SQL Re-Compilations/sec',
    'Transactions/sec', 'Page Splits/sec', 'Lock Waits/sec',
    'Page Life Expectancy', 'User Connections', 'Processes blocked',
    'Errors/sec', 'Full Scans/sec', 'Index Searches/sec',
    'Log Flushes/sec', 'Page reads/sec', 'Page writes/sec',
    'Lazy Writes/sec', 'Checkpoint pages/sec', 'Memory Grants Pending',
    'Free list stalls/sec',
    'Target Server Memory (KB)', 'Total Server Memory (KB)',
    'Stolen Server Memory (KB)',
    'Buffer cache hit ratio', 'Buffer cache hit ratio base',
]

Q_COUNTERS = """
SELECT RTRIM(object_name)   AS object_name,
       RTRIM(counter_name)  AS counter_name,
       RTRIM(instance_name) AS instance_name,
       cntr_value, cntr_type
FROM sys.dm_os_performance_counters
WHERE counter_name IN (%s)
""" % ", ".join("N'%s'" % c for c in PERF_COUNTER_NAMES)

# --- Статистика ожиданий -----------------------------------------------------
Q_WAITS = """
SELECT wait_type, waiting_tasks_count, wait_time_ms, signal_wait_time_ms, max_wait_time_ms
FROM sys.dm_os_wait_stats
"""

# --- Активные запросы --------------------------------------------------------
Q_REQUESTS = """
SELECT r.session_id, r.status, r.blocking_session_id, r.wait_type, r.wait_time,
       r.wait_resource, r.cpu_time, r.total_elapsed_time, r.reads, r.writes,
       r.logical_reads, r.command, DB_NAME(r.database_id) AS db_name,
       s.host_name, s.login_name, s.program_name,
       t.text AS query_text
FROM sys.dm_exec_requests r
LEFT JOIN sys.dm_exec_sessions s ON s.session_id = r.session_id
OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) t
WHERE r.session_id >= 50 AND r.session_id <> @@SPID
"""

Q_HEAD_BLOCKERS = """
SELECT s.session_id, s.login_name, s.host_name, s.program_name, s.status AS session_status,
       r.blocking_session_id, ib.event_info AS input_buffer
FROM sys.dm_exec_sessions s
LEFT JOIN sys.dm_exec_requests r ON r.session_id = s.session_id
OUTER APPLY sys.dm_exec_input_buffer(s.session_id, NULL) ib
WHERE s.session_id IN (%s)
"""

# --- Файлы и ввод-вывод -------------------------------------------------------
Q_FILES = """
SELECT DB_NAME(vfs.database_id) AS db_name, mf.file_id, mf.name AS file_name,
       mf.type_desc, CAST(mf.size * 8.0 / 1024 AS float) AS size_mb, mf.physical_name,
       vfs.num_of_reads, vfs.num_of_writes,
       CAST(vfs.num_of_bytes_read / 1048576.0 AS float)     AS mb_read,
       CAST(vfs.num_of_bytes_written / 1048576.0 AS float)  AS mb_written,
       vfs.io_stall_read_ms, vfs.io_stall_write_ms, vfs.io_stall
FROM sys.dm_io_virtual_file_stats(NULL, NULL) vfs
JOIN sys.master_files mf
  ON mf.database_id = vfs.database_id AND mf.file_id = vfs.file_id
"""

# --- Базы данных, настройки файлов -------------------------------------------
Q_DATABASES = """
SELECT name, recovery_model_desc, is_auto_shrink_on, is_auto_close_on,
       compatibility_level, page_verify_option_desc, log_reuse_wait_desc,
       state_desc, user_access_desc, is_read_only
FROM sys.databases
"""

Q_FILE_CFG = """
SELECT DB_NAME(database_id) AS db_name, name AS file_name, type_desc,
       CAST(size * 8.0 / 1024 AS float) AS size_mb,
       is_percent_growth, growth, physical_name
FROM sys.master_files
"""

Q_TEMPDB_FILES = """
SELECT COUNT(*) AS data_files FROM sys.master_files WHERE database_id = 2 AND type = 0
"""

# --- Конфигурация сервера ------------------------------------------------------
CONFIG_NAMES = [
    'max server memory (MB)', 'max degree of parallelism', 'cost threshold for parallelism',
    'optimize for ad hoc workloads', 'priority boost', 'lightweight pooling',
    'max worker threads', 'backup compression default', 'remote query timeout (s)',
    'recovery interval (min)', 'fill factor (%)', 'nested triggers',
]

Q_CONFIG = """
SELECT name, CAST(value_in_use AS nvarchar(100)) AS value_in_use,
       CAST(value AS nvarchar(100)) AS value_pending,
       is_dynamic, is_advanced, CAST(description AS nvarchar(600)) AS description
FROM sys.configurations
WHERE name IN (%s)
""" % ", ".join("N'%s'" % c for c in CONFIG_NAMES)

# --- Топ запросов (накопительно с момента запуска / с очистки кэша) -----------
Q_TOP_QUERIES = """
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
"""

# --- Кэш планов (для 'optimize for ad hoc workloads') -------------------------
Q_CACHED_PLANS = """
SELECT objtype, COUNT(*) AS plan_count,
       CAST(SUM(CAST(size_in_bytes AS BIGINT)) / 1048576.0 AS float) AS size_mb
FROM sys.dm_exec_cached_plans
GROUP BY objtype
"""

# --- Резервные копии -----------------------------------------------------------
Q_BACKUPS = """
SELECT d.name AS db_name,
       MAX(CASE WHEN bs.type = 'D' THEN bs.backup_finish_date END) AS last_full,
       MAX(CASE WHEN bs.type = 'I' THEN bs.backup_finish_date END) AS last_diff,
       MAX(CASE WHEN bs.type = 'L' THEN bs.backup_finish_date END) AS last_log
FROM sys.databases d
LEFT JOIN msdb.dbo.backupset bs ON bs.database_name = d.name
WHERE d.database_id > 4 AND d.state_desc = 'ONLINE'
GROUP BY d.name
"""

# --- Запросы по конкретной БД (через sp_executesql, чтобы сменить контекст) ---
def for_db(db: str, sql: str) -> str:
    """Выполнить SQL в контексте базы данных db."""
    safe_db = db.replace("]", "]]")
    return "EXEC [%s].sys.sp_executesql N'%s'" % (safe_db, sql.replace("'", "''"))


Q_MISSING_INDEX_DB = """
SELECT TOP 10 d.statement AS table_ref, d.equality_columns, d.inequality_columns,
       d.included_columns, gs.user_seeks, gs.user_scans,
       gs.avg_total_user_cost, gs.avg_user_impact,
       CAST(gs.avg_total_user_cost * gs.avg_user_impact *
            (gs.user_seeks + gs.user_scans) / 100.0 AS float) AS score
FROM sys.dm_db_missing_index_groups g
JOIN sys.dm_db_missing_index_group_stats gs ON gs.group_handle = g.index_group_handle
JOIN sys.dm_db_missing_index_details d      ON d.index_handle  = g.index_handle
ORDER BY score DESC
"""

Q_FRAG_DB = """
SELECT TOP 15 OBJECT_SCHEMA_NAME(ips.object_id) AS schema_name,
       OBJECT_NAME(ips.object_id) AS table_name, i.name AS index_name,
       ips.index_type_desc, ips.avg_fragmentation_in_percent, ips.page_count
FROM sys.dm_db_index_physical_stats(DB_ID(), NULL, NULL, NULL, 'LIMITED') ips
JOIN sys.indexes i ON i.object_id = ips.object_id AND i.index_id = ips.index_id
WHERE ips.index_id > 0 AND ips.avg_fragmentation_in_percent >= 30 AND ips.page_count >= 1000
ORDER BY ips.page_count DESC
"""

Q_STALE_STATS_DB = """
SELECT TOP 15 OBJECT_SCHEMA_NAME(s.object_id) AS schema_name,
       OBJECT_NAME(s.object_id) AS table_name, s.name AS stats_name,
       sp.last_updated, sp.rows, sp.modification_counter
FROM sys.stats s
CROSS APPLY sys.dm_db_stats_properties(s.object_id, s.stats_id) sp
WHERE sp.rows >= 5000 AND sp.modification_counter > 0
ORDER BY CAST(sp.modification_counter AS float) / sp.rows DESC
"""

Q_VLF_DB = """
SELECT COUNT(*) AS vlf_count FROM sys.dm_db_log_info(DB_ID())
"""

# --- Описание запросов (для понятных сообщений об ошибках) ---------------------
QUERY_DESCRIPTIONS = {
    'Q_CPU': 'загрузка ЦП',
    'Q_COUNTERS': 'счётчики производительности',
    'Q_WAITS': 'статистика ожиданий',
    'Q_REQUESTS': 'активные запросы',
    'Q_FILES': 'ввод-вывод файлов',
    'Q_DATABASES': 'список баз данных',
    'Q_CONFIG': 'конфигурация сервера',
    'Q_INSTANCE': 'информация об экземпляре',
    'Q_SYS_INFO': 'параметры системы',
    'Q_OS_MEMORY': 'память ОС',
    'Q_TOP_QUERIES': 'статистика запросов',
    'Q_BACKUPS': 'резервные копии',
    'Q_CACHED_PLANS': 'кэш планов',
    'Q_TEMPDB_FILES': 'файлы tempdb',
    'Q_FILE_CFG': 'настройки файлов',
    'Q_MISSING_INDEX_DB': 'недостающие индексы',
    'Q_FRAG_DB': 'фрагментация индексов',
    'Q_STALE_STATS_DB': 'устаревшая статистика',
    'Q_VLF_DB': 'VLF журнала транзакций',
    'Q_HEAD_BLOCKERS': 'блокирующие сессии',
}
