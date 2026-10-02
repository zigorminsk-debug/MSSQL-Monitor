# -*- coding: utf-8 -*-
"""Графический интерфейс MS SQL Monitor (Tkinter)."""
import os
import queue
import threading
import traceback
import webbrowser
from datetime import datetime

import tkinter as tk
from tkinter import ttk, messagebox

from . import db
from .analyzer import analyze, SEV_RU
from .collector import Collector, Store
from .config import (APP_TITLE, APP_VERSION, db_path, load_settings, reports_dir,
                     save_settings, data_dir)
from .demo import DemoEngine
from .reporter import build_report, fnum

ACCENT = '#1d4ed8'
OK_GREEN = '#16a34a'
BAD_RED = '#dc2626'
WARN_AMBER = '#f59e0b'
MUTED = '#64748b'
CARD_BG = '#ffffff'
BG = '#eef2f7'


class MonitorApp:
    def __init__(self, root: tk.Tk):
        self.root = root
        self.settings = load_settings()
        self.store = Store(db_path())
        self.store.purge(self.settings.retention_days)

        self.backend = None            # живое подключение (Backend)
        self.demo_engine = None        # либо демо-движок
        self.collector: Collector = None
        self.analysis = None
        self.demo_mode = False

        self.ui_queue = queue.Queue()
        self.stop_evt = threading.Event()
        self.collect_lock = threading.Lock()
        self.sampler_thread = None
        self.daily_thread = None
        self._consecutive_errors = 0
        self._report_counter = 0

        self.chart_data = []           # [(ts, cpu, batch)]
        self._build_ui()
        self._poll()
        self.root.after(300, self._maybe_autostart)
        self.root.protocol('WM_DELETE_WINDOW', self._on_close)

    # ================================================================ UI
    def _build_ui(self):
        self.root.title(APP_TITLE)
        self.root.geometry('1060x720')
        self.root.minsize(860, 600)
        self.root.configure(bg=BG)
        style = ttk.Style(self.root)
        try:
            style.theme_use('clam')
        except Exception:
            pass
        style.configure('TButton', padding=(10, 5), font=('Segoe UI', 9))
        style.configure('Accent.TButton', foreground='white', background=ACCENT, padding=(12, 6))
        style.map('Accent.TButton', background=[('active', '#1e40af'), ('disabled', '#93c5fd')])
        style.configure('TLabelframe.Label', font=('Segoe UI', 9, 'bold'), foreground='#334155')
        style.configure('TNotebook.Tab', padding=(14, 7), font=('Segoe UI', 9))

        ico = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), 'icon.ico')
        if os.name == 'nt' and os.path.exists(ico):
            try:
                self.root.iconbitmap(ico)
            except Exception:
                pass

        # ---------------- верхняя панель: подключение ---------------------
        top = tk.Frame(self.root, bg='white', pady=8, padx=12,
                       highlightbackground='#e2e8f0', highlightthickness=1)
        top.pack(fill='x')

        tk.Label(top, text='Сервер:', bg='white').grid(row=0, column=0, sticky='w')
        self.e_server = ttk.Entry(top, width=20)
        self.e_server.insert(0, self.settings.server)
        self.e_server.grid(row=0, column=1, padx=(4, 10))

        tk.Label(top, text='Аутентификация:', bg='white').grid(row=0, column=2, sticky='w')
        self.c_auth = ttk.Combobox(top, width=14, state='readonly',
                                   values=['Windows (доменная)', 'SQL Server (логин/пароль)'])
        self.c_auth.current(0 if self.settings.auth == 'windows' else 1)
        self.c_auth.grid(row=0, column=3, padx=(4, 10))

        self.l_login = tk.Label(top, text='Логин:', bg='white')
        self.e_login = ttk.Entry(top, width=14)
        self.e_login.insert(0, self.settings.login)
        self.l_pass = tk.Label(top, text='Пароль:', bg='white')
        self.e_pass = ttk.Entry(top, width=14, show='•')
        self.e_pass.insert(0, self.settings.password)
        self._grid_auth_fields()

        self.b_connect = ttk.Button(top, text='Подключиться', command=self._connect,
                                    style='Accent.TButton')
        self.b_connect.grid(row=0, column=10, padx=(10, 4))
        self.b_test = ttk.Button(top, text='Тест', command=self._test_conn)
        self.b_test.grid(row=0, column=11, padx=2)
        self.b_demo = ttk.Button(top, text='🧪 Демо-режим', command=self._enable_demo)
        self.b_demo.grid(row=0, column=12, padx=(10, 0))

        self.l_conn = tk.Label(top, text='● не подключено', fg=MUTED, bg='white')
        self.l_conn.grid(row=1, column=0, columnspan=13, sticky='w', pady=(4, 0))

        self.c_auth.bind('<<ComboboxSelected>>', lambda e: self._grid_auth_fields())

        # ---------------- вкладки -----------------------------------------
        self.nb = ttk.Notebook(self.root)
        self.nb.pack(fill='both', expand=True, padx=12, pady=(10, 0))

        self.tab_mon = tk.Frame(self.nb, bg=BG)
        self.tab_recs = tk.Frame(self.nb, bg=BG)
        self.tab_reports = tk.Frame(self.nb, bg=BG)
        self.tab_log = tk.Frame(self.nb, bg=BG)
        self.nb.add(self.tab_mon, text='  Мониторинг  ')
        self.nb.add(self.tab_recs, text='  Рекомендации  ')
        self.nb.add(self.tab_reports, text='  Отчёты  ')
        self.nb.add(self.tab_log, text='  Журнал  ')

        self._build_monitor_tab()
        self._build_recs_tab()
        self._build_reports_tab()
        self._build_log_tab()

        # ---------------- строка состояния --------------------------------
        sb = tk.Frame(self.root, bg='white', pady=4, padx=12,
                      highlightbackground='#e2e8f0', highlightthickness=1)
        sb.pack(fill='x', side='bottom', pady=(8, 0))
        self.l_status = tk.Label(sb, text='Готов', bg='white', fg='#334155')
        self.l_status.pack(side='left')
        self.l_monitor_state = tk.Label(sb, text='○ мониторинг остановлен', bg='white', fg=MUTED)
        self.l_monitor_state.pack(side='right')

    def _grid_auth_fields(self):
        sql_auth = self.c_auth.current() == 1
        if sql_auth:
            self.l_login.grid(row=0, column=4, sticky='w')
            self.e_login.grid(row=0, column=5, padx=(4, 10))
            self.l_pass.grid(row=0, column=6, sticky='w')
            self.e_pass.grid(row=0, column=7, padx=(4, 10))
        else:
            for w in (self.l_login, self.e_login, self.l_pass, self.e_pass):
                w.grid_remove()

    # ---------------------------------------------------------------- mono
    def _build_monitor_tab(self):
        f = self.tab_mon
        cards = tk.Frame(f, bg=BG)
        cards.pack(fill='x', pady=(10, 4), padx=10)
        self.cards = {}
        spec = [('cpu', 'ЦП SQL, %'), ('batch', 'Batch/сек'), ('conn', 'Подключений'),
                ('ple', 'PLE, сек'), ('blocked', 'Заблокировано'), ('health', 'Health score')]
        for i, (key, label) in enumerate(spec):
            card = tk.Frame(cards, bg=CARD_BG, padx=16, pady=10,
                            highlightbackground='#e2e8f0', highlightthickness=1)
            card.grid(row=0, column=i, padx=(0, 10), sticky='nsew')
            cards.columnconfigure(i, weight=1)
            v = tk.Label(card, text='—', font=('Segoe UI', 18, 'bold'), bg=CARD_BG, fg='#0f172a')
            v.pack()
            l = tk.Label(card, text=label, font=('Segoe UI', 9), bg=CARD_BG, fg=MUTED)
            l.pack()
            self.cards[key] = v

        chart_card = tk.Frame(f, bg=CARD_BG, padx=8, pady=8,
                              highlightbackground='#e2e8f0', highlightthickness=1)
        chart_card.pack(fill='both', expand=True, padx=10, pady=(4, 4))
        tk.Label(chart_card, text='Загрузка в реальном времени (ЦП SQL — красная линия, Batch/сек — синяя)',
                 bg=CARD_BG, fg=MUTED, font=('Segoe UI', 9)).pack(anchor='w', padx=4)
        self.canvas = tk.Canvas(chart_card, bg='white', highlightthickness=0)
        self.canvas.pack(fill='both', expand=True, padx=4, pady=4)
        self.canvas.bind('<Configure>', lambda e: self._draw_chart())

        ctrl = tk.Frame(f, bg=BG)
        ctrl.pack(fill='x', padx=10, pady=(4, 10))
        self.b_start = ttk.Button(ctrl, text='▶ Начать суточный мониторинг',
                                  command=self._start_monitoring, style='Accent.TButton')
        self.b_start.pack(side='left')
        self.b_stop = ttk.Button(ctrl, text='⏸ Остановить', command=self._stop_monitoring,
                                 state='disabled')
        self.b_stop.pack(side='left', padx=6)
        self.b_snapshot = ttk.Button(ctrl, text='📸 Полный снимок + отчёт',
                                     command=self._make_snapshot_report, state='disabled')
        self.b_snapshot.pack(side='left', padx=6)
        self.b_daily = ttk.Button(ctrl, text='📄 Суточный отчёт (24 ч)',
                                  command=self._make_daily_report, state='disabled')
        self.b_daily.pack(side='left', padx=6)
        tk.Label(ctrl, text='Интервал, сек:', bg=BG, fg=MUTED).pack(side='left', padx=(16, 2))
        self.sb_interval = ttk.Spinbox(ctrl, from_=10, to=3600, increment=10, width=6)
        self.sb_interval.set(self.settings.sample_interval)
        self.sb_interval.pack(side='left')
        self.b_settings = ttk.Button(ctrl, text='⚙ Настройки', command=self._open_settings)
        self.b_settings.pack(side='right')

    # ---------------------------------------------------------------- recs
    def _build_recs_tab(self):
        f = self.tab_recs
        top = tk.Frame(f, bg=BG)
        top.pack(fill='x', padx=10, pady=(10, 4))
        ttk.Button(top, text='🔄 Выполнить анализ сейчас', command=self._make_snapshot_report)\
            .pack(side='left')
        self.l_recs_summary = tk.Label(top, text='Анализ ещё не выполнялся', bg=BG, fg=MUTED)
        self.l_recs_summary.pack(side='left', padx=12)

        cols = ('sev', 'area', 'title')
        self.tree = ttk.Treeview(f, columns=cols, show='headings', selectmode='browse')
        self.tree.heading('sev', text='Важность')
        self.tree.heading('area', text='Область')
        self.tree.heading('title', text='Рекомендация')
        self.tree.column('sev', width=110, anchor='w')
        self.tree.column('area', width=140, anchor='w')
        self.tree.column('title', width=560, anchor='w')
        self.tree.pack(fill='both', expand=True, padx=10, pady=4)
        self.tree.tag_configure('critical', foreground='#b91c1c')
        self.tree.tag_configure('warning', foreground='#b45309')
        self.tree.tag_configure('info', foreground='#1d4ed8')
        self.tree.bind('<<TreeviewSelect>>', self._show_finding)

        self.f_text = tk.Text(f, height=9, wrap='word', font=('Consolas', 10),
                              state='disabled', bg='#0f172a', fg='#e2e8f0', padx=10, pady=8)
        self.f_text.pack(fill='both', padx=10, pady=(4, 10))

    # ------------------------------------------------------------- reports
    def _build_reports_tab(self):
        f = self.tab_reports
        top = tk.Frame(f, bg=BG)
        top.pack(fill='x', padx=10, pady=(10, 4))
        ttk.Button(top, text='Открыть', command=self._open_report).pack(side='left')
        ttk.Button(top, text='Открыть папку', command=self._open_reports_dir).pack(side='left', padx=6)
        ttk.Button(top, text='Обновить список', command=self._refresh_reports).pack(side='left')
        self.l_reports_hint = tk.Label(
            f, text='Отчёты сохраняются в %s' % reports_dir(), bg=BG, fg=MUTED)
        self.l_reports_hint.pack(anchor='w', padx=12)
        self.lb_reports = tk.Listbox(f, font=('Segoe UI', 10))
        self.lb_reports.pack(fill='both', expand=True, padx=10, pady=(4, 10))
        self.lb_reports.bind('<Double-Button-1>', lambda e: self._open_report())
        self._refresh_reports()

    # ----------------------------------------------------------------- log
    def _build_log_tab(self):
        self.log_text = tk.Text(self.tab_log, wrap='word', font=('Consolas', 9),
                                state='disabled', bg='#0f172a', fg='#cbd5e1', padx=10, pady=8)
        self.log_text.pack(fill='both', expand=True, padx=10, pady=10)

    def log(self, msg):
        self.ui_queue.put(('log', msg))

    def _log_write(self, msg):
        self.log_text.configure(state='normal')
        self.log_text.insert('end', '[%s] %s\n' % (datetime.now().strftime('%H:%M:%S'), msg))
        self.log_text.see('end')
        self.log_text.configure(state='disabled')

    # ============================================================ поток UI
    def _poll(self):
        try:
            while True:
                kind, payload = self.ui_queue.get_nowait()
                if kind == 'sample':
                    self._on_sample(payload)
                elif kind == 'log':
                    self._log_write(payload)
                elif kind == 'status':
                    self.l_status.configure(text=payload)
                elif kind == 'conn':
                    ok, msg = payload
                    self._after_connect(ok, msg)
                elif kind == 'report':
                    self._on_report_ready(*payload)
                elif kind == 'error':
                    self._log_write('ОШИБКА: %s' % payload)
                    self.l_status.configure(text='Ошибка: %s' % payload[:80])
                elif kind == 'sample_error_stop':
                    self._stop_monitoring()
                    self._set_status('Мониторинг остановлен: сервер недоступен (см. журнал)')
        except queue.Empty:
            pass
        self.root.after(400, self._poll)

    def _maybe_autostart(self):
        if self.settings.autostart_monitoring and self.backend:
            self._start_monitoring()

    # ========================================================== подключение
    def _read_conn_settings(self):
        s = self.settings
        s.server = self.e_server.get().strip() or 'localhost'
        s.auth = 'sql' if self.c_auth.current() == 1 else 'windows'
        s.login = self.e_login.get().strip()
        s.password = self.e_pass.get()
        return s

    def _connect(self):
        self._read_conn_settings()
        save_settings(self.settings)
        self.b_connect.configure(state='disabled')
        self.l_conn.configure(text='● подключение…', fg=WARN_AMBER)
        threading.Thread(target=self._connect_worker, daemon=True).start()

    def _connect_worker(self):
        try:
            backend = db.connect(self.settings)
            self.ui_queue.put(('conn', (True, backend.name)))
            with self.collect_lock:
                if self.backend:
                    self.backend.close()
                self.backend = backend
                self.collector = Collector(backend=backend, settings=self.settings)
            self.ui_queue.put(('log', 'Подключено: %s (%s)' % (self.settings.server, backend.name)))
        except Exception as e:
            self.ui_queue.put(('conn', (False, str(e))))

    def _test_conn(self):
        self._read_conn_settings()
        self.b_test.configure(state='disabled')
        threading.Thread(target=self._test_worker, daemon=True).start()

    def _test_worker(self):
        try:
            info = db.test_connection(self.settings)
            self.ui_queue.put(('log', 'Тест подключения: OK — %s' % info))
            messagebox.showinfo('Тест подключения', 'Успешно!\n\n%s' % info)
        except Exception as e:
            self.ui_queue.put(('log', 'Тест подключения: %s' % e))
            messagebox.showerror('Тест подключения', str(e))
        finally:
            self.root.after(0, lambda: self.b_test.configure(state='normal'))

    def _enable_demo(self):
        self.demo_mode = True
        self.demo_engine = DemoEngine(interval=self.settings.sample_interval, realtime=True)
        with self.collect_lock:
            if self.backend:
                self.backend.close()
                self.backend = None
            self.collector = Collector(settings=self.settings, demo=self.demo_engine)
        self._after_connect(True, 'демо-данные (SQLSRV-PROD-01 — имитация)')
        self.log('Включён демо-режим: все данные имитируются. Сгенерируйте отчёт, чтобы '
                 'посмотреть пример анализа.')

    def _after_connect(self, ok, msg):
        self.b_connect.configure(state='normal')
        if ok:
            self.l_conn.configure(text='● подключено: %s' % msg, fg=OK_GREEN)
            self.b_snapshot.configure(state='normal')
            self.b_daily.configure(state='normal')
            self.b_start.configure(state='normal')
            self._set_status('Подключено. Можно запускать мониторинг и отчёты.')
        else:
            self.l_conn.configure(text='● ошибка подключения', fg=BAD_RED)
            messagebox.showerror('Подключение', msg)

    def _set_status(self, text):
        self.l_status.configure(text=text)

    # =========================================================== мониторинг
    def _start_monitoring(self):
        if not self.collector:
            messagebox.showwarning('Мониторинг', 'Сначала подключитесь к серверу или включите демо-режим.')
            return
        self.settings.sample_interval = max(10, int(self.sb_interval.get() or 30))
        save_settings(self.settings)
        self.stop_evt.clear()
        self._consecutive_errors = 0
        self._monitoring_started_at = datetime.now()
        self.sampler_thread = threading.Thread(target=self._sampler_loop, daemon=True)
        self.sampler_thread.start()
        self.daily_thread = threading.Thread(target=self._daily_loop, daemon=True)
        self.daily_thread.start()
        self.b_start.configure(state='disabled')
        self.b_stop.configure(state='normal')
        self.l_monitor_state.configure(text='● СУТОЧНЫЙ МОНИТОРИНГ: каждые %d сек' %
                                       self.settings.sample_interval, fg=OK_GREEN)
        self.log('Мониторинг запущен (интервал %d сек). Показания пишутся в историю.'
                 % self.settings.sample_interval)

    def _stop_monitoring(self):
        self.stop_evt.set()
        self.b_start.configure(state='normal')
        self.b_stop.configure(state='disabled')
        self.l_monitor_state.configure(text='○ мониторинг остановлен', fg=MUTED)
        self.log('Мониторинг остановлен.')

    def _sampler_loop(self):
        while not self.stop_evt.is_set():
            try:
                with self.collect_lock:
                    smp = self.collector.light_sample()
                self.store.save_sample(smp)
                self._consecutive_errors = 0
                self.ui_queue.put(('sample', smp))
            except Exception as e:
                self._consecutive_errors += 1
                self.ui_queue.put(('error', 'Съём показаний: %s' % e))
                if self._consecutive_errors >= 5 and not self.demo_mode:
                    self.ui_queue.put(('log', 'Мониторинг остановлен: недоступен сервер.'))
                    self.ui_queue.put(('sample_error_stop', None))
                    return
            self.stop_evt.wait(self.settings.sample_interval)
        # финальный снимок не нужен

    def _daily_loop(self):
        while not self.stop_evt.is_set():
            if not self.settings.daily_report_enabled:
                self.stop_evt.wait(60)
                continue
            try:
                hh, mm = map(int, self.settings.daily_report_time.split(':'))
            except ValueError:
                hh, mm = 8, 0
            now = datetime.now()
            today = now.strftime('%Y-%m-%d')
            target = now.replace(hour=hh, minute=mm, second=0, microsecond=0)
            started = getattr(self, '_monitoring_started_at', None)
            # отчёт формируется, только если настроенное время настало ВО ВРЕМЯ
            # мониторинга (не сразу при запуске) и уже накоплено >= 15 мин данных
            if (self.store.get_meta('last_daily') != today and started
                    and started <= target <= now
                    and (now - started).total_seconds() >= 900):
                self.store.set_meta('last_daily', today)
                self.log('Автоматическая генерация суточного отчёта…')
                self._generate_report(daily=True, silent=True)
            self.stop_evt.wait(60)

    # ============================================================= отчёты
    def _make_snapshot_report(self):
        self._generate_report(daily=False, silent=False)

    def _make_daily_report(self):
        self._generate_report(daily=True, silent=False)

    def _generate_report(self, daily=False, silent=False):
        if not self.collector:
            messagebox.showwarning('Отчёт', 'Сначала подключитесь или включите демо-режим.')
            return
        prefix = 'daily' if daily else 'report'
        name = '%s_%s.html' % (prefix, datetime.now().strftime('%Y%m%d_%H%M%S'))
        out = os.path.join(reports_dir(), name)
        self._set_status('Формирование отчёта (полный снимок — это до 1–2 минут)…')
        self.b_snapshot.configure(state='disabled')
        self.b_daily.configure(state='disabled')
        threading.Thread(target=self._report_worker, args=(out, daily, silent),
                         daemon=True).start()

    def _report_worker(self, out, daily, silent):
        try:
            with self.collect_lock:
                snapshot = self.collector.full_snapshot()
            history = self.store.history(24)
            self.analysis = analyze(snapshot, history)
            title = ('Суточный отчёт мониторинга' if daily else 'Полный отчёт (снимок)') + \
                ' — %s' % (self.settings.server if not self.demo_mode else 'демо-сервер')
            build_report(snapshot, history, self.analysis, self.settings, out, title=title)
            self.ui_queue.put(('report', (out, self.analysis, silent)))
        except Exception as e:
            err = '%s\n%s' % (e, traceback.format_exc()[-400:])
            self.ui_queue.put(('error', 'Отчёт: %s' % err))

    def _on_report_ready(self, path, analysis, silent):
        self.b_snapshot.configure(state='normal')
        self.b_daily.configure(state='normal')
        self._set_status('Отчёт готов: %s' % os.path.basename(path))
        self.log('Отчёт сохранён: %s (оценка %d/100, критично: %d, предупреждений: %d)'
                 % (path, analysis.health_score, analysis.counts['critical'],
                    analysis.counts['warning']))
        self._fill_findings(analysis)
        self._refresh_reports()
        self.cards['health'].configure(text=str(analysis.health_score))
        if not silent and messagebox.askyesno('Отчёт готов',
                                              'Отчёт сформирован:\n%s\n\nОткрыть в браузере?' % path):
            webbrowser.open('file://' + path)

    # ========================================================= findings UI
    def _fill_findings(self, analysis):
        self.tree.delete(*self.tree.get_children())
        for i, f in enumerate(analysis.findings):
            self.tree.insert('', 'end', iid=str(i), tags=(f.severity,),
                             values=(SEV_RU[f.severity], f.area, f.title))
        c = analysis.counts
        self.l_recs_summary.configure(
            text='Оценка: %d/100 · критично: %d · предупреждений: %d · рекомендаций: %d'
                 % (analysis.health_score, c['critical'], c['warning'], c['info']),
            fg='#0f172a')
        if analysis.findings:
            self.tree.selection_set('0')
            self.tree.focus('0')

    def _show_finding(self, _evt=None):
        sel = self.tree.selection()
        if not sel or not self.analysis:
            return
        f = self.analysis.findings[int(sel[0])]
        parts = []
        if f.problem:
            parts.append('ПРОБЛЕМА\n' + f.problem)
        if f.recommendation:
            parts.append('\nРЕКОМЕНДАЦИЯ\n' + f.recommendation)
        if f.script:
            parts.append('\nSQL-СКРИПТ\n' + f.script)
        self.f_text.configure(state='normal')
        self.f_text.delete('1.0', 'end')
        self.f_text.insert('1.0', '\n\n'.join(parts) or f.title)
        self.f_text.configure(state='disabled')

    # ============================================================== отчёты
    def _refresh_reports(self):
        self.lb_reports.delete(0, 'end')
        try:
            files = sorted((os.path.join(reports_dir(), f) for f in os.listdir(reports_dir())
                            if f.endswith('.html')), key=os.path.getmtime, reverse=True)
        except Exception:
            files = []
        for p in files:
            self.lb_reports.insert('end', '%s  (%s)' % (
                os.path.basename(p), datetime.fromtimestamp(os.path.getmtime(p)).strftime('%d.%m %H:%M')))
        self._report_files = files

    def _open_report(self):
        sel = self.lb_reports.curselection()
        if not sel:
            messagebox.showinfo('Отчёты', 'Выберите отчёт в списке.')
            return
        path = self._report_files[sel[0]]
        webbrowser.open('file://' + path)

    def _open_reports_dir(self):
        path = reports_dir()
        if os.name == 'nt':
            os.startfile(path)
        else:
            webbrowser.open('file://' + path)

    # ========================================================= мониторинг UI
    def _on_sample(self, smp):
        cpu = smp['cpu']
        p = smp.get('points') or {}
        r = smp.get('rates') or {}
        self.cards['cpu'].configure(text=str(fnum(cpu.get('sql'), '—')))
        self.cards['cpu'].configure(fg=BAD_RED if (cpu.get('sql') or 0) > 80 else '#0f172a')
        self.cards['batch'].configure(text=str(fnum(r.get('batch_sec'), '—')))
        self.cards['conn'].configure(text=str(fnum(p.get('connections'), '—')))
        ple = p.get('ple')
        self.cards['ple'].configure(text=str(fnum(ple, '—')))
        self.cards['ple'].configure(fg=BAD_RED if (ple or 999) < 300 else '#0f172a')
        blk = p.get('blocked') or 0
        self.cards['blocked'].configure(text=str(fnum(blk)))
        self.cards['blocked'].configure(fg=BAD_RED if blk > 0 else '#0f172a')
        self._set_status('Последний замер: %s' % smp['ts'].strftime('%d.%m %H:%M:%S'))

        self.chart_data.append((smp['ts'], cpu.get('sql') or 0, r.get('batch_sec') or 0))
        if len(self.chart_data) > 600:
            self.chart_data = self.chart_data[-600:]
        self._draw_chart()

    def _draw_chart(self):
        c = self.canvas
        try:
            w = c.winfo_width()
            h = c.winfo_height()
        except Exception:
            return
        if w < 60 or h < 40:
            return
        c.delete('all')
        pl, pr, pt, pb = 42, 52, 12, 20
        gw, gh = w - pl - pr, h - pt - pb
        # сетка 0-100%
        for i in range(5):
            v = i * 25
            y = pt + gh * (1 - v / 100)
            c.create_line(pl, y, w - pr, y, fill='#e2e8f0')
            c.create_text(pl - 6, y, text=str(v), anchor='e', fill='#94a3b8', font=('Segoe UI', 8))
        data = self.chart_data
        if len(data) < 2:
            c.create_text(w / 2, h / 2, text='ожидание данных… (нажмите «Начать суточный мониторинг»)',
                          fill='#94a3b8', font=('Segoe UI', 10))
            return
        max_batch = max(b for _, _, b in data) or 1
        c.create_text(w - pr + 6, pt + 4, text=fnum(max_batch), anchor='w', fill='#2563eb',
                      font=('Segoe UI', 8))
        n = len(data)

        def xy(i, v, vmax):
            return pl + gw * i / (n - 1), pt + gh * (1 - min(v, vmax) / vmax)

        pts_cpu, pts_batch = [], []
        for i, (_, cpu, batch) in enumerate(data):
            pts_cpu.append(xy(i, cpu, 100))
            pts_batch.append(xy(i, batch, max_batch))
        if len(pts_cpu) > 1:
            c.create_line(*sum([[x, y] for x, y in pts_batch], []), fill='#2563eb', width=1)
            c.create_line(*sum([[x, y] for x, y in pts_cpu], []), fill='#dc2626', width=1)
            x0, y0 = pts_cpu[-1]
            c.create_oval(x0 - 3, y0 - 3, x0 + 3, y0 + 3, fill='#dc2626', outline='')
        c.create_text(pl, h - 6, text=data[0][0].strftime('%H:%M'), anchor='w', fill='#94a3b8',
                      font=('Segoe UI', 8))
        c.create_text(w - pr, h - 6, text=data[-1][0].strftime('%H:%M'), anchor='e', fill='#94a3b8',
                      font=('Segoe UI', 8))

    # ============================================================== настройки
    def _open_settings(self):
        dlg = tk.Toplevel(self.root)
        dlg.title('Настройки')
        dlg.configure(bg='white', padx=16, pady=14)
        dlg.transient(self.root)
        dlg.grab_set()
        s = self.settings
        rows = []

        def add_row(label, widget):
            r = len(rows)
            tk.Label(dlg, text=label, bg='white').grid(row=r, column=0, sticky='w', pady=4, padx=(0, 12))
            widget.grid(row=r, column=1, sticky='w', pady=4)
            rows.append(widget)

        v_int = tk.IntVar(value=s.slow_query_ms)
        e_slow = ttk.Spinbox(dlg, from_=500, to=600000, increment=500, textvariable=v_int, width=10)
        add_row('Порог «медленного» запроса, мс:', e_slow)

        v_ret = tk.IntVar(value=s.retention_days)
        e_ret = ttk.Spinbox(dlg, from_=1, to=90, textvariable=v_ret, width=10)
        add_row('Хранить историю, дней:', e_ret)

        v_time = tk.StringVar(value=s.daily_report_time)
        e_time = ttk.Entry(dlg, textvariable=v_time, width=10)
        add_row('Время автосуточного отчёта (ЧЧ:ММ):', e_time)

        v_daily = tk.BooleanVar(value=s.daily_report_enabled)
        add_row('Автоотчёт каждый день:', ttk.Checkbutton(dlg, variable=v_daily))

        v_deep = tk.BooleanVar(value=s.deep_index_scan)
        add_row('Глубокий анализ индексов (медленнее):', ttk.Checkbutton(dlg, variable=v_deep))

        v_auto = tk.BooleanVar(value=s.autostart_monitoring)
        add_row('Запускать мониторинг при старте:', ttk.Checkbutton(dlg, variable=v_auto))

        v_enc = tk.BooleanVar(value=s.encrypt)
        add_row('Шифровать подключение (Encrypt):', ttk.Checkbutton(dlg, variable=v_enc))

        v_trust = tk.BooleanVar(value=s.trust_cert)
        add_row('Доверять сертификату сервера:', ttk.Checkbutton(dlg, variable=v_trust))

        def save():
            try:
                s.slow_query_ms = max(500, int(v_int.get()))
                s.retention_days = max(1, int(v_ret.get()))
                hh, mm = v_time.get().split(':')
                int(hh), int(mm)
                s.daily_report_time = v_time.get()
            except Exception:
                messagebox.showerror('Настройки', 'Проверьте значения (время — в формате ЧЧ:ММ).',
                                     parent=dlg)
                return
            s.daily_report_enabled = v_daily.get()
            s.deep_index_scan = v_deep.get()
            s.autostart_monitoring = v_auto.get()
            s.encrypt = v_enc.get()
            s.trust_cert = v_trust.get()
            save_settings(s)
            self.log('Настройки сохранены.')
            dlg.destroy()

        btns = tk.Frame(dlg, bg='white')
        btns.grid(row=len(rows), column=0, columnspan=2, pady=(14, 0))
        ttk.Button(btns, text='Сохранить', command=save, style='Accent.TButton').pack(side='left')
        ttk.Button(btns, text='Отмена', command=dlg.destroy).pack(side='left', padx=8)
        dlg.update_idletasks()
        dlg.geometry('+%d+%d' % (self.root.winfo_x() + 200, self.root.winfo_y() + 150))

    # ================================================================ прочее
    def _on_close(self):
        self.stop_evt.set()
        try:
            self.store.close()
        except Exception:
            pass
        if self.backend:
            try:
                self.backend.close()
            except Exception:
                pass
        self.root.destroy()

    def run(self):
        self.log('MS SQL Monitor %s запущен. Данные: %s' % (APP_VERSION, data_dir()))
        self.log('1) Укажите сервер и подключитесь (или включите демо-режим). '
                 '2) «Суточный мониторинг» собирает статистику. 3) «Отчёт» — подробный HTML.')
        self.root.mainloop()


def run_gui():
    root = tk.Tk()
    if os.name == 'nt':
        try:
            import ctypes
            ctypes.windll.shcore.SetProcessDpiAwareness(1)
        except Exception:
            pass
    MonitorApp(root).run()
