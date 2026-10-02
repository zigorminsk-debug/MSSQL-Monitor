# -*- coding: utf-8 -*-
"""Демо-движок: реалистичная имитация загруженного SQL Server (рабочие часы,
пики нагрузки, блокировки, давление на память). Позволяет изучить программу
и отчёты без реального сервера. Структура данных полностью совпадает с боевой."""
import math
import random
from datetime import datetime, timedelta

DAY = 86400


def _clamp(v, lo, hi):
    return max(lo, min(hi, v))


class DemoEngine:
    """Имитирует съём показаний с условного сервера SQLSRV-PROD-01."""

    def __init__(self, interval=30, realtime=False, seed=42, history_hours=24):
        self.interval = interval
        self.realtime = realtime
        self.rnd = random.Random(seed)
        now = datetime.now().replace(microsecond=0)
        self.sim = now if realtime else now - timedelta(hours=history_hours)
        self.uptime_days = 45
        self.history_hours = history_hours

    # ------------------------------------------------------------------ load
    def _load(self, t: datetime) -> float:
        """Коэффициент нагрузки 0..1 по времени суток (рабочие пики)."""
        h = t.hour + t.minute / 60.0

        def peak(c, w, a):
            return a * math.exp(-((h - c) ** 2) / (2 * w * w))

        load = 0.05
        load += peak(9.6, 1.6, 0.55)
        load += peak(11.4, 0.8, 0.80)
        load += peak(15.5, 1.3, 0.95)
        load += peak(18.3, 0.9, 0.28)
        if 3.0 <= h < 4.0:                      # ночное резервное копирование
            load += 0.25
        if t.weekday() >= 5:                     # выходные
            load *= 0.35
        load *= 0.88 + 0.24 * self.rnd.random()
        return _clamp(load, 0.02, 1.0)

    # --------------------------------------------------------------- sample
    def _current_state(self) -> dict:
        if self.realtime:
            self.sim = datetime.now().replace(microsecond=0)   # живое время
        t = self.sim
        load = self._load(t)
        h = t.hour + t.minute / 60.0
        cpu = _clamp(5 + 88 * load + self.rnd.gauss(0, 3), 2, 97)
        other = _clamp(2 + 9 * load + self.rnd.gauss(0, 1.5), 0.5, 22)
        idle = _clamp(100 - cpu - other, 0, 100)

        batch = 15 + 780 * load + self.rnd.gauss(0, 6)
        comp_ratio = 0.05 + (0.16 if 14.0 <= h < 16.5 else 0.0)
        rates = {
            'batch_sec': max(2, batch),
            'trans_sec': max(3, 2.4 * batch),
            'compilations_sec': max(1, batch * comp_ratio * (1 + self.rnd.gauss(0, 0.08))),
            'recompilations_sec': max(0, batch * comp_ratio * 0.12),
            'page_reads_sec': max(1, 620 * load + self.rnd.gauss(0, 25)),
            'page_writes_sec': max(0.3, 240 * load + (45 if 3 <= h < 4 else 0)),
            'log_flushes_sec': max(1, 170 * load + self.rnd.gauss(0, 6)),
            'lock_waits_sec': 0.02 + (2.6 * load if 10.8 <= h < 12.4 else 0),
            'full_scans_sec': max(0.5, 95 * load),
            'index_searches_sec': max(10, 2600 * load),
            'errors_sec': 2.1 if self.rnd.random() < 0.04 else 0.02,
            'page_splits_sec': max(0.1, 24 * load),
            'lazy_writes_sec': max(0, 60 * load * (load - 0.55)),
        }
        points = {
            'ple': int(_clamp(2500 - 2150 * load + self.rnd.gauss(0, 90), 35, 3600)),
            'connections': int(35 + 230 * load + self.rnd.gauss(0, 8)),
            'blocked': 0,
            'grants_pending': int(1 + 2 * load) if (11 <= h < 15.5 and self.rnd.random() < 0.3) else 0,
            'target_mem_mb': 60000.0,
            'total_mem_mb': _clamp(59000 + 900 * load, 30000, 60000),
            'stolen_mem_mb': _clamp(6100 + 2800 * load, 4000, 12000),
            'buffer_hit_pct': round(_clamp(99.6 - 6.5 * load + self.rnd.gauss(0, 0.4), 90, 100), 1),
        }

        # --- блокировки: утром при пике 11:00-12:24 --------------------------
        blocking_now = 10.9 <= h < 12.4 and self.rnd.random() < 0.22
        head_info = {}
        if blocking_now:
            n_block = self.rnd.randint(2, 11)
            points['blocked'] = n_block
            head_info[58] = {
                'session_id': 58, 'login_name': 'CORP\\svc_1c',
                'host_name': 'APP-SRV-03', 'program_name': '1C:Enterprise 8.3',
                'session_status': 'sleeping',
                'input_buffer': ('UPDATE dbo.Warehouse.StockUnits SET ReservedQty = ReservedQty + @p1 '
                                 'WHERE Sku IN (SELECT Sku FROM dbo.Warehouse.Reserve WHERE OrderId = @p2)'),
            }

        # --- активные запросы -----------------------------------------------
        base = [
            dict(cmd='SELECT', db='SalesDb', text=(
                'SELECT TOP 100 o.Id, c.Name, o.TotalAmount FROM Sales.Orders o '
                'JOIN Sales.Customers c ON c.CustomerId = o.CustomerId '
                'WHERE o.CreatedAt >= @FromDate AND o.StatusId = @Status '
                'ORDER BY o.TotalAmount DESC'),
                reads=26500, cpu=140, elapsed=900, wait=None),
            dict(cmd='UPDATE', db='SalesDb', text=(
                'UPDATE dbo.Warehouse.StockUnits SET ReservedQty = ReservedQty + @delta '
                'WHERE Sku = @sku'), reads=210, cpu=18, elapsed=45, wait=None),
            dict(cmd='SELECT', db='ReportsDb', text=(
                'SELECT d.Date, p.Name, SUM(f.Amount) AS Total FROM Reports.FactSales f '
                'JOIN Reports.DimDate d ON d.DateKey = f.DateKey '
                'JOIN Reports.DimProduct p ON p.ProductKey = f.ProductKey '
                'WHERE d.Year = @y AND d.Month = @m GROUP BY d.Date, p.Name'),
                reads=1420000, cpu=9500, elapsed=11500, wait='CXPACKET'),
            dict(cmd='SELECT', db='StatsDb', text=(
                'SELECT e.EventId, e.Payload FROM dbo.Events e '
                "WHERE e.CreatedAt >= DATEADD(HOUR, -24, SYSUTCDATETIME()) "
                'AND e.Payload LIKE @pattern'), reads=890000, cpu=4300, elapsed=9400, wait='PAGEIOLATCH_SH'),
            dict(cmd='INSERT', db='SalesDb', text=(
                'INSERT INTO dbo.AuditLog (Entity, ActionId, At, Detail) '
                'VALUES (@entity, @action, SYSUTCDATETIME(), @detail)'),
                reads=12, cpu=2, elapsed=8, wait=None),
            dict(cmd='SELECT', db='SalesDb', text=(
                'EXEC dbo.usp_CalcOrderTotals @FromDate = @p0, @ToDate = @p1'),
                reads=96000, cpu=780, elapsed=2600, wait='LCK_M_S'),
        ]
        requests = []
        sid = 51
        for q in base:
            if self.rnd.random() > 0.25 + 0.7 * load:
                continue
            sid += 1
            r = dict(session_id=sid, status='suspended' if q['wait'] else 'running',
                     blocking_session_id=0, wait_type=q['wait'],
                     wait_time=self.rnd.randint(20, 9000) if q['wait'] else 0,
                     wait_resource='14:5:3312' if q['wait'] else '',
                     cpu_time=int(q['cpu'] * (0.5 + load)), total_elapsed_time=int(q['elapsed'] * (0.4 + load)),
                     reads=int(q['reads'] * (0.5 + load)), writes=self.rnd.randint(0, 30),
                     logical_reads=int(q['reads'] * 2.2), command=q['cmd'], db_name=q['db'],
                     host_name='APP-SRV-0%d' % self.rnd.randint(1, 5),
                     login_name=self.rnd.choice(['CORP\\svc_web', 'CORP\\app_user', 'sa']),
                     program_name=self.rnd.choice(['.Net SqlClient Data Provider', 'Microsoft JDBC Driver', 'SSMS']),
                     query_text=q['text'])
            if q['wait'] == 'LCK_M_S' and blocking_now:
                r['blocking_session_id'] = 58
            requests.append(r)
        if blocking_now:
            for i in range(min(4, points['blocked'])):
                sid += 1
                requests.append(dict(
                    session_id=sid, status='suspended', blocking_session_id=58,
                    wait_type='LCK_M_S', wait_time=self.rnd.randint(500, 42000),
                    wait_resource='14:5:%d' % self.rnd.randint(100, 9999),
                    cpu_time=self.rnd.randint(0, 200), total_elapsed_time=self.rnd.randint(2000, 60000),
                    reads=self.rnd.randint(10, 500), writes=0, logical_reads=self.rnd.randint(20, 900),
                    command='UPDATE', db_name='SalesDb', host_name='APP-SRV-02',
                    login_name='CORP\\svc_web', program_name='.Net SqlClient Data Provider',
                    query_text='UPDATE dbo.Warehouse.StockUnits SET ReservedQty = ReservedQty - @d WHERE Sku = @sku'))

        # --- ожидания (приращения за интервал, мс) ---------------------------
        dt = self.interval
        w = {
            'PAGEIOLATCH_SH': 640 * load,
            'PAGEIOLATCH_EX': 95 * load,
            'WRITELOG': 235 * (0.35 + load),
            'LCK_M_S': 1250 * load if 10.8 <= h < 12.4 else 18 * load,
            'ASYNC_NETWORK_IO': 300 * load,
            'SOS_SCHEDULER_YIELD': 210 * load,
            'THREADPOOL': 140 * load if load > 0.85 else 0,
            'BACKUPIO': 2400 if 3 <= h < 4 else 4,
            'BACKUPBUFFER': 900 if 3 <= h < 4 else 2,
        }
        if 13.8 <= h < 16.8:                       # вечерний отчёт — параллелизм
            w['CXPACKET'] = 1750 * load
            w['CXCONSUMER'] = 640 * load
        else:
            w['CXPACKET'] = 120 * load
            w['CXCONSUMER'] = 60 * load
        if 10.5 <= h < 15.5:                       # давление на память
            w['RESOURCE_SEMAPHORE'] = 170 * load
        waits = []
        for k, v in w.items():
            v = max(0.0, v * dt / 30.0 * (0.8 + 0.4 * self.rnd.random()))
            if v >= 1:
                waits.append(dict(wait_type=k, wait_ms=round(v, 1),
                                  signal_ms=round(v * 0.12, 1),
                                  tasks=max(1, int(v / 40))))

        return dict(ts=t, source='demo', cpu={'sql': round(cpu, 1), 'other': round(other, 1),
                                              'idle': round(idle, 1)},
                    points=points, rates={k: round(v, 2) for k, v in rates.items()},
                    waits_delta=waits, requests=requests, head_info=head_info)

    # --------------------------------------------------------------- public
    def light_sample(self) -> dict:
        state = self._current_state()
        if not self.realtime:
            self.sim = self.sim + timedelta(seconds=self.interval)
        return state

    # ---------------------------------------------------------- full данные
    def full_snapshot(self) -> dict:
        s = self._current_state()
        s['instance'] = {
            'machine_name': 'SQLSRV-PROD-01', 'server_name': 'SQLSRV-PROD-01\\PROD',
            'instance_name': 'PROD', 'product_version': '15.0.4360.2', 'product_level': 'RTM CU12',
            'edition': 'SQL Server 2019 Standard Edition (64-bit)', 'is_clustered': 0,
            'collation': 'Cyrillic_General_CI_AS',
        }
        s['sys'] = {'cpu_count': 16, 'hyperthread_ratio': 2, 'physical_memory_kb': 67108864,
                    'committed_kb': 58_000_000, 'sql_memory_model_desc': 'CONVENTIONAL',
                    'sqlserver_start_time': datetime.now() - timedelta(days=self.uptime_days),
                    'uptime_sec': self.uptime_days * DAY}
        s['os_mem'] = {'total_physical_kb': 67108864, 'available_physical_kb': 2932736,
                       'system_memory_state_desc': 'Available physical memory is low'}
        s['config'] = {
            'max server memory (MB)': '2147483647', 'max degree of parallelism': '0',
            'cost threshold for parallelism': '5', 'optimize for ad hoc workloads': '0',
            'priority boost': '0', 'lightweight pooling': '0', 'max worker threads': '0',
            'backup compression default': '0', 'remote query timeout (s)': '600',
            'recovery interval (min)': '0', 'fill factor (%)': '0', 'nested triggers': '1',
        }
        s['databases'] = [
            dict(name='master', recovery_model_desc='SIMPLE', is_auto_shrink_on=0, is_auto_close_on=0,
                 compatibility_level=150, page_verify_option_desc='CHECKSUM', log_reuse_wait_desc='NOTHING',
                 state_desc='ONLINE', user_access_desc='MULTI_USER', is_read_only=0),
            dict(name='SalesDb', recovery_model_desc='FULL', is_auto_shrink_on=0, is_auto_close_on=0,
                 compatibility_level=150, page_verify_option_desc='CHECKSUM', log_reuse_wait_desc='LOG_BACKUP',
                 state_desc='ONLINE', user_access_desc='MULTI_USER', is_read_only=0),
            dict(name='ReportsDb', recovery_model_desc='SIMPLE', is_auto_shrink_on=1, is_auto_close_on=0,
                 compatibility_level=110, page_verify_option_desc='CHECKSUM', log_reuse_wait_desc='NOTHING',
                 state_desc='ONLINE', user_access_desc='MULTI_USER', is_read_only=0),
            dict(name='StatsDb', recovery_model_desc='FULL', is_auto_shrink_on=0, is_auto_close_on=1,
                 compatibility_level=150, page_verify_option_desc='CHECKSUM', log_reuse_wait_desc='NOTHING',
                 state_desc='ONLINE', user_access_desc='MULTI_USER', is_read_only=0),
            dict(name='Legacy2014', recovery_model_desc='FULL', is_auto_shrink_on=0, is_auto_close_on=0,
                 compatibility_level=100, page_verify_option_desc='NONE', log_reuse_wait_desc='NOTHING',
                 state_desc='ONLINE', user_access_desc='MULTI_USER', is_read_only=1),
        ]
        s['tempdb_files'] = 1
        load = self._load(self.sim)
        s['files_mode'] = 'window'
        s['files_window_sec'] = int(self.history_hours * 3600)
        s['files'] = [
            dict(db_name='SalesDb', file_name='SalesDb_data01', type_desc='ROWS', size_mb=122880,
                 physical_name='D:\\MSSQL\\DATA\\SalesDb_data01.mdf',
                 reads_sec=round(210 * load + 3, 1), writes_sec=round(85 * load + 1, 1),
                 mb_read_sec=round(1.9 * load + 0.02, 2), mb_write_sec=round(0.6 * load + 0.01, 2),
                 read_latency_ms=round(_clamp(17.5 * load + 2, 2, 40) + self.rnd.gauss(0, 1.5), 1),
                 write_latency_ms=round(_clamp(7 * load + 1, 1, 20), 1)),
            dict(db_name='SalesDb', file_name='SalesDb_log', type_desc='LOG', size_mb=24576,
                 physical_name='E:\\MSSQL\\LOG\\SalesDb_log.ldf',
                 reads_sec=round(2 + 3 * load, 1), writes_sec=round(170 * load + 2, 1),
                 mb_read_sec=round(0.01, 2), mb_write_sec=round(3.4 * load + 0.05, 2),
                 read_latency_ms=round(4, 1), write_latency_ms=round(_clamp(44 * load + 6, 4, 70), 1)),
            dict(db_name='ReportsDb', file_name='ReportsDb_data', type_desc='ROWS', size_mb=73728,
                 physical_name='D:\\MSSQL\\DATA\\ReportsDb.mdf',
                 reads_sec=round(400 * load + 4, 1), writes_sec=round(30 * load + 0.5, 1),
                 mb_read_sec=round(4.2 * load + 0.05, 2), mb_write_sec=round(0.2 * load + 0.01, 2),
                 read_latency_ms=round(_clamp(24 * load + 3, 2, 48), 1), write_latency_ms=round(6, 1)),
            dict(db_name='tempdb', file_name='tempdev', type_desc='ROWS', size_mb=8192,
                 physical_name='D:\\MSSQL\\DATA\\tempdb.mdf',
                 reads_sec=round(60 * load + 2, 1), writes_sec=round(310 * load + 5, 1),
                 mb_read_sec=round(0.4 * load, 2), mb_write_sec=round(2.2 * load + 0.02, 2),
                 read_latency_ms=round(_clamp(5 * load + 1, 0.5, 12), 1), write_latency_ms=round(_clamp(9 * load + 1, 1, 20), 1)),
            dict(db_name='tempdb', file_name='templog', type_desc='LOG', size_mb=2048,
                 physical_name='D:\\MSSQL\\LOG\\templog.ldf',
                 reads_sec=0.4, writes_sec=round(120 * load + 3, 1), mb_read_sec=0.0,
                 mb_write_sec=round(1.1 * load + 0.01, 2), read_latency_ms=3.0,
                 write_latency_ms=round(_clamp(12 * load + 2, 1, 25), 1)),
            dict(db_name='StatsDb', file_name='StatsDb_data', type_desc='ROWS', size_mb=51200,
                 physical_name='D:\\MSSQL\\DATA\\StatsDb.mdf',
                 reads_sec=round(260 * load + 2, 1), writes_sec=round(140 * load + 2, 1),
                 mb_read_sec=round(2.1 * load, 2), mb_write_sec=round(1.4 * load + 0.01, 2),
                 read_latency_ms=round(_clamp(28 * load + 4, 2, 55), 1), write_latency_ms=round(_clamp(11 * load + 2, 1, 25), 1)),
        ]
        s['file_cfg'] = [
            dict(db_name='SalesDb', file_name='SalesDb_data01', type_desc='ROWS', size_mb=122880,
                 is_percent_growth=0, growth=256, physical_name='D:\\MSSQL\\DATA\\SalesDb_data01.mdf'),
            dict(db_name='SalesDb', file_name='SalesDb_log', type_desc='LOG', size_mb=24576,
                 is_percent_growth=1, growth=10, physical_name='E:\\MSSQL\\LOG\\SalesDb_log.ldf'),
            dict(db_name='ReportsDb', file_name='ReportsDb_data', type_desc='ROWS', size_mb=73728,
                 is_percent_growth=0, growth=128, physical_name='D:\\MSSQL\\DATA\\ReportsDb.mdf'),
            dict(db_name='tempdb', file_name='tempdev', type_desc='ROWS', size_mb=8192,
                 is_percent_growth=0, growth=8, physical_name='D:\\MSSQL\\DATA\\tempdb.mdf'),
            dict(db_name='StatsDb', file_name='StatsDb_data', type_desc='ROWS', size_mb=51200,
                 is_percent_growth=0, growth=64, physical_name='D:\\MSSQL\\DATA\\StatsDb.mdf'),
        ]
        s['backups'] = [
            dict(db_name='SalesDb', last_full=datetime.now() - timedelta(days=2, hours=5),
                 last_diff=None, last_log=datetime.now() - timedelta(days=3, hours=2)),
            dict(db_name='ReportsDb', last_full=datetime.now() - timedelta(days=9), last_diff=None, last_log=None),
            dict(db_name='StatsDb', last_full=datetime.now() - timedelta(hours=20), last_diff=None,
                 last_log=datetime.now() - timedelta(hours=20)),
        ]
        s['cached_plans'] = [
            dict(objtype='Adhoc', plan_count=41230, size_mb=1987.4),
            dict(objtype='Prepared', plan_count=8145, size_mb=512.9),
            dict(objtype='Proc', plan_count=3120, size_mb=745.1),
        ]
        s['top_queries'] = [
            dict(db_name='ReportsDb', execution_count=5420, total_cpu_ms=28_940_000, avg_cpu_ms=5340.0,
                 avg_elapsed_ms=7120.0, total_elapsed_ms=38_610_000, avg_logical_reads=1_420_000,
                 total_physical_reads=984_000_000, total_writes=0, creation_time=datetime.now() - timedelta(days=40),
                 last_execution_time=datetime.now() - timedelta(hours=2),
                 query_text='SELECT d.Date, p.Name, SUM(f.Amount) AS Total FROM Reports.FactSales f '
                            'JOIN Reports.DimDate d ON d.DateKey = f.DateKey '
                            'JOIN Reports.DimProduct p ON p.ProductKey = f.ProductKey '
                            'WHERE d.Year = @y GROUP BY d.Date, p.Name'),
            dict(db_name='StatsDb', execution_count=128900, total_cpu_ms=19_450_000, avg_cpu_ms=150.9,
                 avg_elapsed_ms=540.0, total_elapsed_ms=69_600_000, avg_logical_reads=210_000,
                 total_physical_reads=4_120_000_000, total_writes=12,
                 creation_time=datetime.now() - timedelta(days=44),
                 last_execution_time=datetime.now() - timedelta(minutes=3),
                 query_text='SELECT e.EventId, e.Payload FROM dbo.Events e '
                            'WHERE e.CreatedAt >= DATEADD(HOUR, -24, SYSUTCDATETIME()) AND e.Payload LIKE @pattern'),
            dict(db_name='SalesDb', execution_count=2_100_000, total_cpu_ms=15_200_000, avg_cpu_ms=7.2,
                 avg_elapsed_ms=9.5, total_elapsed_ms=19_950_000, avg_logical_reads=420,
                 total_physical_reads=8_900_000, total_writes=4,
                 creation_time=datetime.now() - timedelta(days=45),
                 last_execution_time=datetime.now() - timedelta(seconds=10),
                 query_text='UPDATE dbo.Warehouse.StockUnits SET ReservedQty = ReservedQty + @delta WHERE Sku = @sku'),
            dict(db_name='SalesDb', execution_count=184_000, total_cpu_ms=12_800_000, avg_cpu_ms=69.6,
                 avg_elapsed_ms=210.0, total_elapsed_ms=38_640_000, avg_logical_reads=26_500,
                 total_physical_reads=220_000_000, total_writes=0,
                 creation_time=datetime.now() - timedelta(days=38),
                 last_execution_time=datetime.now() - timedelta(minutes=1),
                 query_text='SELECT TOP 100 o.Id, c.Name, o.TotalAmount FROM Sales.Orders o '
                            'JOIN Sales.Customers c ON c.CustomerId = o.CustomerId '
                            'WHERE o.CreatedAt >= @FromDate AND o.StatusId = @Status ORDER BY o.TotalAmount DESC'),
            dict(db_name='SalesDb', execution_count=410, total_cpu_ms=4_300_000, avg_cpu_ms=10487.8,
                 avg_elapsed_ms=25800.0, total_elapsed_ms=10_578_000, avg_logical_reads=4_900_000,
                 total_physical_reads=820_000_000, total_writes=0,
                 creation_time=datetime.now() - timedelta(days=30),
                 last_execution_time=datetime.now() - timedelta(hours=3),
                 query_text='EXEC dbo.usp_CalcOrderTotals @FromDate = @p0, @ToDate = @p1'),
            dict(db_name='StatsDb', execution_count=95_000, total_cpu_ms=3_800_000, avg_cpu_ms=40.0,
                 avg_elapsed_ms=48.0, total_elapsed_ms=4_560_000, avg_logical_reads=980,
                 total_physical_reads=44_000_000, total_writes=33_000,
                 creation_time=datetime.now() - timedelta(days=41),
                 last_execution_time=datetime.now() - timedelta(seconds=45),
                 query_text='INSERT INTO dbo.Events (EventId, CreatedAt, Payload) VALUES (@id, SYSUTCDATETIME(), @payload)'),
        ]
        s['missing_indexes'] = [
            dict(db_name='SalesDb', table_ref='[Sales].[Orders]', equality_columns='[StatusId], [CustomerId]',
                 inequality_columns='[CreatedAt]', included_columns='[TotalAmount]',
                 user_seeks=184_212, user_scans=0, avg_total_user_cost=0.912, avg_user_impact=88.34, score=148_500),
            dict(db_name='StatsDb', table_ref='[dbo].[Events]', equality_columns=None,
                 inequality_columns='[CreatedAt]', included_columns='[Payload]',
                 user_seeks=96_400, user_scans=1_204, avg_total_user_cost=14.872, avg_user_impact=74.10, score=107_200),
            dict(db_name='SalesDb', table_ref='[Sales].[OrderLines]', equality_columns='[ProductId]',
                 inequality_columns=None, included_columns='[Qty], [Price]',
                 user_seeks=51_800, user_scans=0, avg_total_user_cost=0.445, avg_user_impact=91.20, score=21_000),
        ]
        s['frag'] = [
            dict(db_name='ReportsDb', schema_name='Reports', table_name='FactSales', index_name='IX_FactSales_Date',
                 index_type_desc='NONCLUSTERED INDEX', avg_fragmentation_in_percent=74.2, page_count=92_300),
            dict(db_name='SalesDb', schema_name='Sales', table_name='Orders', index_name='IX_Orders_CreatedAt',
                 index_type_desc='NONCLUSTERED INDEX', avg_fragmentation_in_percent=57.8, page_count=48_100),
            dict(db_name='StatsDb', schema_name='dbo', table_name='Events', index_name='PK_Events',
                 index_type_desc='CLUSTERED INDEX', avg_fragmentation_in_percent=45.1, page_count=12_400),
            dict(db_name='ReportsDb', schema_name='Reports', table_name='DimProduct', index_name='IX_DimProduct_Name',
                 index_type_desc='NONCLUSTERED INDEX', avg_fragmentation_in_percent=33.9, page_count=1_050),
        ]
        s['stale_stats'] = [
            dict(db_name='SalesDb', schema_name='Sales', table_name='Orders', stats_name='_WA_Sys_00000007_1273C1CD',
                 last_updated=datetime.now() - timedelta(days=17), rows=4_920_000, modification_counter=1_610_000),
            dict(db_name='SalesDb', schema_name='Sales', table_name='OrderLines', stats_name='PK_OrderLines',
                 last_updated=datetime.now() - timedelta(days=23), rows=19_400_000, modification_counter=5_900_000),
            dict(db_name='StatsDb', schema_name='dbo', table_name='Events', stats_name='IX_Events_CreatedAt',
                 last_updated=datetime.now() - timedelta(days=9), rows=88_000_000, modification_counter=31_000_000),
        ]
        s['vlf'] = [
            dict(db_name='SalesDb', vlf_count=1184),
            dict(db_name='StatsDb', vlf_count=342),
            dict(db_name='ReportsDb', vlf_count=97),
        ]
        s['errors'] = []
        return s


def generate_demo_history(store, engine=None, hours=24, interval=30):
    """Заполнить хранилище 24 часами демо-истории (для суточного отчёта)."""
    from datetime import datetime, timedelta
    from .collector import finalize_sample   # лениво, чтобы избежать циклического импорта
    eng = engine or DemoEngine(interval=interval, history_hours=hours)
    now = datetime.now()
    n = int(hours * 3600 / interval)
    eng.sim = now - timedelta(seconds=n * interval)
    for _ in range(n):
        smp = eng.light_sample()
        store.save_sample(finalize_sample(smp, 10000))
    return n
