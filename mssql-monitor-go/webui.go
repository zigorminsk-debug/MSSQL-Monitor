package main

// Встроенный веб-интерфейс: локальный HTTP-сервер + страница-приложение.
// exe запускает сервер на 127.0.0.1 и открывает браузер — это и есть GUI.

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

// ---------------------------------------------------------------------------
//
//	Журнал (кольцевой буфер + файл)
//
// ---------------------------------------------------------------------------
var (
	logMu   sync.Mutex
	logRing []string
	logFile *os.File
)

func initLogFile() {
	f, err := os.OpenFile(LogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		logFile = f
	}
}

func logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg)
	logMu.Lock()
	defer logMu.Unlock()
	logRing = append(logRing, line)
	if len(logRing) > 600 {
		logRing = logRing[len(logRing)-600:]
	}
	if logFile != nil {
		fmt.Fprintln(logFile, line)
	}
	fmt.Println(line)
}

func logTail() string {
	logMu.Lock()
	defer logMu.Unlock()
	return strings.Join(logRing, "\n")
}

// ---------------------------------------------------------------------------
//  Состояние приложения
// ---------------------------------------------------------------------------

// SeriesPoint — точка живого графика.
type SeriesPoint struct {
	TS    time.Time `json:"ts"`
	CPU   float64   `json:"cpu"`
	Batch float64   `json:"batch"`
}

// App — состояние программы.
type App struct {
	mu        sync.Mutex
	collectMu sync.Mutex

	settings   Settings
	store      *Store
	querier    Querier
	collector  *Collector
	demo       bool
	monitoring bool
	monitorCh  chan struct{}
	monitorSt  time.Time

	lastSample *Sample
	series     []SeriesPoint
	analysis   *Analysis
	generating bool
	status     string
	connInfo   string
	errStreak  int
}

func NewApp(settings Settings, store *Store) *App {
	return &App{settings: settings, store: store}
}

func (a *App) log(format string, args ...any) { logf(format, args...) }

func (a *App) setConn(q Querier, demo bool, info string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.querier != nil {
		a.querier.Close()
	}
	a.querier = q
	a.demo = demo
	a.collector = NewCollector(q, a.settings)
	a.connInfo = info
	a.errStreak = 0
	a.series = nil
	a.lastSample = nil
}

// ---------------------------------------------------------------- мониторинг
func (a *App) StartMonitoring() error {
	a.mu.Lock()
	if a.collector == nil {
		a.mu.Unlock()
		return fmt.Errorf("сначала подключитесь к серверу или включите демо-режим")
	}
	if a.monitoring {
		a.mu.Unlock()
		return nil
	}
	a.monitoring = true
	a.monitorCh = make(chan struct{})
	a.monitorSt = time.Now()
	interval := a.settings.SampleInterval
	a.mu.Unlock()

	a.log("Мониторинг запущен (интервал %d сек). Показания пишутся в историю.", interval)
	a.takeSample()
	go a.monitorLoop()
	go a.dailyLoop()
	return nil
}

func (a *App) StopMonitoring() {
	a.mu.Lock()
	ch := a.monitorCh
	a.monitoring = false
	a.monitorCh = nil
	a.mu.Unlock()
	if ch != nil {
		close(ch)
	}
	a.log("Мониторинг остановлен.")
}

func (a *App) monitorLoop() {
	for {
		a.mu.Lock()
		if !a.monitoring || a.monitorCh == nil {
			a.mu.Unlock()
			return
		}
		interval := a.settings.SampleInterval
		ch := a.monitorCh
		a.mu.Unlock()
		select {
		case <-ch:
			return
		case <-time.After(time.Duration(interval) * time.Second):
		}
		a.takeSample()
	}
}

func (a *App) takeSample() {
	a.collectMu.Lock()
	defer a.collectMu.Unlock()
	a.mu.Lock()
	coll := a.collector
	demo := a.demo
	a.mu.Unlock()
	if coll == nil {
		return
	}
	if demo {
		coll.Tick() // продвигаем виртуальное время демо
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	smp := coll.LightSample(ctx)
	if smp.Points.PLE == nil && smp.Points.Connections == nil && !demo {
		// ни один счётчик не получен — сервер недоступен
		a.mu.Lock()
		a.errStreak++
		stop := a.errStreak >= 5
		a.status = "Сервер не отвечает (проверьте подключение)"
		a.mu.Unlock()
		a.log("ОШИБКА: сервер не отвечает (%d подряд)", a.errStreak)
		if stop {
			a.log("Мониторинг остановлен: сервер недоступен.")
			a.StopMonitoring()
		}
		return
	}
	a.store.SaveSample(smp)
	a.mu.Lock()
	a.errStreak = 0
	a.lastSample = &smp
	a.series = append(a.series, SeriesPoint{TS: smp.TS, CPU: smp.CPU.SQL, Batch: smp.Rates["batch_sec"]})
	if len(a.series) > 400 {
		a.series = a.series[len(a.series)-400:]
	}
	a.status = "Последний замер: " + smp.TS.Format("02.01 15:04:05")
	a.mu.Unlock()
}

func (a *App) dailyLoop() {
	for {
		a.mu.Lock()
		if !a.monitoring || a.monitorCh == nil {
			a.mu.Unlock()
			return
		}
		ch := a.monitorCh
		s := a.settings
		started := a.monitorSt
		a.mu.Unlock()
		select {
		case <-ch:
			return
		case <-time.After(60 * time.Second):
		}
		if !s.DailyReportOn {
			continue
		}
		hh, mm := 8, 0
		if parts := strings.Split(s.DailyReportTime, ":"); len(parts) == 2 {
			if v, err := strconv.Atoi(parts[0]); err == nil {
				hh = v
			}
			if v, err := strconv.Atoi(parts[1]); err == nil {
				mm = v
			}
		}
		now := time.Now()
		target := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
		today := now.Format("2006-01-02")
		// отчёт формируется, только если настроенное время настало ВО ВРЕМЯ
		// мониторинга и накоплено >= 15 минут данных
		if a.store.GetMeta("last_daily", "") != today &&
			!started.After(target) && now.After(target) &&
			now.Sub(started) >= 15*time.Minute {
			a.store.SetMeta("last_daily", today)
			a.log("Автоматическая генерация суточного отчёта…")
			go a.generateReport(true)
		}
	}
}

// ------------------------------------------------------------------- отчёты
func (a *App) generateReport(daily bool) {
	a.mu.Lock()
	if a.generating {
		a.mu.Unlock()
		return
	}
	a.generating = true
	a.mu.Unlock()

	prefix := "report"
	title := "Полный отчёт (снимок)"
	if daily {
		prefix = "daily"
		title = "Суточный отчёт мониторинга"
	}
	name := fmt.Sprintf("%s_%s.html", prefix, time.Now().Format("20060102_150405"))
	out := filepath.Join(ReportsDir(), name)

	defer func() {
		a.mu.Lock()
		a.generating = false
		a.mu.Unlock()
	}()

	a.collectMu.Lock()
	defer a.collectMu.Unlock()
	a.mu.Lock()
	coll := a.collector
	demo := a.demo
	server := a.settings.Server
	a.mu.Unlock()
	if coll == nil {
		a.log("ОТЧЁТ: не подключено")
		return
	}
	if demo {
		coll.Tick()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	snap := coll.FullSnapshot(ctx)
	hist := a.store.History(24)
	an := Analyze(snap, &hist)
	if demo {
		title += " — демо-сервер"
	} else {
		title += " — " + server
	}
	if _, err := BuildReport(snap, &hist, an, a.settings, out, title); err != nil {
		a.log("ОТЧЁТ: ошибка записи: %v", err)
		return
	}
	a.mu.Lock()
	a.analysis = an
	a.mu.Unlock()
	a.log("Отчёт сохранён: %s (оценка %d/100, критично: %d, предупреждений: %d)",
		out, an.HealthScore, countSev(an, "critical"), countSev(an, "warning"))
}

// ------------------------------------------------------------------- API
type stateDTO struct {
	Connected  bool           `json:"connected"`
	Demo       bool           `json:"demo"`
	Monitoring bool           `json:"monitoring"`
	ConnInfo   string         `json:"conn_info"`
	Status     string         `json:"status"`
	Generating bool           `json:"generating"`
	Interval   int            `json:"interval"`
	Samples    int            `json:"samples"`
	Cards      map[string]any `json:"cards"`
	Series     []SeriesPoint  `json:"series"`
	Health     int            `json:"health"`
	Summary    string         `json:"summary"`
	Findings   []Finding      `json:"findings"`
	Causes     []Cause        `json:"causes"`
	Reports    []string       `json:"reports"`
	Log        string         `json:"log"`
	Settings   Settings       `json:"settings"`
}

func (a *App) stateDTO() stateDTO {
	a.mu.Lock()
	defer a.mu.Unlock()
	dto := stateDTO{
		Connected: a.collector != nil, Demo: a.demo, Monitoring: a.monitoring,
		ConnInfo: a.connInfo, Status: a.status, Generating: a.generating,
		Interval: a.settings.SampleInterval, Series: a.series,
		Settings: a.settings, Findings: []Finding{}, Causes: []Cause{},
		Reports: []string{}, Cards: map[string]any{}, Log: "",
	}
	dto.Samples = a.store.SampleCount()
	if a.lastSample != nil {
		p := a.lastSample.Points
		dto.Cards["cpu"] = fmtNum(a.lastSample.CPU.SQL)
		dto.Cards["batch"] = fmtNum(a.lastSample.Rates["batch_sec"])
		dto.Cards["conn"] = fmtNumD(p.Connections, "—")
		dto.Cards["ple"] = fmtNumD(p.PLE, "—")
		dto.Cards["blocked"] = fmtNumD(p.Blocked, "0")
		dto.Cards["ple_warn"] = p.PLE != nil && *p.PLE < 300
		dto.Cards["cpu_warn"] = a.lastSample.CPU.SQL > 80
		dto.Cards["blocked_warn"] = p.Blocked != nil && *p.Blocked > 0
	}
	if a.analysis != nil {
		dto.Health = a.analysis.HealthScore
		dto.Summary = a.analysis.Summary
		dto.Findings = a.analysis.Findings
		dto.Causes = a.analysis.Causes
	}
	dto.Log = logTail()
	files, _ := filepath.Glob(filepath.Join(ReportsDir(), "*.html"))
	sort.Slice(files, func(i, j int) bool {
		fi, fj := statMTime(files[i]), statMTime(files[j])
		return fi.After(fj)
	})
	for i, f := range files {
		if i >= 100 {
			break
		}
		dto.Reports = append(dto.Reports, filepath.Base(f))
	}
	return dto
}

func statMTime(p string) time.Time {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// ServeWeb запускает HTTP-сервер интерфейса и блокируется.
// host="" означает безопасный по умолчанию 127.0.0.1.
func ServeWeb(a *App, port int, host string, noBrowser bool) error {
	if host == "" {
		host = "127.0.0.1"
	}
	initLogFile()
	mux := http.NewServeMux()

	serveAsset := func(name, ctype string) http.HandlerFunc {
		data, err := webFS.ReadFile("web/" + name)
		if err != nil {
			panic(err)
		}
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ctype+"; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(data)
		}
	}
	mux.HandleFunc("/", serveAsset("index.html", "text/html"))
	mux.HandleFunc("/app.js", serveAsset("app.js", "application/javascript"))
	mux.HandleFunc("/style.css", serveAsset("style.css", "text/css"))

	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.stateDTO())
	})

	mux.HandleFunc("/api/connect", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Server, Auth, Login, Password, Database string
			Port                                    int
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "некорректный запрос", 400)
			return
		}
		a.mu.Lock()
		s := a.settings
		a.mu.Unlock()
		s.Server = strings.TrimSpace(req.Server)
		if s.Server == "" {
			s.Server = "localhost"
		}
		s.Port = req.Port
		if req.Auth == "sql" {
			s.Auth = "sql"
		} else {
			s.Auth = "windows"
		}
		s.Login = req.Login
		s.Database = orDefault(req.Database, "master")
		if req.Password != "" {
			s.Password = encodePass(req.Password)
		} else if req.Auth != "sql" {
			s.Password = ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.ConnectTimeout+5)*time.Second)
		defer cancel()
		conn, err := Connect(ctx, s)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		s.Save()
		a.mu.Lock()
		a.settings = s
		a.mu.Unlock()
		a.setConn(conn, false, conn.Name())
		a.log("Подключено: %s (%s)", s.Server, conn.Name())
		writeJSON(w, map[string]any{"ok": true, "info": conn.Name()})
	})

	mux.HandleFunc("/api/test", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Server, Auth, Login, Password, Database string
			Port                                    int
		}
		json.NewDecoder(r.Body).Decode(&req)
		s := Settings{Server: orDefault(strings.TrimSpace(req.Server), "localhost"), Port: req.Port,
			Auth: req.Auth, Login: req.Login, Database: orDefault(req.Database, "master"),
			TrustCert: true, ConnectTimeout: 15}
		if req.Password != "" {
			s.Password = encodePass(req.Password)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		info, err := TestConnection(ctx, s)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "info": info})
	})

	mux.HandleFunc("/api/demo", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		interval := a.settings.SampleInterval
		a.mu.Unlock()
		engine := NewDemoEngine(interval, 0)
		a.setConn(engine, true, engine.Name())
		a.log("Включён демо-режим: все данные имитируются. Сгенерируйте отчёт, чтобы посмотреть пример анализа.")
		writeJSON(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/monitor/start", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Interval int }
		json.NewDecoder(r.Body).Decode(&req)
		a.mu.Lock()
		if req.Interval >= 10 {
			a.settings.SampleInterval = req.Interval
		}
		a.settings.Save()
		a.mu.Unlock()
		if err := a.StartMonitoring(); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/monitor/stop", func(w http.ResponseWriter, r *http.Request) {
		a.StopMonitoring()
		writeJSON(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/report", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Daily bool }
		json.NewDecoder(r.Body).Decode(&req)
		a.mu.Lock()
		ready := a.collector != nil && !a.generating
		a.mu.Unlock()
		if !ready {
			writeJSON(w, map[string]any{"ok": false, "error": "сначала подключитесь (или идёт формирование)"})
			return
		}
		go a.generateReport(req.Daily)
		writeJSON(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var s Settings
			if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
				http.Error(w, "некорректный запрос", 400)
				return
			}
			s.normalize()
			a.mu.Lock()
			// подключение не трогаем
			conn := a.settings
			s.Server, s.Port, s.Auth, s.Login, s.Password, s.Database = conn.Server, conn.Port, conn.Auth, conn.Login, conn.Password, conn.Database
			a.settings = s
			if a.collector != nil {
				a.collector.settings = s
			}
			a.mu.Unlock()
			s.Save()
			a.log("Настройки сохранены.")
			writeJSON(w, map[string]any{"ok": true})
			return
		}
		a.mu.Lock()
		s := a.settings
		a.mu.Unlock()
		s.Password = ""
		writeJSON(w, s)
	})

	mux.HandleFunc("/reports/", func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		if !strings.HasSuffix(name, ".html") {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(ReportsDir(), name)
		if _, err := os.Stat(path); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeFile(w, r, path)
	})

	// --- запуск сервера -----------------------------------------------------
	var ln net.Listener
	var err error
	for p := port; p < port+25; p++ {
		ln, err = net.Listen("tcp", fmt.Sprintf("%s:%d", host, p))
		if err == nil {
			port = p
			break
		}
	}
	if ln == nil {
		return fmt.Errorf("не удалось занять порт %d: %w", port, err)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	a.log("Интерфейс: %s (данные: %s)", url, DataDir())
	a.log("1) Подключитесь к серверу (или включите демо-режим). 2) «Суточный мониторинг» собирает статистику. 3) «Отчёт» — подробный HTML.")
	if !noBrowser {
		go func() {
			time.Sleep(400 * time.Millisecond)
			openBrowser(url)
		}()
	}
	srv := &http.Server{Handler: mux}
	return srv.Serve(ln)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func encodePass(p string) string {
	// base64-маскировка пароля в конфиге (как в config.go при сохранении)
	return base64.StdEncoding.EncodeToString([]byte(p))
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		logf("Не удалось открыть браузер: %v (откройте %s вручную)", err, url)
	}
}
