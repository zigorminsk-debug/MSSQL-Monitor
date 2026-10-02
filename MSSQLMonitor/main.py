# -*- coding: utf-8 -*-
"""MS SQL Monitor — мониторинг и диагностика MS SQL Server.

Запуск без аргументов — графический интерфейс.
  --demo-report [ПУТЬ]  — сформировать демонстрационный HTML-отчёт (24 ч имитации)
  --selftest            — проверка всех подсистем на демо-данных
  --version             — версия
"""
import argparse
import os
import sys
import tempfile

def main(argv=None):
    p = argparse.ArgumentParser(prog='MSSQLMonitor',
                                description='Мониторинг и диагностика MS SQL Server')
    p.add_argument('--demo-report', nargs='?', const='demo_report.html', metavar='ПУТЬ',
                   help='сформировать демонстрационный HTML-отчёт и выйти')
    p.add_argument('--selftest', action='store_true', help='самопроверка подсистем')
    p.add_argument('--version', action='version', version='MS SQL Monitor 1.0.0')
    args = p.parse_args(argv)

    if args.selftest:
        return selftest()
    if args.demo_report:
        return demo_report(args.demo_report)

    from app.gui import run_gui
    run_gui()
    return 0


def _run_pipeline(out_path=None):
    """Полный конвейер на демо-данных: история 24 ч -> снимок -> анализ -> отчёт."""
    from app.collector import Collector, Store
    from app.demo import DemoEngine, generate_demo_history
    from app.analyzer import analyze
    from app.reporter import build_report

    tmpdir = tempfile.mkdtemp(prefix='mssqlmon_')
    store = Store(os.path.join(tmpdir, 'monitor.db'))
    engine = DemoEngine(interval=30, history_hours=24)
    n = generate_demo_history(store, engine=engine, hours=24, interval=30)
    collector = Collector(demo=engine)
    snapshot = collector.full_snapshot()
    history = store.history(24)
    analysis = analyze(snapshot, history)
    if out_path:
        build_report(snapshot, history, analysis, None, out_path,
                     title='Демонстрационный отчёт — SQL Server 2019 (имитация)')
    store.close()
    return snapshot, history, analysis, n


def demo_report(path):
    if not os.path.isabs(path):
        from app.config import reports_dir
        path = os.path.join(reports_dir(), path)
    print('Генерация демонстрационного отчёта (имитация суток мониторинга)…')
    snapshot, history, analysis, n = _run_pipeline(path)
    c = analysis.counts
    print('Готово: %s' % os.path.abspath(path))
    print('  замеров за 24 ч: %d' % n)
    print('  оценка сервера: %d/100 (%s)' % (analysis.health_score, analysis.summary))
    print('  проблем: критично=%d, предупреждений=%d, рекомендаций=%d'
          % (c['critical'], c['warning'], c['info']))
    print('  причин замедления: %d' % len(analysis.causes))
    return 0


def selftest():
    print('Самопроверка подсистем MS SQL Monitor…')
    snapshot, history, analysis, n = _run_pipeline()
    checks = [
        ('история за 24 ч (замеров)', n >= 2800),
        ('снимок содержит конфигурацию', bool(snapshot.get('config'))),
        ('снимок содержит базы данных', len(snapshot.get('databases') or []) >= 3),
        ('снимок содержит файлы', len(snapshot.get('files') or []) >= 4),
        ('снимок содержит топ запросов', len(snapshot.get('top_queries') or []) >= 3),
        ('найдены недостающие индексы', len(snapshot.get('missing_indexes') or []) >= 1),
        ('история: есть замеры', len(history['samples']) >= 2800),
        ('история: есть ожидания', len(history['waits']) > 100),
        ('анализ: есть рекомендации', len(analysis.findings) >= 10),
        ('анализ: есть причины замедлений', len(analysis.causes) >= 3),
        ('анализ: категории ожиданий', len(analysis.wait_by_cat) >= 3),
        ('анализ: health score в границах', 5 <= analysis.health_score <= 100),
    ]
    failed = 0
    for name, ok in checks:
        print('  [%s] %s' % ('OK' if ok else 'FAIL', name))
        if not ok:
            failed += 1
    if failed:
        print('ПРОВАЛЕНО проверок: %d' % failed)
        return 1
    print('Все проверки пройдены. (%d рекомендаций, оценка %d/100)'
          % (len(analysis.findings), analysis.health_score))
    return 0


if __name__ == '__main__':
    sys.exit(main())
