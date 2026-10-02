# -*- coding: utf-8 -*-
"""Анализ собранных данных: поиск проблем, причины замедлений, рекомендации.

Основано на общепринятой методологии анализа ожиданий (waits) и лучших
практиках настройки MS SQL Server.
"""
import math
from dataclasses import dataclass, field
from typing import List, Dict, Optional

# =============================================================================
#  Ожидания: игнорируемые типы и категории
# =============================================================================
WAIT_IGNORE = {
    'SLEEP_TASK', 'SLEEP_BPOOL_FLUSH', 'SLEEP_DBSTARTUP', 'SLEEP_DCOMSTARTUP',
    'SLEEP_MASTERDBREADY', 'SLEEP_MASTERMDREADY', 'SLEEP_MASTERUPGRADED',
    'SLEEP_MSDBSTARTUP', 'SLEEP_SYSTEMTASK', 'SLEEP_TEMPDBSTARTUP',
    'SLEEP_WORKSPACEALLOCATE', 'BROKER_EVENTHANDLER', 'BROKER_RECEIVE_WAITFOR',
    'BROKER_TASK_STOP', 'BROKER_TO_FLUSH', 'BROKER_TRANSMITTER', 'CHECKPOINT_QUEUE',
    'CHKPT', 'DIRTY_PAGE_POLL', 'DISPATCHER_QUEUE_SLEEP', 'EXECSYNC',
    'KSOURCE_WAKEUP', 'LAZYWRITER_SLEEP', 'LOGMGR_QUEUE', 'MISCELLANEOUS',
    'ONDEMAND_TASK_QUEUE', 'REQUEST_FOR_DEADLOCK_SEARCH', 'DEADLOCK_ENUM_MUTEX',
    'RESOURCE_QUEUE', 'SERVER_IDLE_CHECK', 'SOSHOST_SLEEP', 'SQLTRACE_BUFFER_FLUSH',
    'SQLTRACE_INCREMENTAL_FLUSH_SLEEP', 'SQLTRACE_WAIT_ENTRIES', 'TRACEWRITE',
    'UCS_SESSION_REGISTRATION', 'WAIT_FOR_RESULTS', 'WAITFOR', 'WAITFOR_TASKSHUTDOWN',
    'WAIT_XTP_OFFLINE_CKPT_NEW_LOG', 'WAIT_XTP_CKPT_CLOSE', 'XE_DISPATCHER_JOIN',
    'XE_DISPATCHER_WAIT', 'XE_LIVE_TARGET_TVF', 'XE_TIMER_EVENT',
    'XE_FILE_TARGET_TVF', 'CLR_AUTO_EVENT', 'CLR_MANUAL_EVENT',
    'INTERNAL', 'BROKER_RECEIVE_WAITFOR', 'SOS_TASK_DEMOTION', 'SOS_WORKER',
}
WAIT_IGNORE_PREFIXES = (
    'BROKER_', 'HADR_', 'QDS_', 'XE_', 'SQLTRACE_', 'DIRTY_PAGE', 'SLEEP_',
    'PREEMPTIVE_XE_', 'PARALLEL_REDO_', 'DATABASE_MIRRORING', 'TRAN_MARKLATCH',
    'DTC_', 'CLR_', 'FT_', 'DPTMP_', 'MIRROR_', 'LOW_PRIORITY',
)


def is_ignorable_wait(wt: str) -> bool:
    return wt in WAIT_IGNORE or wt.startswith(WAIT_IGNORE_PREFIXES)


WAIT_CATS = {
    'cpu': dict(title='ЦП и параллелизм', color='#dc2626',
                why='Запросы ждут освобождения планировщиков ЦП либо потоки параллельного плана '
                    'обмениваются данными (CXPACKET). Высокая доля таких ожиданий говорит о нехватке '
                    'процессорных ресурсов или неверной настройке параллелизма.',
                fixes=["Настройте MAXDOP (для OLTP обычно ≤ 8) и 'cost threshold for parallelism' (25–50).",
                       "Оптимизируйте самые дорогие по ЦП запросы (раздел «Топ запросов»).",
                       "Проверьте частоту компиляций (Compilations/sec > 10% от Batch Requests/sec)."]),
    'memory': dict(title='Память (гранты запросов)', color='#9333ea',
                   why='Запросы ждут предоставления памяти (memory grant) под сортировки и хеширования. '
                       'Частая причина — запросы с завышенными оценками кардинальности или нехватка памяти.',
                   fixes=["Оптимизируйте запросы с большими сортировками/хешированиями.",
                          "Проверьте актуальность статистики (устаревшая статистика → завышенные гранты).",
                          "Проверьте настройки 'max server memory' и объём ОЗУ сервера."]),
    'data_io': dict(title='Чтение страниц с диска', color='#2563eb',
                    why='Данные не находятся в буферном кэше — SQL Server читает страницы с диска '
                        '(PAGEIOLATCH_*). Причины: нехватка памяти, медленные диски, неоптимальные '
                        'планы запросов (сканирования таблиц).',
                    fixes=["Проверьте Page Life Expectancy и объём буферного кэша (раздел «Память»).",
                           "Проверьте задержки дисков (раздел «Диски и файлы»).",
                           "Создайте недостающие индексы, устраните сканирования больших таблиц."]),
    'log_io': dict(title='Запись журнала транзакций', color='#0891b2',
                   why='Медленная запись журнала транзакций на диск (WRITELOG). Журнал должен лежать '
                       'на быстром диске (SSD/NVMe), запись — быть последовательной.',
                   fixes=["Разместите файлы журнала (.ldf) на отдельном быстром диске.",
                           "Проверьте задержки записи журнала в разделе «Диски и файлы».",
                           "Сократите объём длинных транзакций, избегайте частых мелких autogrowth."]),
    'locks': dict(title='Блокировки и цепочки блокирования', color='#d97706',
                  why='Транзакции удерживают блокировки и мешают друг другу (LCK_M_*). Обычно причина — '
                      'длинные или незавершённые транзакции и неоптимальные планы (сканирования).',
                  fixes=["Найдите головные блокировки (head blockers) в разделе «Блокировки».",
                         "Рассмотрите READ_COMMITTED_SNAPSHOT для OLTP-баз.",
                         "Проверьте приложение: короткие транзакции, одинаковый порядок доступа к таблицам.",
                         "Оптимизируйте запросы — меньше сканирований, меньше блокировок."]),
    'network': dict(title='Сеть и клиентские приложения', color='#65a30d',
                    why='SQL Server отправил результат клиенту и ждёт, пока приложение заберёт данные '
                        '(ASYNC_NETWORK_IO). Узкое место — на стороне приложения или сети.',
                    fixes=["Проверьте, не обрабатывает ли приложение результат построчно (без буферизации).",
                           "Уменьшайте объём выбираемых данных (TOP, WHERE, пагинация).",
                           "Проверьте сеть между сервером приложений и SQL Server."]),
    'backup': dict(title='Резервное копирование', color='#64748b',
                   why='Ожидания, связанные с резервным копированием (BACKUPIO/BACKUPBUFFER). '
                       'Влияет, если копирование идёт в рабочие часы.',
                   fixes=["Перенесите резервное копирование на ночное время.",
                           "Включите сжатие резервных копий (backup compression default).",
                           "Проверьте скорость диска, на который пишутся копии."]),
    'preemptive': dict(title='Внешние вызовы (XP, linked servers)', color='#78716c',
                       why='SQL Server ожидает внешних вызовов ОС/внешних процедур (PREEMPTIVE_*): '
                           'linked servers, xp_cmdshell, полнотекстовый поиск и т.п.',
                       fixes=["Проверьте производительность связанных серверов и внешних процедур.",
                              "По возможности заменяйте linked server запросы на локальные."]),
    'other': dict(title='Прочие ожидания', color='#94a3b8', why='Суммарные прочие ожидания.',
                  fixes=["Изучите конкретные типы ожиданий в таблице ниже."]),
}

_CAT_TYPES = {
    'cpu': {'CXPACKET', 'CXCONSUMER', 'SOS_SCHEDULER_YIELD', 'THREADPOOL', 'CPU'},
    'memory': {'RESOURCE_SEMAPHORE', 'RESOURCE_SEMAPHORE_QUERY_COMPILE', 'RESOURCE_SEMAPHORE_MUTEX',
               'CMEMTHREAD', 'MEMORY_ALLOCATION_EXT', 'RESERVED_MEMORY_ALLOCATION_EXT',
               'MEMORY_GRANT_INSERT'},
    'data_io': set(), 'log_io': {'WRITELOG', 'LOGBUFFER', 'LOGMGR_FLUSH', 'LOG_RATE_GOVERNOR',
                                 'PREEMPTIVE_LOGFLUSH'},
    'locks': set(), 'network': {'ASYNC_NETWORK_IO'}, 'backup': set(), 'preemptive': set(),
}
_CAT_PREFIX = {'data_io': ('PAGEIOLATCH_', 'IO_COMPLETION', 'ASYNC_IO_COMPLETION', 'DISKIO'),
               'locks': ('LCK_M_',), 'backup': ('BACKUP',), 'preemptive': ('PREEMPTIVE_',)}


def categorize_wait(wt: str) -> str:
    for cat, types in _CAT_TYPES.items():
        if wt in types:
            return cat
    for cat, prefixes in _CAT_PREFIX.items():
        if wt.startswith(prefixes):
            return cat
    return 'other'


# =============================================================================
#  Структуры результата
# =============================================================================
@dataclass
class Finding:
    severity: str            # critical | warning | info | ok
    area: str                # Конфигурация, Память, Базы данных, ...
    title: str
    problem: str = ''
    recommendation: str = ''
    script: str = ''


@dataclass
class Cause:
    severity: str
    title: str
    evidence: List[str] = field(default_factory=list)
    explanation: str = ''
    fixes: List[str] = field(default_factory=list)


@dataclass
class Analysis:
    findings: List[Finding] = field(default_factory=list)
    causes: List[Cause] = field(default_factory=list)
    health_score: int = 100
    summary: str = ''
    wait_by_cat: List[dict] = field(default_factory=list)
    wait_top: List[dict] = field(default_factory=list)
    config_audit: List[dict] = field(default_factory=list)
    stats: dict = field(default_factory=dict)

    @property
    def counts(self):
        return {s: sum(1 for f in self.findings if f.severity == s)
                for s in ('critical', 'warning', 'info')}


SEV_RU = {'critical': 'Критично', 'warning': 'Важно', 'info': 'Рекомендуется', 'ok': 'Норма'}
SEV_WEIGHT = {'critical': 12, 'warning': 5, 'info': 1.5, 'ok': 0}


# =============================================================================
#  Статистика по истории
# =============================================================================
def _stats(history: Optional[dict]) -> dict:
    st = dict(n=0, cpu_avg=None, cpu_max=0.0, batch_avg=0.0, batch_max=0.0,
              ple_avg=None, ple_min=None, blocked_max=0, blocked_events=0,
              grants_max=0, compile_ratio_avg=None, errors_avg=0.0,
              reads_avg=0.0, writes_avg=0.0, waits_by_type={}, cat_ms={}, cat_pct={},
              waits_top=[], hours=0.0)
    if not history:
        return st
    samples = history.get('samples') or []
    st['n'] = len(samples)
    st['hours'] = history.get('hours') or 0
    if samples:
        cpus = [s['sql_cpu'] or 0 for s in samples if s.get('sql_cpu') is not None]
        batches = [s['batch_sec'] or 0 for s in samples if s.get('batch_sec') is not None]
        ples = [s['ple'] or 0 for s in samples if s.get('ple') is not None]
        blocked = [s['blocked'] or 0 for s in samples]
        grants = [s['grants_pending'] or 0 for s in samples]
        compiles = [s['compiles_sec'] or 0 for s in samples if s.get('compiles_sec') is not None]
        errors = [s['errors_sec'] or 0 for s in samples]
        if cpus:
            st['cpu_avg'] = sum(cpus) / len(cpus)
            st['cpu_max'] = max(cpus)
        if batches:
            st['batch_avg'] = sum(batches) / len(batches)
            st['batch_max'] = max(batches)
        if ples:
            st['ple_avg'] = sum(ples) / len(ples)
            st['ple_min'] = min(ples)
        st['blocked_max'] = max(blocked) if blocked else 0
        st['blocked_events'] = sum(1 for b in blocked if b > 0)
        st['grants_max'] = max(grants) if grants else 0
        if compiles and batches and len(compiles) == len(batches):
            ratios = [c / b for c, b in zip(compiles, batches) if b > 0]
            if ratios:
                st['compile_ratio_avg'] = sum(ratios) / len(ratios)
        st['errors_avg'] = sum(errors) / len(errors) if errors else 0
        st['reads_avg'] = sum(s.get('reads_sec') or 0 for s in samples) / len(samples)
        st['writes_avg'] = sum(s.get('writes_sec') or 0 for s in samples) / len(samples)
    by_type: Dict[str, float] = {}
    for w in history.get('waits') or []:
        if w.get('wait_ms') and not is_ignorable_wait(w['wait_type']):
            by_type[w['wait_type']] = by_type.get(w['wait_type'], 0) + w['wait_ms']
    st['waits_by_type'] = by_type
    for wt, ms in by_type.items():
        cat = categorize_wait(wt)
        st['cat_ms'][cat] = st['cat_ms'].get(cat, 0) + ms
    total_ms = sum(st['cat_ms'].values())
    if total_ms > 0:
        st['cat_pct'] = {k: v / total_ms * 100 for k, v in st['cat_ms'].items()}
    st['waits_top'] = sorted(by_type.items(), key=lambda kv: kv[1], reverse=True)[:15]
    return st


# =============================================================================
#  Главная функция анализа
# =============================================================================
def analyze(snapshot: dict, history: Optional[dict] = None) -> Analysis:
    an = Analysis()
    findings = an.findings
    st = _stats(history)
    an.stats = st
    cfg = {k: v for k, v in (snapshot.get('config') or {}).items()}
    sysinfo = snapshot.get('sys') or {}
    databases = snapshot.get('databases') or []

    def cfg_int(name, default=None):
        try:
            return int(float(cfg.get(name)))
        except (TypeError, ValueError):
            return default

    def add(sev, area, title, problem='', rec='', script=''):
        findings.append(Finding(sev, area, title, problem, rec, script))

    # ---------------------------------------------------------------- права
    for e in snapshot.get('errors') or []:
        if 'VIEW SERVER STATE' in e.upper():
            add('warning', 'Права доступа', 'Недостаточно прав для полного мониторинга',
                'Учётной записи запрещён доступ к DMV: %s' % e,
                'Предоставьте учётной записи право VIEW SERVER STATE и роль msdb db_datareader '
                'для полного снимка состояния.',
                "USE master;\nGRANT VIEW SERVER STATE TO [домен\\пользователь];\n"
                "USE msdb;\nEXEC sp_addrolemember 'db_datareader', 'домен\\пользователь';")
            break

    # --------------------------------------------------------- конфигурация
    ram_mb = (sysinfo.get('physical_memory_kb') or 0) / 1024.0
    maxmem = cfg_int('max server memory (MB)')
    audit = []
    if maxmem is not None and ram_mb > 0:
        rec_mem = max(2048, int((ram_mb - max(4096, ram_mb * 0.15)) / 64) * 64)
        if maxmem >= 2147483647:
            if ram_mb >= 8192:
                add('warning', 'Конфигурация', 'Память сервера не ограничена (max server memory по умолчанию)',
                    'SQL Server может занять всю память ОС, оставив системе недостаточно ресурсов — '
                    'это приводит к вытеснению в файл подкачки и деградации всей машины.',
                    "Ограничьте 'max server memory' значением ≈ ОЗУ минус 4 ГБ (или минус 10–20%%). "
                    "Для этого сервера (ОЗУ %s ГБ) рекомендуется ≈ %s МБ." % (_fm_gb(ram_mb), f"{rec_mem:,}".replace(',', ' ')),
                    "EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"
                    "EXEC sys.sp_configure 'max server memory (MB)', %d;\nRECONFIGURE;" % rec_mem)
            audit.append(dict(param='max server memory', current='не ограничено',
                              rec='≈ %s МБ' % f"{rec_mem:,}".replace(',', ' '), status='warn' if ram_mb >= 8192 else 'ok'))
        elif maxmem > ram_mb:
            add('critical', 'Конфигурация', 'max server memory больше физической памяти',
                "Значение (%s МБ) превышает ОЗУ сервера (%s) — риск нехватки памяти на уровне ОС."
                % (f"{maxmem:,}".replace(',', ' '), _fm_gb(ram_mb)),
                'Установите значение ниже объёма ОЗУ.',
                "EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"
                "EXEC sys.sp_configure 'max server memory (MB)', %d;\nRECONFIGURE;" % rec_mem)
            audit.append(dict(param='max server memory', current=f"{maxmem:,} МБ", rec='≈ %s МБ' % rec_mem, status='bad'))
        else:
            audit.append(dict(param='max server memory', current=f"{maxmem:,} МБ",
                              rec='≈ %s МБ' % f"{rec_mem:,}".replace(',', ' '), status='ok'))

    cpu_count = sysinfo.get('cpu_count') or 0
    maxdop = cfg_int('max degree of parallelism')
    if maxdop is not None:
        if maxdop == 0 and cpu_count >= 8:
            add('warning', 'Конфигурация', 'MAXDOP не ограничен (0) при %d логических ядрах' % cpu_count,
                'Параллельные планы могут занять все ядра, вызывая волны деградации (типично для CXPACKET).',
                'Для смешанной/OLTP нагрузки установите MAXDOP = 8 или число ядер одного NUMA-узла.',
                "EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"
                "EXEC sys.sp_configure 'max degree of parallelism', 8;\nRECONFIGURE;")
            audit.append(dict(param='max degree of parallelism', current='0 (все ядра)', rec='8', status='warn'))
        elif maxdop > 8:
            add('info', 'Конфигурация', 'MAXDOP = %d — слишком высокое значение' % maxdop,
                'Для типичной OLTP-нагрузки рекомендуется MAXDOP ≤ 8.',
                'Снизьте значение до 8.', None)
            audit.append(dict(param='max degree of parallelism', current=str(maxdop), rec='≤ 8', status='warn'))
        else:
            audit.append(dict(param='max degree of parallelism', current=str(maxdop), rec='≤ 8', status='ok'))

    ct = cfg_int('cost threshold for parallelism')
    if ct is not None:
        if ct <= 5:
            add('warning', 'Конфигурация', 'Порог параллелизма оставлен по умолчанию (%d)' % ct,
                'Мелкие запросы уходят в параллельные планы — рост CXPACKET и накладных расходов.',
                "Увеличьте 'cost threshold for parallelism' до 25–50.",
                "EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"
                "EXEC sys.sp_configure 'cost threshold for parallelism', 50;\nRECONFIGURE;")
            audit.append(dict(param='cost threshold for parallelism', current=str(ct), rec='25–50', status='warn'))
        else:
            audit.append(dict(param='cost threshold for parallelism', current=str(ct), rec='25–50', status='ok'))

    plans = {p['objtype']: p for p in (snapshot.get('cached_plans') or [])}
    adhoc = plans.get('Adhoc') or {}
    total_mb = sum((p.get('size_mb') or 0) for p in plans.values())
    adhoc_share = (adhoc.get('size_mb') or 0) / total_mb * 100 if total_mb else 0
    if cfg_int('optimize for ad hoc workloads') == 0 and adhoc_share > 25 and adhoc.get('plan_count', 0) > 5000:
        add('warning', 'Конфигурация', 'Кэш планов засорён ad-hoc запросами',
            'Ad-hoc планы занимают %.0f%% кэша (%s МБ, %s планов) — память тратится впустую.'
            % (adhoc_share, _fnum(adhoc.get('size_mb')), _fnum(adhoc.get('plan_count'))),
            "Включите 'optimize for ad hoc workloads' — при первом выполнении будет компилироваться "
            "только заглушка плана.",
            "EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"
            "EXEC sys.sp_configure 'optimize for ad hoc workloads', 1;\nRECONFIGURE;")
    audit.append(dict(param='optimize for ad hoc workloads',
                      current=cfg.get('optimize for ad hoc workloads', '—'),
                      rec='1', status='ok' if cfg_int('optimize for ad hoc workloads') == 1 else
                      ('warn' if adhoc_share > 25 else 'ok')))

    if cfg_int('priority boost') == 1:
        add('warning', 'Конфигурация', 'Включён priority boost',
            'Повышение приоритета потока SQL Server устарело и может дестабилизировать сервер.',
            "Отключите 'priority boost'.",
            "EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"
            "EXEC sys.sp_configure 'priority boost', 0;\nRECONFIGURE;")
    if cfg_int('lightweight pooling') == 1:
        add('warning', 'Конфигурация', 'Включён lightweight pooling (fibers)',
            'Режим волокон ухудшает работу и не решает проблему ЦП.',
            "Отключите 'lightweight pooling'.",
            "EXEC sys.sp_configure 'show advanced options', 1; RECONFIGURE;\n"
            "EXEC sys.sp_configure 'lightweight pooling', 0;\nRECONFIGURE;")

    mem_model = sysinfo.get('sql_memory_model_desc') or ''
    if ram_mb >= 61440 and 'LOCK_PAGES' not in mem_model.upper():
        add('info', 'Конфигурация', 'Lock Pages in Memory не включён',
            'На сервере %s ГБ ОЗУ рекомендуется разрешить SQL Server фиксировать страницы в памяти '
            '(защита от вытеснения ОС).' % _fm_gb(ram_mb),
            'Выдайте учётной записи служб SQL Server право «Lock pages in memory» (групповая политика) '
            'и перезапустите службу.')

    # ------------------------------------------------------- tempdb
    tempdb_files = snapshot.get('tempdb_files')
    if tempdb_files and cpu_count and tempdb_files < min(8, cpu_count) and cpu_count >= 4:
        need = min(8, cpu_count)
        add('warning', 'Конфигурация', 'tempdb: всего %d файл(а) данных' % tempdb_files,
            'При %d ядрах один файл tempdb создаёт конкуренцию за страницы GAM/SGAM (PFS/GAM-конфликты).'
            % cpu_count,
            'Создайте %d файлов данных одинакового размера с одинаковым autogrowth.' % need,
            "ALTER DATABASE tempdb MODIFY FILE (NAME = N'tempdev', SIZE = 8GB);\n" +
            "\n".join("ALTER DATABASE tempdb ADD FILE (NAME = N'temp%d', FILENAME = "
                      "N'D:\\MSSQL\\DATA\\tempdb_mssql_%d.ndf', SIZE = 8GB, FILEGROWTH = 512MB);"
                      % (i, i) for i in range(2, need + 1)))
        audit.append(dict(param='Файлы данных tempdb', current=str(tempdb_files),
                          rec=str(need) + ' (по числу ядер, до 8)', status='warn'))
    elif tempdb_files:
        audit.append(dict(param='Файлы данных tempdb', current=str(tempdb_files),
                          rec='min(8, ядра)', status='ok'))

    # ------------------------------------------------------- базы данных
    full_no_log, no_full, auto_shrink, auto_close, bad_verify, low_compat = [], [], [], [], [], []
    offline = []
    for d in databases:
        name = d.get('name')
        if name in ('master', 'model', 'msdb', 'tempdb'):
            continue
        if (d.get('state_desc') or 'ONLINE') != 'ONLINE':
            offline.append(name)
            continue
        if d.get('is_auto_shrink_on'):
            auto_shrink.append(name)
        if d.get('is_auto_close_on'):
            auto_close.append(name)
        if (d.get('page_verify_option_desc') or '').upper() != 'CHECKSUM':
            bad_verify.append(name)
        if (d.get('compatibility_level') or 150) <= 110:
            low_compat.append((name, d.get('compatibility_level')))
    backups = {b['db_name']: b for b in (snapshot.get('backups') or [])}
    from datetime import datetime, timedelta
    now = datetime.now()
    for d in databases:
        name = d.get('name')
        if name in ('master', 'model', 'msdb', 'tempdb') or (d.get('state_desc') or '') != 'ONLINE':
            continue
        b = backups.get(name)
        if (d.get('recovery_model_desc') == 'FULL'):
            ll = b.get('last_log') if b else None
            if not ll or (now - ll) > timedelta(hours=24):
                full_no_log.append(name)
        lf = b.get('last_full') if b else None
        if not lf or (now - lf) > timedelta(days=7):
            no_full.append(name)

    if offline:
        add('critical', 'Базы данных', 'Базы не в сети: %s' % ', '.join(offline),
            'База данных в состоянии не ONLINE — недоступна приложениям.',
            'Проверьте причину (журналы SQL Server, состояние файлов, ALTER DATABASE).')
    if auto_shrink:
        add('critical', 'Базы данных', 'Включён AUTO_SHRINK: %s' % ', '.join(auto_shrink),
            'Автосжатие файлов вызывает постоянное уменьшение/рост файлов, фрагментацию и нагрузку на ЦП — '
            'одна из самых вредных настроек.',
            'Отключите AUTO_SHRINK и управляйте размером файлов вручную.',
            '\n'.join("ALTER DATABASE [%s] SET AUTO_SHRINK OFF;" % n for n in auto_shrink))
    if auto_close:
        add('warning', 'Базы данных', 'Включён AUTO_CLOSE: %s' % ', '.join(auto_close),
            'База закрывается после отключения последнего пользователя: каждый новый коннект платит '
            'за открытие файлов и очистку кэша процедур.',
            'Отключите AUTO_CLOSE.',
            '\n'.join("ALTER DATABASE [%s] SET AUTO_CLOSE OFF;" % n for n in auto_close))
    if bad_verify:
        add('warning', 'Базы данных', 'CHECKSUM отключён: %s' % ', '.join(bad_verify),
            'Без CHECKSUM повреждения страниц могут оставаться незамеченными до резервного копирования '
            '(когда уже поздно).',
            'Включите проверку страниц и пересоздайте бэкапы.',
            '\n'.join("ALTER DATABASE [%s] SET PAGE_VERIFY CHECKSUM;" % n for n in bad_verify))
    if low_compat:
        add('info', 'Базы данных', 'Устаревший уровень совместимости',
            'Базы: %s.' % ', '.join('%s (compat %s)' % x for x in low_compat),
            'Новый уровень совместимости включает улучшенный оптимизатор (новый cardinality estimator). '
            'Проверьте на тестовой среде и повысьте.',
            '\n'.join("ALTER DATABASE [%s] SET COMPATIBILITY_LEVEL = 150;" % n for n, _ in low_compat))
    if no_full:
        add('critical', 'Резервное копирование',
            'Нет полной резервной копии за последние 7 дней: %s' % ', '.join(no_full),
            'Восстановление после сбоя будет невозможно. Проверьте задания резервного копирования.',
            'Настройте регулярное резервное копирование (полное — еженедельно/ежедневно, разностное — '
            'ежедневно, журнал — каждые 15–30 минут для FULL-модели).',
            '\n'.join("BACKUP DATABASE [%s] TO DISK = N'D:\\Backup\\%s.bak' WITH COMPRESSION, INIT, CHECKSUM;"
                      % (n, n) for n in no_full))
    if full_no_log:
        add('critical', 'Резервное копирование',
            'FULL-модель без копий журнала: %s' % ', '.join(full_no_log),
            'Журнал транзакций растёт бесконечно (log_reuse_wait = LOG_BACKUP). Возможны переполнение '
            'диска и потеря данных на точку отказа.',
            'Настройте регулярное резервное копирование журнала транзакций.',
            '\n'.join("BACKUP LOG [%s] TO DISK = N'D:\\Backup\\%s_log.trn' WITH COMPRESSION, INIT;"
                      % (n, n) for n in full_no_log))

    # autogrowth
    bad_growth = []
    for f in snapshot.get('file_cfg') or []:
        if f['db_name'] in ('master', 'model', 'msdb', 'tempdb'):
            continue
        if f.get('is_percent_growth') and (f.get('size_mb') or 0) > 8192:
            bad_growth.append('%s!%s: рост %d%%' % (f['db_name'], f['file_name'], f.get('growth') or 0))
        elif not f.get('is_percent_growth') and f.get('type_desc') == 'ROWS' and (f.get('growth') or 0) * 8 < 65536 and (f.get('size_mb') or 0) > 4096:
            bad_growth.append('%s!%s: шаг %d МБ' % (f['db_name'], f['file_name'], (f.get('growth') or 0) * 8 // 1024))
    if bad_growth:
        add('warning', 'Базы данных', 'Неоптимальный autogrowth файлов',
            '; '.join(bad_growth[:8]),
            'Процентный рост на больших файлах приводит к длительным паузам и фрагментации; '
            'мелкий шаг (1–64 МБ) у больших файлов вызывает тысячи расширений. Задайте фиксированный '
            'шаг 256–512 МБ (данные) / 512–1024 МБ (журнал) и следите за свободным местом.')

    # VLF
    big_vlf = [(v['db_name'], v['vlf_count']) for v in (snapshot.get('vlf') or []) if v.get('vlf_count', 0) > 1000]
    if big_vlf:
        add('warning', 'Базы данных', 'Слишком много VLF в журнале транзакций',
            '; '.join('%s: %d VLF' % x for x in big_vlf),
            'Больше 1000 виртуальных файлов журнала замедляют восстановление и операции с журналом '
            '(crash recovery, log reader).',
            'Уменьшите журнал до минимума и заново увеличьте крупными порциями (ALTER DATABASE ... '
            'MODIFY FILE (SIZE=...)).')

    # ------------------------------------------------------- память / ЦП
    os_mem = snapshot.get('os_mem') or {}
    avail_kb = os_mem.get('available_physical_kb') or 0
    total_kb = os_mem.get('total_physical_kb') or 0
    if total_kb and avail_kb and avail_kb / total_kb < 0.05:
        add('warning', 'Память', 'Свободная память ОС на грани (%.1f ГБ из %s)'
            % (avail_kb / 1048576, _fm_gb(total_kb / 1024)),
            'Системе не хватает памяти: возможно вытеснение в pagefile и общая деградация. '
            'Проверьте настройки max server memory и сторонние процессы.',
            'Уменьшите max server memory или добавьте ОЗУ.')

    if st['ple_min'] is not None and st['ple_min'] < 300 and (st['n'] or 0) > 10:
        add('warning', 'Память', 'Низкое время жизни страниц в кэше (PLE)',
            'Минимальный PLE за период: %d сек (норма — сотни и тысячи). SQL Server сбрасывает '
            'страницы из буферного кэша из-за нехватки памяти.' % st['ple_min'],
            'Увеличьте память сервера/max server memory, проверьте запросы с большим числом чтений.')

    points = snapshot.get('points') or {}
    if st['grants_max'] and st['grants_max'] > 0 and (st['n'] or 0) > 10:
        add('warning', 'Память', 'Запросы стоят в очереди за памятью (Memory Grants Pending)',
            'Максимум ожидающих грантов за период: %d. Запросам не хватает памяти под сортировки/хеши.'
            % st['grants_max'],
            'Оптимизируйте тяжёлые запросы, проверьте статистику; при необходимости увеличьте память.')

    if st['compile_ratio_avg'] is not None and st['compile_ratio_avg'] > 0.15 and (st['batch_avg'] or 0) > 20:
        add('warning', 'Производительность', 'Высокая доля компиляций',
            'SQL Compilations/sec составляют %.0f%% от Batch Requests/sec (норма < 10–15%%). '
            'Процессор тратится на компиляцию планов.' % (st['compile_ratio_avg'] * 100),
            'Включите «optimize for ad hoc workloads», проверьте параметризацию запросов в приложении '
            'и однотипные ad-hoc запросы (разные литералы).')

    if st['cpu_avg'] is not None:
        if st['cpu_avg'] > 80:
            add('warning', 'Процессор', 'Высокая средняя загрузка ЦП SQL Server (%.0f%%)' % st['cpu_avg'],
                'Процессор — текущее узкое место: средняя за период загрузка выше 80%%.',
                'Оптимизируйте самые дорогие по CPU запросы, проверьте параллелизм (MAXDOP) и компиляции.')
        elif st['cpu_max'] > 95:
            add('info', 'Процессор', 'Пиковые значения загрузки ЦП до %.0f%%' % st['cpu_max'],
                'Кратковременные пики — проверить природу нагрузки.',
                'Если пики регулярны, оптимизируйте запросы, создающие их.')

    if st['errors_avg'] > 1 and (st['n'] or 0) > 5:
        add('warning', 'Надёжность', 'Постоянные ошибки (Errors/sec ≈ %.1f)' % st['errors_avg'],
            'В счётчике ошибок устойчивое значение выше нуля.',
            'Проверьте журнал SQL Server (XE/ERRORLOG): возможны неудачные входы, ошибки ограничения '
            'или разрывы соединений.')

    # ------------------------------------------------------- ожидания
    an.wait_by_cat = sorted(
        [dict(key=k, title=WAIT_CATS[k]['title'], color=WAIT_CATS[k]['color'],
              ms=v, pct=st['cat_pct'].get(k, 0)) for k, v in st['cat_ms'].items()],
        key=lambda x: -x['ms'])
    an.wait_top = [dict(wait_type=w, ms=ms, pct=ms / max(1, sum(st['waits_by_type'].values())) * 100,
                        cat=categorize_wait(w)) for w, ms in st['waits_top']]

    if (st['cat_pct'].get('cpu') or 0) > 25 and st['cat_ms'].get('cpu', 0) > 60000:
        top = dict(st['waits_top']).get('CXPACKET', 0)
        if top:
            add('warning', 'Ожидания', 'Высокая доля ожиданий параллелизма (CXPACKET и др.) — %.0f%%'
                % st['cat_pct']['cpu'],
                'Запросы активно работают параллельно и конкурируют за планировщики.',
                'Настройте MAXDOP и cost threshold for parallelism; проверьте тяжёлые отчётные запросы.')
    if (st['cat_pct'].get('locks') or 0) > 10 and st['cat_ms'].get('locks', 0) > 30000:
        add('warning', 'Ожидания', 'Значимые ожидания блокировок — %.0f%%' % st['cat_pct']['locks'],
            'Транзакции ожидают снятия блокировок друг другом.',
            'См. раздел «Блокировки»: найдите head-blocker и длинные транзакции.')
    if (st['cat_pct'].get('network') or 0) > 20 and st['cat_ms'].get('network', 0) > 60000:
        add('warning', 'Ожидания', 'Высокая доля ASYNC_NETWORK_IO — %.0f%%' % st['cat_pct']['network'],
            'Сервер ждёт, пока приложения заберут результаты запросов.',
            'Оптимизируйте обработку результатов на стороне приложения (буферизация, меньшие выборки).')

    # ------------------------------------------------------- диски/файлы
    hot_files = []
    for f in snapshot.get('files') or []:
        lat = f.get('write_latency_ms') if f.get('type_desc') == 'LOG' else f.get('read_latency_ms')
        kind = 'запись журнала' if f.get('type_desc') == 'LOG' else 'чтение данных'
        limit = 15 if f.get('type_desc') == 'LOG' else 25
        if lat and lat > limit and (f.get('reads_sec', 0) + f.get('writes_sec', 0)) > 1:
            hot_files.append('%s!%s: %s %.0f мс' % (f['db_name'], f['file_name'], kind, lat))
    if hot_files:
        add('warning', 'Диски и файлы', 'Медленные файлы (задержки ввода-вывода)',
            '; '.join(hot_files[:8]),
            'Задержки выше нормы (журнал > 15 мс, данные > 25 мс). Разместите файлы на более быстрых '
            'дисках (SSD/NVMe), журнал — отдельно от данных и tempdb.')

    # ------------------------------------------------------- индексы/статистика
    mi_list = snapshot.get('missing_indexes') or []
    if mi_list:
        top = mi_list[0]
        add('warning', 'Индексы', 'Обнаружены недостающие индексы (%d)' % len(mi_list),
            'Самый значимый: %s — %d поисков, влияние %.0f%%.'
            % (top.get('table_ref'), top.get('user_seeks', 0) or 0, top.get('avg_user_impact') or 0),
            'Создайте индексы из раздела «Индексы и статистика» (проверив на тестовой среде и '
            'объединив с существующими).')

    frag = snapshot.get('frag') or []
    if frag:
        add('warning', 'Индексы', 'Фрагментированные индексы (%d)' % len(frag),
            'Худший: %s.%s — %.0f%% фрагментации (%s страниц).'
            % (frag[0].get('schema_name'), frag[0].get('table_name'),
               frag[0].get('avg_fragmentation_in_percent') or 0, _fnum(frag[0].get('page_count'))),
            'Выполните обслуживание: REORGANIZE при 10–30%%, REBUILD при > 30%% фрагментации.',
            '\n'.join('USE [%s];\nALTER INDEX [%s] ON [%s].[%s] REBUILD;' %
                      (f.get('db_name'), f.get('index_name'), f.get('schema_name'), f.get('table_name'))
                      for f in frag[:5]))

    stale = snapshot.get('stale_stats') or []
    if stale:
        add('warning', 'Индексы', 'Устаревшая статистика (%d объектов)' % len(stale),
            'Например: %s.%s — изменено %s из %s строк (%.0f%%), обновлялась %s.'
            % (stale[0].get('schema_name'), stale[0].get('table_name'),
               _fnum(stale[0].get('modification_counter')), _fnum(stale[0].get('rows')),
               100 * (stale[0].get('modification_counter') or 0) / max(1, stale[0].get('rows') or 1),
               (stale[0].get('last_updated') or '—')),
            'Обновите статистику — устаревшие оценки приводят к плохим планам и избыточным чтениям.',
            '\n'.join('USE [%s];\nUPDATE STATISTICS [%s].[%s] [%s];' %
                      (s.get('db_name'), s.get('schema_name'), s.get('table_name'), s.get('stats_name'))
                      for s in stale[:5]))

    # uptime
    uptime = sysinfo.get('uptime_sec')
    if uptime and uptime < 86400:
        add('info', 'Надёжность', 'Сервер перезапускался менее суток назад',
            'Накопительная статистика (DMV) могла обнулиться.',
            'Рекомендуется наблюдать за сервером не менее суток для достоверных выводов.')

    # ------------------------------------------------------- причины замедлений
    an.causes = _build_causes(st, snapshot, findings)
    # ------------------------------------------------------- health score
    cnts = {'critical': 0, 'warning': 0, 'info': 0}
    for f in findings:
        if f.severity in cnts:
            cnts[f.severity] += 1
    penalty = (SEV_WEIGHT['critical'] * min(3, cnts['critical'])
               + SEV_WEIGHT['warning'] * min(6, cnts['warning'])
               + SEV_WEIGHT['info'] * min(4, cnts['info']))
    if st['cpu_avg'] and st['cpu_avg'] > 90:
        penalty += 10
    an.health_score = int(max(5, min(100, round(100 - penalty))))
    cnt = an.counts
    an.summary = ("Критичных проблем: %d, предупреждений: %d, рекомендаций: %d. %s"
                  % (cnt['critical'], cnt['warning'], cnt['info'],
                     _verdict(an.health_score)))
    an.config_audit = audit
    an.findings.sort(key=lambda f: (SEV_WEIGHT.get(f.severity, 99) * -1, f.area))
    return an


def _verdict(score):
    if score >= 85:
        return 'Сервер в целом в хорошем состоянии.'
    if score >= 70:
        return 'Есть замечания, требующие внимания.'
    if score >= 50:
        return 'Обнаружены серьёзные проблемы производительности.'
    return 'Критическое состояние — требуется срочное вмешательство.'


def _build_causes(st, snapshot, findings) -> List[Cause]:
    causes: List[Cause] = []
    pct = st['cat_pct']

    def top_wait(*types):
        for w, ms in st['waits_top']:
            if w in types:
                return w, ms
        return None, 0

    # 1. ЦП
    if (st['cpu_avg'] or 0) > 65 or (pct.get('cpu') or 0) > 30:
        ev = ['Средняя загрузка ЦП SQL: %s%%, пиковая: %s%%'
              % (_fnum(st['cpu_avg'], '—'), _fnum(st['cpu_max'], '—'))]
        if pct.get('cpu'):
            ev.append('Доля ожиданий ЦП/параллелизма: %.0f%% от всех ожиданий' % pct['cpu'])
        if st['compile_ratio_avg'] and st['compile_ratio_avg'] > 0.15:
            ev.append('Компиляции: %.0f%% от Batch Requests/sec' % (st['compile_ratio_avg'] * 100))
        tq = snapshot.get('top_queries') or []
        if tq:
            ev.append('Самый дорогой запрос: %.0f мс ЦП в среднем (%s)'
                      % (tq[0].get('avg_cpu_ms') or 0, (tq[0].get('db_name') or '')))
        fixes = ['Оптимизировать топ-запросы по ЦП (раздел «Топ запросов»).']
        if top_wait('CXPACKET')[0]:
            fixes.append('Настроить MAXDOP ≤ 8 и cost threshold for parallelism 25–50.')
        if st['compile_ratio_avg'] and st['compile_ratio_avg'] > 0.15:
            fixes.append('Включить optimize for ad hoc workloads, параметризовать запросы.')
        causes.append(Cause('critical' if (st['cpu_avg'] or 0) > 85 else 'warning',
                            'Процессор перегружен', ev,
                            'SQL Server не хватает вычислительных ресурсов: запросы стоят в очереди '
                            'на планировщики, растут ожидания SOS_SCHEDULER_YIELD/CXPACKET.', fixes))

    # 2. Память / буферный кэш
    if (st['ple_min'] is not None and st['ple_min'] < 300) or (pct.get('data_io') or 0) > 20:
        ev = []
        if st['ple_min'] is not None:
            ev.append('PLE: мин %s, среднее %s сек (низкие значения = сброс кэша)'
                      % (_fnum(st['ple_min'], '—'), _fnum(st['ple_avg'], '—')))
        if pct.get('data_io'):
            ev.append('Доля ожиданий чтения с диска (PAGEIOLATCH и др.): %.0f%%' % pct['data_io'])
        ev.append('Чтений с диска в среднем: %s стр/сек' % _fnum(st['reads_avg'], '—'))
        fixes = ['Проверить/увеличить max server memory и объём ОЗУ сервера.',
                 'Создать недостающие индексы, устранить сканирования больших таблиц.',
                 'Проверить задержки чтения по файлам (раздел «Диски и файлы»).']
        causes.append(Cause('warning' if (st['ple_min'] or 9999) > 150 else 'critical',
                            'Нехватка памяти для буферного кэша (или лишние чтения с диска)', ev,
                            'Данные не помещаются в буферный пул: SQL Server постоянно читает страницы '
                            'с диска. Либо памяти мало, либо запросы читают слишком много лишнего '
                            '(сканирования, отсутствующие индексы).', fixes))

    # 3. Журнал транзакций
    log_files = [f for f in (snapshot.get('files') or []) if f.get('type_desc') == 'LOG']
    worst_log = max((f.get('write_latency_ms') or 0) for f in log_files) if log_files else 0
    if (pct.get('log_io') or 0) > 10 or worst_log > 15:
        ev = []
        if pct.get('log_io'):
            ev.append('Доля WRITELOG и связанных ожиданий: %.0f%%' % pct['log_io'])
        if worst_log:
            ev.append('Средняя задержка записи журнала: до %.0f мс (норма < 10–15 мс)' % worst_log)
        causes.append(Cause('warning', 'Медленная запись журнала транзакций', ev,
                            'Диск не успевает фиксировать транзакции — все записи в БД замедляются '
                            '(ожидания WRITELOG).',
                            ['Перенести .ldf на более быстрый диск (SSD/NVMe), отдельно от данных.',
                             'Проверить частые мелкие autogrowth журнала.',
                             'Сократить объём/длительность транзакций приложения.']))

    # 4. Блокировки
    blocking_hist = st.get('blocked_events') or 0
    if (pct.get('locks') or 0) > 10 or blocking_hist > 0 or (st['blocked_max'] or 0) >= 3:
        ev = []
        if blocking_hist:
            ev.append('Заблокированные процессы наблюдались в %d замерах (максимум %d)'
                      % (blocking_hist, st['blocked_max']))
        if pct.get('locks'):
            ev.append('Доля ожиданий блокировок (LCK_M_*): %.0f%%' % pct['locks'])
        causes.append(Cause('warning', 'Блокировки между транзакциями', ev,
                            'Одни транзакции удерживают блокировки, другие ждут. Обычно виновата '
                            'длинная транзакция (head blocker) или сканирования, захватывающие '
                            'множество строк.',
                            ['Найти head-blocker в разделе «Блокировки» и оптимизировать его транзакцию.',
                             'Сократить длительность транзакций в приложении (коммитить чаще).',
                             'Рассмотреть READ_COMMITTED_SNAPSHOT для OLTP-баз.',
                             'Оптимизировать запросы-жертвы (индексы).']))

    # 5. Гранты памяти
    if (pct.get('memory') or 0) > 5 or (st['grants_max'] or 0) > 0:
        causes.append(Cause('warning', 'Конкуренция за память запросов',
                            ['Доля ожиданий RESOURCE_SEMAPHORE и связанных: %.0f%%' % (pct.get('memory') or 0),
                             'Максимум ожидающих грантов: %s' % _fnum(st['grants_max'], '—')],
                            'Тяжёлым запросам (сортировки, хеширования) не хватает памяти — они ждут '
                            'освобождения грантов, время выполнения растёт.',
                            ['Оптимизировать запросы с большими сортировками/группировками.',
                             'Обновить статистику (завышенные оценки → завышенные гранты).',
                             'Проверить достаточность max server memory.']))

    # 6. Сеть/клиент
    if (pct.get('network') or 0) > 15:
        causes.append(Cause('info', 'Клиентские приложения забирают данные медленно',
                            ['Доля ASYNC_NETWORK_IO: %.0f%%' % pct['network']],
                            'Сервер простаивает, ожидая, пока приложение прочитает результат запроса.',
                            ['Буферизовать чтение результатов на клиенте.',
                             'Уменьшать объём выборок (TOP/WHERE/пагинация).',
                             'Проверить сеть между приложением и SQL Server.']))

    # 7. Резервное копирование в рабочие часы
    if (pct.get('backup') or 0) > 8:
        causes.append(Cause('info', 'Резервное копирование конкурирует за ресурсы',
                            ['Доля ожиданий BACKUPIO/BACKUPBUFFER: %.0f%%' % pct['backup']],
                            'Копирование создаёт заметную нагрузку на диски в наблюдаемый период.',
                            ['Перенести бэкапы на ночное окно.', 'Включить сжатие бэкапов.',
                             'Проверить скорость целевого диска.']))

    if not causes:
        causes.append(Cause('ok', 'Явных узких мест не обнаружено',
                            ['Суммарные ожидания за период невелики и равномерно распределены.'],
                            'В наблюдаемый период нет выраженного дефицита процессора, памяти, дисков '
                            'или блокировок. Если пользователи всё же жалуются на медленную работу — '
                            'проверьте конкретные запросы и время их выполнения.',
                            ['Продолжать суточный мониторинг для накопления статистики.',
                             'Изучить раздел «Топ запросов» — возможно, дело в конкретных запросах.']))
    order = {'critical': 0, 'warning': 1, 'info': 2, 'ok': 3}
    causes.sort(key=lambda c: order.get(c.severity, 9))
    return causes


# ------------------------------------------------------------------ helpers
def _fnum(v, default='—'):
    try:
        v = float(v)
    except (TypeError, ValueError):
        return default
    if v >= 1000000:
        return '%.1f млн' % (v / 1e6)
    if v >= 1000:
        return '%s' % f"{int(round(v)):,}".replace(',', ' ')
    if v == int(v):
        return '%d' % int(v)
    return '%.1f' % v


def _fm_gb(mb):
    return '%.0f ГБ' % (mb / 1024.0)


def missing_index_script(mi: dict) -> str:
    table = mi.get('table_ref') or '[?]'
    eq = (mi.get('equality_columns') or '').strip()
    ineq = (mi.get('inequality_columns') or '').strip()
    inc = (mi.get('included_columns') or '').strip()
    keys = ', '.join(x for x in [eq, ineq] if x)
    if not keys:
        keys = '[id]'
    name = 'IX_' + table.replace('[', '').replace(']', '').replace('.', '_').replace(' ', '_')[:90]
    body = "USE [%s];\nCREATE NONCLUSTERED INDEX [%s]\n    ON %s (%s)" % (mi.get('db_name', ''), name, table, keys)
    if inc:
        body += "\n    INCLUDE (%s)" % inc
    body += ";\n-- Перед созданием проверьте на тестовой среде и объедините с существующими индексами."
    return body
