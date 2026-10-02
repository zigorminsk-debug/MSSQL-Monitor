# -*- coding: utf-8 -*-
"""Сбор показателей с SQL Server (через DMV) и хранилище истории в SQLite."""
import hashlib
import logging
import sqlite3
import threading
from datetime import datetime, timedelta
from typing import Dict, List, Optional

from . import queries as Q
from .analyzer import is_ignorable_wait
from .demo import DemoEngine

log = logging.getLogger("mssql.collector")

# Счётчики: семантический ключ -> имя счётчика sys.dm_os_performance_counters
_DELTA_COUNTERS = {
    'batch_sec': 'Batch Requests/sec',
    'compilations_sec': 'SQL Compilations/sec',
    'recompilations_sec': 'SQL Re-Compilations/sec',
    'trans_sec': 'Transactions/sec',
    'page_reads_sec': 'Page reads/sec',
    'page_writes_sec': 'Page writes/sec',
    'log_flushes_sec': 'Log Flushes/sec',
    'lock_waits_sec': 'Lock Waits/sec',
    'full_scans_sec': 'Full Scans/sec',
    'index_searches_sec': 'Index Searches/sec',
    'errors_sec': 'Errors/sec',
    'page_splits_sec': 'Page Splits/sec',
    'lazy_writes_sec': 'Lazy Writes/sec',
}
_POINT_COUNTERS = {
    'ple': 'Page Life Expectancy',
    'connections': 'User Connections',
    'blocked': 'Processes blocked',
    'grants_pending': 'Memory Grants Pending',
}
_MEM_COUNTERS = {
    'target_mem_mb': 'Target Server Memory (KB)',
    'total_mem_mb': 'Total Server Memory (KB)',
    'stolen_mem_mb': 'Stolen Server Memory (KB)',
}


def _pick(rows: List[dict], name: str):
    """Выбрать значение счётчика по имени (приоритет экземпляра _Total)."""
    cands = [r for r in rows if r.get('counter_name') == name]
    if not cands:
        return None
    for r in cands:
        if r.get('instance_name') == '_Total':
            return r['cntr_value']
    for r in cands:
        if r.get('instance_name') == '':
            return r['cntr_value']
    return cands[0]['cntr_value']


def finalize_sample(state: dict, slow_ms: int) -> dict:
    """Добавить в «сырой» снимок блокировки и медленные запросы."""
    reqs = state.get('requests') or []
    blocked = [r for r in reqs if r.get('blocking_session_id') and r['blocking_session_id'] > 0]
    chains = []
    if blocked:
        blocked_ids = {r['session_id'] for r in blocked}
        head_ids = {r['blocking_session_id'] for r in blocked} - blocked_ids
        head_info = state.get('head_info') or {}
        for hid in sorted(head_ids):
            victims = [r for r in blocked if r['blocking_session_id'] == hid]
            info = head_info.get(hid, {})
            chains.append({
                'head': hid,
                'head_query': (info.get('input_buffer') or '— (сессия не выполняет запрос — вероятно, открытая транзакция)')[:1500],
                'head_login': info.get('login_name', ''),
                'head_program': info.get('program_name', ''),
                'victims': len(victims),
                'max_wait_ms': max((r.get('wait_time') or 0) for r in victims),
                'victim_query': (victims[0].get('query_text') or '')[:800] if victims else '',
            })
        if not head_ids:  # все блокирующие сами заблокированы — цикл
            chains.append({'head': 0, 'head_query': '—', 'head_login': '', 'head_program': '',
                           'victims': len(blocked), 'max_wait_ms': max((r.get('wait_time') or 0) for r in blocked),
                           'victim_query': ''})
    state['blocking'] = {'count': len(blocked), 'chains': chains}
    state['slow'] = [
        {'session_id': r['session_id'], 'elapsed_ms': r.get('total_elapsed_time') or 0,
         'cpu_ms': r.get('cpu_time') or 0, 'logical_reads': r.get('logical_reads') or 0,
         'db_name': r.get('db_name') or '', 'text': (r.get('query_text') or '')[:1500],
         'wait_type': r.get('wait_type')}
        for r in reqs if (r.get('total_elapsed_time') or 0) >= max(1000, slow_ms)
    ]
    return state


class Collector:
    """Собирает лёгкие (частые) и полные (тяжёлые) снимки состояния сервера."""

    def __init__(self, backend=None, settings=None, demo: Optional[DemoEngine] = None):
        self.backend = backend
        self.settings = settings
        self.demo = demo
        self._prev = None       # предыдущее состояние (ts, counters, waits, files)
        self._prev_files = None

    # ------------------------------------------------------------- helpers
    def _try(self, key: str, sql: str, params=None):
        """Выполнить запрос; при ошибке записать в журнал и вернуть None."""
        try:
            return self.backend.query(sql, params)
        except Exception as e:
            desc = Q.QUERY_DESCRIPTIONS.get(key, key)
            log.warning("Не удалось получить %s: %s", desc, e)
            if self._snapshot_errors is not None:
                self._snapshot_errors.append("%s: %s" % (desc, _short_err(e)))
            return None

    # ----------------------------------------------------------- лёгкий
    def light_sample(self) -> dict:
        if self.demo is not None:
            return finalize_sample(self.demo.light_sample(),
                                   self.settings.slow_query_ms if self.settings else 10000)
        self._snapshot_errors = None
        ts = datetime.now()
        cpu_rows = self._try('Q_CPU', Q.Q_CPU)
        cnt_rows = self._try('Q_COUNTERS', Q.Q_COUNTERS) or []
        wait_rows = self._try('Q_WAITS', Q.Q_WAITS) or []
        req_rows = self._try('Q_REQUESTS', Q.Q_REQUESTS) or []

        cpu = {'sql': 0.0, 'other': 0.0, 'idle': 0.0}
        if cpu_rows:
            sql_cpu = cpu_rows[0].get('sql_cpu')
            idle = cpu_rows[0].get('system_idle')
            if sql_cpu is not None and idle is not None:
                cpu['sql'] = float(sql_cpu)
                cpu['idle'] = float(idle)
                cpu['other'] = max(0.0, 100.0 - idle - sql_cpu)

        # семантические счётчики
        counters: Dict[str, float] = {}
        for k, name in _POINT_COUNTERS.items():
            v = _pick(cnt_rows, name)
            if v is not None:
                counters[k] = v
        for k, name in _MEM_COUNTERS.items():
            v = _pick(cnt_rows, name)
            if v is not None:
                counters[k] = float(v) / 1024.0  # KB -> MB
        ratio = _pick(cnt_rows, 'Buffer cache hit ratio')
        base = _pick(cnt_rows, 'Buffer cache hit ratio base')
        for k, name in _DELTA_COUNTERS.items():
            v = _pick(cnt_rows, name)
            if v is not None:
                counters[k] = float(v)

        points = {k: counters.get(k) for k in
                  ('ple', 'connections', 'blocked', 'grants_pending',
                   'target_mem_mb', 'total_mem_mb', 'stolen_mem_mb')}
        points['buffer_hit_pct'] = round(float(ratio) / float(base) * 100.0, 1) if (ratio and base) else None

        # скорости (дельты счётчиков)
        rates = {k: 0.0 for k in _DELTA_COUNTERS}
        waits_delta = []
        prev = self._prev
        if prev is not None:
            dt = (ts - prev['ts']).total_seconds()
            if dt > 0:
                for k in _DELTA_COUNTERS:
                    cur, old = counters.get(k), prev['counters'].get(k)
                    if cur is not None and old is not None and cur >= old:
                        rates[k] = max(0.0, (cur - old) / dt)
                for w in wait_rows:
                    wt = w['wait_type']
                    if is_ignorable_wait(wt):
                        continue
                    old = prev['waits'].get(wt)
                    if not old:
                        continue
                    d_ms = w['wait_time_ms'] - old['wait_ms']
                    if d_ms > 0.5:
                        waits_delta.append({
                            'wait_type': wt, 'wait_ms': round(d_ms, 1),
                            'signal_ms': round(max(0.0, w['signal_wait_time_ms'] - old['sig_ms']), 1),
                            'tasks': max(0, w['waiting_tasks_count'] - old['tasks'])})

        # head-blocker'ы
        head_info = {}
        blocked_ids = sorted({r['blocking_session_id'] for r in req_rows
                              if r.get('blocking_session_id') and r['blocking_session_id'] > 0})
        if blocked_ids:
            in_list = ", ".join(str(int(i)) for i in blocked_ids[:32])
            rows = self._try('Q_HEAD_BLOCKERS', Q.Q_HEAD_BLOCKERS % in_list) or []
            head_info = {r['session_id']: r for r in rows if r.get('session_id')}

        # запомнить кумулятивы
        self._prev = {'ts': ts, 'counters': counters,
                      'waits': {w['wait_type']: {'wait_ms': w['wait_time_ms'],
                                                 'sig_ms': w['signal_wait_time_ms'],
                                                 'tasks': w['waiting_tasks_count']} for w in wait_rows}}

        state = {'ts': ts, 'source': 'live', 'cpu': cpu, 'points': points,
                 'rates': rates, 'waits_delta': waits_delta,
                 'requests': [dict(r) for r in req_rows], 'head_info': head_info}
        return finalize_sample(state, self.settings.slow_query_ms if self.settings else 10000)

    # ----------------------------------------------------------- полный
    def full_snapshot(self) -> dict:
        if self.demo is not None:
            return finalize_sample(self.demo.full_snapshot(),
                                   self.settings.slow_query_ms if self.settings else 10000)
        self._snapshot_errors = []
        s = self.light_sample()
        s['source'] = 'live'
        s['errors'] = self._snapshot_errors

        row0 = (self._try('Q_INSTANCE', Q.Q_INSTANCE) or [{}])[0]
        s['instance'] = row0

        si = self._try('Q_SYS_INFO', Q.Q_SYS_INFO)
        if si is None:
            si = self._try('Q_SYS_INFO', Q.Q_SYS_INFO_OLD)
        s['sys'] = (si or [{}])[0]

        s['os_mem'] = (self._try('Q_OS_MEMORY', Q.Q_OS_MEMORY) or [{}])[0]
        s['databases'] = self._try('Q_DATABASES', Q.Q_DATABASES) or []
        s['config'] = {r['name']: r['value_in_use'] for r in (self._try('Q_CONFIG', Q.Q_CONFIG) or [])}
        s['file_cfg'] = self._try('Q_FILE_CFG', Q.Q_FILE_CFG) or []
        s['backups'] = self._try('Q_BACKUPS', Q.Q_BACKUPS) or []
        s['cached_plans'] = self._try('Q_CACHED_PLANS', Q.Q_CACHED_PLANS) or []
        s['top_queries'] = self._try('Q_TOP_QUERIES', Q.Q_TOP_QUERIES) or []
        s['tempdb_files'] = ((self._try('Q_TEMPDB_FILES', Q.Q_TEMPDB_FILES) or [{}])[0]).get('data_files')

        # --- файлы: дельты с прошлого раза или средние с запуска сервера ----
        files = self._try('Q_FILES', Q.Q_FILES) or []
        had_prev = self._prev_files is not None
        s['files'] = self._files_rates(files, s.get('sys', {}).get('uptime_sec'))
        s['files_mode'] = 'window' if had_prev else 'startup'

        # --- тяжёлые запросы по базам --------------------------------------
        s['missing_indexes'], s['frag'], s['stale_stats'], s['vlf'] = [], [], [], []
        if self.settings is None or self.settings.deep_index_scan:
            for db in (d for d in s['databases']
                       if d.get('name') not in ('master', 'model', 'msdb', 'tempdb', 'mssqlsystemresource')
                       and d.get('state_desc') == 'ONLINE' and not d.get('is_read_only')):
                name = db['name']
                for r in (self._try('Q_MISSING_INDEX_DB', Q.for_db(name, Q.Q_MISSING_INDEX_DB)) or []):
                    if r:
                        r['db_name'] = name
                        s['missing_indexes'].append(r)
                for r in self._try('Q_FRAG_DB', Q.for_db(name, Q.Q_FRAG_DB)) or []:
                    r['db_name'] = name
                    s['frag'].append(r)
                for r in self._try('Q_STALE_STATS_DB', Q.for_db(name, Q.Q_STALE_STATS_DB)) or []:
                    r['db_name'] = name
                    s['stale_stats'].append(r)
                for r in self._try('Q_VLF_DB', Q.for_db(name, Q.Q_VLF_DB)) or []:
                    r['db_name'] = name
                    s['vlf'].append(r)
            s['missing_indexes'] = sorted(s['missing_indexes'], key=lambda r: r.get('score') or 0, reverse=True)[:8]
            s['frag'] = sorted(s['frag'], key=lambda r: r.get('page_count') or 0, reverse=True)[:10]
            s['stale_stats'] = sorted(s['stale_stats'],
                                      key=lambda r: (r.get('modification_counter') or 0) / max(1, r.get('rows') or 1),
                                      reverse=True)[:10]
        return s

    def _files_rates(self, rows: List[dict], uptime_sec):
        """Скорости и задержки по файлам: за окно с прошлого снимка или с запуска."""
        out = []
        prev = self._prev_files
        window = None
        now = datetime.now()
        if prev:
            window = (now - prev['ts']).total_seconds()
        for r in rows:
            e = {'db_name': r['db_name'], 'file_name': r['file_name'], 'type_desc': r['type_desc'],
                 'size_mb': r['size_mb'], 'physical_name': r['physical_name']}
            if prev and window and window > 0:
                old = prev['files'].get((r['db_name'], r['file_name']))
            else:
                old = None
            if old:
                dr = max(0, r['num_of_reads'] - old['r'])
                dw = max(0, r['num_of_writes'] - old['w'])
                dsr = max(0, r['io_stall_read_ms'] - old['sr'])
                dsw = max(0, r['io_stall_write_ms'] - old['sw'])
                dmr = max(0.0, r['mb_read'] - old['mr'])
                dmw = max(0.0, r['mb_written'] - old['mw'])
                e.update(reads_sec=dr / window, writes_sec=dw / window,
                         mb_read_sec=dmr / window, mb_write_sec=dmw / window,
                         read_latency_ms=dsr / dr if dr else 0.0,
                         write_latency_ms=dsw / dw if dw else 0.0)
            else:
                t = max(1.0, uptime_sec or 3600.0)
                e.update(reads_sec=r['num_of_reads'] / t, writes_sec=r['num_of_writes'] / t,
                         mb_read_sec=r['mb_read'] / t, mb_write_sec=r['mb_written'] / t,
                         read_latency_ms=r['io_stall_read_ms'] / r['num_of_reads'] if r['num_of_reads'] else 0.0,
                         write_latency_ms=r['io_stall_write_ms'] / r['num_of_writes'] if r['num_of_writes'] else 0.0)
            for k in ('reads_sec', 'writes_sec', 'mb_read_sec', 'mb_write_sec',
                      'read_latency_ms', 'write_latency_ms'):
                e[k] = round(e.get(k) or 0.0, 2)
            out.append(e)
        self._prev_files = {'ts': now, 'files': {(r['db_name'], r['file_name']):
                          {'r': r['num_of_reads'], 'w': r['num_of_writes'],
                           'sr': r['io_stall_read_ms'], 'sw': r['io_stall_write_ms'],
                           'mr': r['mb_read'], 'mw': r['mb_written']} for r in rows}}
        return out


def _short_err(e: Exception) -> str:
    text = str(e)
    if 'VIEW SERVER STATE' in text.upper() or 'was denied' in text.lower():
        return "нет прав VIEW SERVER STATE (данные неполные)"
    if 'Invalid object name' in text:
        return "объект не поддерживается этой версией SQL Server"
    return text[:200]


# ===========================================================================
class Store:
    """Хранилище истории мониторинга (SQLite), потокобезопасное."""

    _SCHEMA = """
    CREATE TABLE IF NOT EXISTS samples(
        id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT,
        sql_cpu REAL, other_cpu REAL, batch_sec REAL, trans_sec REAL,
        compiles_sec REAL, recompiles_sec REAL, reads_sec REAL, writes_sec REAL,
        log_flush_sec REAL, lock_waits_sec REAL, full_scans_sec REAL,
        errors_sec REAL, ple REAL, connections INTEGER, blocked INTEGER,
        grants_pending REAL);
    CREATE INDEX IF NOT EXISTS idx_samples_ts ON samples(ts);
    CREATE TABLE IF NOT EXISTS waits(ts TEXT, wait_type TEXT, wait_ms REAL, signal_ms REAL);
    CREATE INDEX IF NOT EXISTS idx_waits_ts ON waits(ts);
    CREATE TABLE IF NOT EXISTS blocking(ts TEXT, head_session INTEGER, head_query TEXT,
        victims INTEGER, max_wait_ms REAL, head_login TEXT, head_program TEXT);
    CREATE TABLE IF NOT EXISTS slow_queries(ts TEXT, elapsed_ms REAL, cpu_ms REAL,
        db_name TEXT, text TEXT, hash TEXT);
    CREATE INDEX IF NOT EXISTS idx_slow_ts ON slow_queries(ts);
    CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT);
    """

    def __init__(self, path: str):
        self.path = path
        self.lock = threading.Lock()
        self.cn = sqlite3.connect(path, check_same_thread=False)
        self.cn.row_factory = sqlite3.Row
        with self.lock:
            self.cn.executescript(self._SCHEMA)
            self.cn.commit()

    # ------------------------------------------------------------- запись
    def save_sample(self, smp: dict) -> None:
        ts = smp['ts']
        ts_str = ts.strftime('%Y-%m-%d %H:%M:%S') if hasattr(ts, 'strftime') else str(ts)
        p = smp.get('points') or {}
        r = smp.get('rates') or {}
        c = smp.get('cpu') or {}
        with self.lock:
            self.cn.execute(
                "INSERT INTO samples(ts, sql_cpu, other_cpu, batch_sec, trans_sec, compiles_sec,"
                " recompiles_sec, reads_sec, writes_sec, log_flush_sec, lock_waits_sec, full_scans_sec,"
                " errors_sec, ple, connections, blocked, grants_pending)"
                " VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                (ts_str, c.get('sql'), c.get('other'), r.get('batch_sec'), r.get('trans_sec'),
                 r.get('compilations_sec'), r.get('recompilations_sec'), r.get('page_reads_sec'),
                 r.get('page_writes_sec'), r.get('log_flushes_sec'), r.get('lock_waits_sec'),
                 r.get('full_scans_sec'), r.get('errors_sec'), p.get('ple'), p.get('connections'),
                 p.get('blocked'), p.get('grants_pending')))
            for w in smp.get('waits_delta') or []:
                if w.get('wait_ms', 0) > 0.5:
                    self.cn.execute("INSERT INTO waits(ts, wait_type, wait_ms, signal_ms) VALUES (?,?,?,?)",
                                    (ts_str, w['wait_type'], w['wait_ms'], w.get('signal_ms', 0)))
            blk = smp.get('blocking') or {}
            for ch in blk.get('chains') or []:
                self.cn.execute(
                    "INSERT INTO blocking(ts, head_session, head_query, victims, max_wait_ms,"
                    " head_login, head_program) VALUES (?,?,?,?,?,?,?)",
                    (ts_str, ch.get('head'), ch.get('head_query'), ch.get('victims'),
                     ch.get('max_wait_ms'), ch.get('head_login'), ch.get('head_program')))
            for q in smp.get('slow') or []:
                h = hashlib.md5((q.get('text') or '')[:300].encode('utf-8', 'ignore')).hexdigest()
                dup = self.cn.execute(
                    "SELECT 1 FROM slow_queries WHERE hash=? AND ts>=? LIMIT 1",
                    (h, (ts - timedelta(minutes=10)).strftime('%Y-%m-%d %H:%M:%S')
                     if hasattr(ts, 'strftime') else ts_str)).fetchone()
                if not dup:
                    self.cn.execute(
                        "INSERT INTO slow_queries(ts, elapsed_ms, cpu_ms, db_name, text, hash)"
                        " VALUES (?,?,?,?,?,?)",
                        (ts_str, q.get('elapsed_ms'), q.get('cpu_ms'), q.get('db_name'),
                         (q.get('text') or '')[:1500], h))
            self.cn.commit()

    # ------------------------------------------------------------- чтение
    @staticmethod
    def _dt(v):
        try:
            return datetime.strptime(v[:19], '%Y-%m-%d %H:%M:%S')
        except Exception:
            return None

    def history(self, hours: float = 24) -> dict:
        cutoff = (datetime.now() - timedelta(hours=hours)).strftime('%Y-%m-%d %H:%M:%S')
        with self.lock:
            samples = []
            for row in self.cn.execute(
                    "SELECT * FROM samples WHERE ts>=? ORDER BY ts", (cutoff,)):
                d = dict(row)
                d['ts'] = self._dt(d['ts'])
                samples.append(d)
            waits = [dict(ts=self._dt(r[0]), wait_type=r[1], wait_ms=r[2])
                     for r in self.cn.execute(
                         "SELECT ts, wait_type, wait_ms FROM waits WHERE ts>=? ORDER BY ts", (cutoff,))]
            blocking = []
            for row in self.cn.execute("SELECT * FROM blocking WHERE ts>=? ORDER BY ts DESC LIMIT 200", (cutoff,)):
                d = dict(row)
                d['ts'] = self._dt(d['ts'])
                blocking.append(d)
            slow = []
            for row in self.cn.execute(
                    "SELECT ts, elapsed_ms, cpu_ms, db_name, text FROM slow_queries"
                    " WHERE ts>=? ORDER BY elapsed_ms DESC LIMIT 100", (cutoff,)):
                d = dict(row)
                d['ts'] = self._dt(d['ts'])
                slow.append(d)
        return {'samples': samples, 'waits': waits, 'blocking': blocking,
                'slow': slow, 'hours': hours}

    def sample_count(self) -> int:
        with self.lock:
            return self.cn.execute("SELECT COUNT(*) FROM samples").fetchone()[0]

    # ------------------------------------------------------------- служебное
    def get_meta(self, key: str, default: str = "") -> str:
        with self.lock:
            row = self.cn.execute("SELECT v FROM meta WHERE k=?", (key,)).fetchone()
            return row[0] if row else default

    def set_meta(self, key: str, value: str) -> None:
        with self.lock:
            self.cn.execute("INSERT OR REPLACE INTO meta(k, v) VALUES (?,?)", (key, value))
            self.cn.commit()

    def purge(self, days: int) -> None:
        cutoff = (datetime.now() - timedelta(days=days)).strftime('%Y-%m-%d %H:%M:%S')
        with self.lock:
            for t in ('samples', 'waits', 'blocking', 'slow_queries'):
                self.cn.execute("DELETE FROM %s WHERE ts<?" % t, (cutoff,))
            self.cn.commit()

    def close(self):
        with self.lock:
            try:
                self.cn.close()
            except Exception:
                pass
