package main

// MS SQL Monitor — мониторинг и диагностика MS SQL Server.
//
// Запуск без аргументов — веб-интерфейс (браузер откроется автоматически).
//   --demo-report [путь]  — сформировать демонстрационный HTML-отчёт и выйти
//   --selftest            — самопроверка всех подсистем на демо-данных
//   --port N              — порт интерфейса (по умолчанию 8437)
//   --host АДРЕС          — адрес прослушивания (по умолчанию 127.0.0.1)
//   --no-browser          — не открывать браузер
//   --version             — версия

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]
	has := func(f string) bool {
		for _, a := range args {
			if a == f || strings.HasPrefix(a, f+"=") {
				return true
			}
		}
		return false
	}
	flagValue := func(f string) (string, bool) {
		for i, a := range args {
			if a == f && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				return args[i+1], true
			}
			if strings.HasPrefix(a, f+"=") {
				return strings.TrimPrefix(a, f+"="), true
			}
		}
		return "", false
	}

	switch {
	case has("--version") || has("-v"):
		fmt.Printf("MS SQL Monitor %s (Go)\n", AppVersion)
	case has("--selftest"):
		os.Exit(selftest())
	case has("--demo-report"):
		path, _ := flagValue("--demo-report")
		os.Exit(demoReport(path))
	default:
		port := 8437
		host := ""
		if v, ok := flagValue("--port"); ok {
			if p, err := strconv.Atoi(v); err == nil {
				port = p
			}
		}
		if v, ok := flagValue("--host"); ok {
			host = v
		}
		os.Exit(runServer(port, host, has("--no-browser")))
	}
}

func runServer(port int, host string, noBrowser bool) int {
	initLogFile()
	settings := LoadSettings()
	store, err := OpenStore(DBPath())
	if err != nil {
		logf("Не удалось открыть хранилище %s: %v", DBPath(), err)
		fmt.Fprintf(os.Stderr, "Не удалось открыть хранилище: %v\n", err)
		return 1
	}
	defer store.Close()
	store.Purge(settings.RetentionDays)

	app := NewApp(settings, store)
	if err := ServeWeb(app, port, host, noBrowser); err != nil {
		logf("Сервер интерфейса: %v", err)
		fmt.Fprintf(os.Stderr, "Ошибка сервера: %v\n", err)
		return 1
	}
	return 0
}

// demoPipeline прогоняет весь конвейер на демо-данных.
func demoPipeline(outPath string) (*Snapshot, *History, *Analysis, int) {
	tmp, _ := os.MkdirTemp("", "mssqlmon_")
	defer os.RemoveAll(tmp)
	store, err := OpenStore(filepath.Join(tmp, "monitor.db"))
	if err != nil {
		panic(err)
	}
	defer store.Close()
	engine := NewDemoEngine(30, 24)
	n, err := GenerateDemoHistory(store, engine, 24, 30)
	if err != nil {
		panic(err)
	}
	coll := NewCollector(engine, Settings{SlowQueryMS: 10000, DeepIndexScan: true, SampleInterval: 30})
	coll.Tick()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	snap := coll.FullSnapshot(ctx)
	hist := store.History(24)
	an := Analyze(snap, &hist)
	if outPath != "" {
		_, err := BuildReport(snap, &hist, an, Settings{}, outPath,
			"Демонстрационный отчёт — SQL Server 2019 (имитация)")
		if err != nil {
			panic(err)
		}
	}
	return snap, &hist, an, n
}

func demoReport(path string) int {
	initLogFile()
	if path == "" {
		path = "demo_report.html"
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(ReportsDir(), path)
	}
	fmt.Println("Генерация демонстрационного отчёта (имитация суток мониторинга)…")
	_, _, an, n := demoPipeline(path)
	fmt.Printf("Готово: %s\n", path)
	fmt.Printf("  замеров за 24 ч: %d\n", n)
	fmt.Printf("  оценка сервера: %d/100 (%s)\n", an.HealthScore, an.Summary)
	cnt := 0
	for _, f := range an.Findings {
		if f.Severity == "critical" {
			cnt++
		}
	}
	fmt.Printf("  причин замедления: %d\n", len(an.Causes))
	_ = cnt
	return 0
}

func selftest() int {
	initLogFile()
	fmt.Println("Самопроверка подсистем MS SQL Monitor (Go)…")
	snap, hist, an, n := demoPipeline("")

	// дополнительные проверки живого пути (демо реализует тот же интерфейс БД)
	ignorable := 0
	for _, w := range hist.Waits {
		if isIgnorableWait(w.Type) {
			ignorable++
		}
	}
	batchSane := 0
	for _, s := range hist.Samples {
		if s.Batch >= 1 && s.Batch <= 3000 {
			batchSane++
		}
	}
	transCorrect := true
	// Transactions/sec должен браться у экземпляра _Total (≈2.4×Batch), а не SalesDb (≈0.8×)
	for _, s := range hist.Samples {
		if s.Batch > 100 && (s.Trans < 1.5*s.Batch || s.Trans > 3.5*s.Batch) {
			transCorrect = false
			break
		}
	}

	checks := []struct {
		name string
		ok   bool
	}{
		{"история за 24 ч (замеров)", n >= 2800},
		{"снимок содержит конфигурацию", len(snap.Config) >= 8},
		{"снимок содержит базы данных", len(snap.Databases) >= 3},
		{"снимок содержит файлы", len(snap.Files) >= 4},
		{"снимок содержит топ запросов", len(snap.TopQueries) >= 3},
		{"найдены недостающие индексы", len(snap.MissingIdx) >= 1},
		{"потребление по базам: память", len(snap.PerDBMem) >= 3},
		{"потребление по базам: ЦП", len(snap.PerDBCPU) >= 3},
		{"история: есть замеры", len(hist.Samples) >= 2800},
		{"история: есть ожидания", len(hist.Waits) > 100},
		{"игнорируемые ожидания отфильтрованы", ignorable == 0},
		{"скорости запросов в разумных пределах", batchSane >= len(hist.Samples)*9/10},
		{"Transactions/sec из _Total (не из БД)", transCorrect},
		{"зафиксированы блокировки", len(hist.Blocking) > 0},
		{"зафиксированы медленные запросы", len(hist.Slow) > 0},
		{"анализ: есть рекомендации", len(an.Findings) >= 10},
		{"анализ: есть причины замедлений", len(an.Causes) >= 3},
		{"анализ: категории ожиданий", len(an.WaitByCat) >= 3},
		{"анализ: health score в границах", an.HealthScore >= 5 && an.HealthScore <= 100},
		{"файловая статистика рассчитывается", filesSane(snap)},
	}
	failed := 0
	for _, c := range checks {
		mark := "OK"
		if !c.ok {
			mark = "FAIL"
			failed++
		}
		fmt.Printf("  [%s] %s\n", mark, c.name)
	}
	if failed > 0 {
		fmt.Printf("ПРОВАЛЕНО проверок: %d\n", failed)
		return 1
	}
	fmt.Printf("Все проверки пройдены. (%d рекомендаций, оценка %d/100)\n",
		len(an.Findings), an.HealthScore)
	return 0
}

// filesSane — файловая статистика заполнена и в разумных пределах
// (значения зависят от времени суток демо-сюжета, поэтому проверяем расчёт,
// а не конкретные «горячие» файлы).
func filesSane(snap *Snapshot) bool {
	if len(snap.Files) < 6 {
		return false
	}
	for _, f := range snap.Files {
		if f.ReadLatencyMS < 0 || f.ReadLatencyMS > 200 || f.WriteLatencyMS < 0 || f.WriteLatencyMS > 200 {
			return false
		}
		if f.ReadsSec < 0 || f.WritesSec < 0 || f.SizeMB <= 0 || f.PhysicalName == "" {
			return false
		}
	}
	return true
}
