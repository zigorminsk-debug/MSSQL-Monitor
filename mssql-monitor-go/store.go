package main

// Хранилище истории мониторинга (SQLite, pure-Go драйвер modernc.org/sqlite).

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const storeSchema = `
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
    victims INTEGER, max_wait_ms REAL, head_login TEXT, head_program TEXT, victim_query TEXT);
CREATE TABLE IF NOT EXISTS slow_queries(ts TEXT, elapsed_ms REAL, cpu_ms REAL,
    db_name TEXT, text TEXT, hash TEXT);
CREATE INDEX IF NOT EXISTS idx_slow_ts ON slow_queries(ts);
CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT);
`

// Store — потокобезопасное хранилище.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(storeSchema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() { s.db.Close() }

// SaveSample сохраняет лёгкий снимок.
func (s *Store) SaveSample(smp Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := smp.TS.Format("2006-01-02 15:04:05")
	p := smp.Points
	g := func(k string) any {
		switch k {
		case "ple":
			return ptrArg(p.PLE)
		case "conn":
			return ptrArg(p.Connections)
		case "blocked":
			return ptrArg(p.Blocked)
		case "grants":
			return ptrArg(p.GrantsPending)
		}
		return nil
	}
	r := smp.Rates
	_, err := s.db.Exec(`INSERT INTO samples(ts, sql_cpu, other_cpu, batch_sec, trans_sec,
        compiles_sec, recompiles_sec, reads_sec, writes_sec, log_flush_sec, lock_waits_sec,
        full_scans_sec, errors_sec, ple, connections, blocked, grants_pending)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ts, smp.CPU.SQL, smp.CPU.Other, r["batch_sec"], r["trans_sec"], r["compilations_sec"],
		r["recompilations_sec"], r["page_reads_sec"], r["page_writes_sec"], r["log_flushes_sec"],
		r["lock_waits_sec"], r["full_scans_sec"], r["errors_sec"],
		g("ple"), g("conn"), g("blocked"), g("grants"))
	if err != nil {
		return err
	}
	for _, w := range smp.WaitsDelta {
		if w.WaitMS > 0.5 {
			s.db.Exec("INSERT INTO waits(ts, wait_type, wait_ms, signal_ms) VALUES (?,?,?,?)",
				ts, w.Type, w.WaitMS, w.SignalMS)
		}
	}
	for _, ch := range smp.Blocking.Chains {
		s.db.Exec(`INSERT INTO blocking(ts, head_session, head_query, victims, max_wait_ms,
            head_login, head_program, victim_query) VALUES (?,?,?,?,?,?,?,?)`,
			ts, ch.Head, ch.HeadQuery, ch.Victims, ch.MaxWaitMS, ch.HeadLogin, ch.HeadProgram, ch.VictimQuery)
	}
	for _, q := range smp.Slow {
		h := hashStr(q.Text[:mathMin(300, len(q.Text))])
		var one int
		if err := s.db.QueryRow("SELECT 1 FROM slow_queries WHERE hash=? AND ts>=? LIMIT 1",
			h, smp.TS.Add(-10*time.Minute).Format("2006-01-02 15:04:05")).Scan(&one); err == sql.ErrNoRows {
			s.db.Exec(`INSERT INTO slow_queries(ts, elapsed_ms, cpu_ms, db_name, text, hash)
                VALUES (?,?,?,?,?,?)`,
				ts, q.ElapsedMS, q.CPUMS, q.DBName, truncStr(q.Text, 1500), h)
		}
	}
	return nil
}

func ptrArg(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func hashStr(s string) string {
	// простой FNV-1a — достаточно для дедупликации
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}

func mathMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// SampleRow — строка истории.
type SampleRow struct {
	TS            time.Time
	SQLCPU, Other float64
	Batch, Trans  float64
	Reads, Writes float64
	Compiles      float64
	Errors        float64
	LogFlush      float64
	PLE           float64
	Connections   float64
	Blocked       float64
	Grants        float64
}

// WaitRow — строка ожиданий истории.
type WaitRow struct {
	TS     time.Time
	Type   string
	WaitMS float64
}

// BlockRow — событие блокировки.
type BlockRow struct {
	TS          time.Time
	HeadSession int64
	HeadQuery   string
	Victims     int
	MaxWaitMS   float64
	HeadLogin   string
	HeadProgram string
	VictimQuery string
}

// SlowRow — медленный запрос из истории.
type SlowRow struct {
	TS        time.Time
	ElapsedMS float64
	CPUMS     float64
	DBName    string
	Text      string
}

// History — данные истории за период.
type History struct {
	Samples  []SampleRow
	Waits    []WaitRow
	Blocking []BlockRow
	Slow     []SlowRow
	Hours    float64
}

func parseTS(s string) time.Time {
	if len(s) >= 19 {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", s[:19], time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

// History читает историю за последние hours часов.
func (s *Store) History(hours float64) History {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-time.Duration(hours * float64(time.Hour))).Format("2006-01-02 15:04:05")
	h := History{Hours: hours, Samples: []SampleRow{}, Waits: []WaitRow{},
		Blocking: []BlockRow{}, Slow: []SlowRow{}}

	rows, err := s.db.Query("SELECT * FROM samples WHERE ts>=? ORDER BY ts", cutoff)
	if err == nil {
		cols, _ := rows.Columns()
		idx := func(name string) int {
			for i, c := range cols {
				if strings.EqualFold(c, name) {
					return i
				}
			}
			return -1
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			scan := make([]any, len(cols))
			for i := range vals {
				scan[i] = &vals[i]
			}
			rows.Scan(scan...)
			get := func(name string) any {
				if i := idx(name); i >= 0 {
					return vals[i]
				}
				return nil
			}
			h.Samples = append(h.Samples, SampleRow{
				TS: parseTS(sstr(get("ts"))), SQLCPU: toF(get("sql_cpu")), Other: toF(get("other_cpu")),
				Batch: toF(get("batch_sec")), Trans: toF(get("trans_sec")),
				Compiles: toF(get("compiles_sec")), Reads: toF(get("reads_sec")),
				Writes: toF(get("writes_sec")), LogFlush: toF(get("log_flush_sec")),
				Errors: toF(get("errors_sec")), PLE: toF(get("ple")),
				Connections: toF(get("connections")), Blocked: toF(get("blocked")),
				Grants: toF(get("grants_pending")),
			})
		}
		rows.Close()
	}

	rows, err = s.db.Query("SELECT ts, wait_type, wait_ms FROM waits WHERE ts>=? ORDER BY ts", cutoff)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ts, wt string
			var ms float64
			rows.Scan(&ts, &wt, &ms)
			h.Waits = append(h.Waits, WaitRow{TS: parseTS(ts), Type: wt, WaitMS: ms})
		}
	}

	rows, err = s.db.Query("SELECT ts, head_session, head_query, victims, max_wait_ms, head_login, head_program, victim_query "+
		"FROM blocking WHERE ts>=? ORDER BY ts DESC LIMIT 200", cutoff)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ts string
			var b BlockRow
			rows.Scan(&ts, &b.HeadSession, &b.HeadQuery, &b.Victims, &b.MaxWaitMS, &b.HeadLogin, &b.HeadProgram, &b.VictimQuery)
			b.TS = parseTS(ts)
			h.Blocking = append(h.Blocking, b)
		}
	}

	rows, err = s.db.Query("SELECT ts, elapsed_ms, cpu_ms, db_name, text FROM slow_queries "+
		"WHERE ts>=? ORDER BY elapsed_ms DESC LIMIT 100", cutoff)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ts string
			var q SlowRow
			rows.Scan(&ts, &q.ElapsedMS, &q.CPUMS, &q.DBName, &q.Text)
			q.TS = parseTS(ts)
			h.Slow = append(h.Slow, q)
		}
	}
	return h
}

func (s *Store) SampleCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM samples").Scan(&n)
	return n
}

func (s *Store) GetMeta(key, def string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v string
	if err := s.db.QueryRow("SELECT v FROM meta WHERE k=?", key).Scan(&v); err != nil {
		return def
	}
	return v
}

func (s *Store) SetMeta(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.Exec("INSERT OR REPLACE INTO meta(k, v) VALUES (?,?)", key, value)
}

func (s *Store) Purge(days int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02 15:04:05")
	for _, t := range []string{"samples", "waits", "blocking", "slow_queries"} {
		s.db.Exec("DELETE FROM "+t+" WHERE ts<?", cutoff)
	}
}

// GenerateDemoHistory заполняет хранилище виртуальными сутками мониторинга.
func GenerateDemoHistory(store *Store, engine *DemoEngine, hours int, intervalSec int) (int, error) {
	engine.mu.Lock()
	now := time.Now()
	n := int(float64(hours) * 3600 / float64(intervalSec))
	engine.sim = now.Add(-time.Duration(n*intervalSec) * time.Second)
	engine.lastT = engine.sim
	engine.mu.Unlock()
	coll := NewCollector(engine, Settings{SlowQueryMS: 10000, DeepIndexScan: true, SampleInterval: intervalSec})
	count := 0
	var err error
	for i := 0; i < n; i++ {
		engine.Tick()
		smp := coll.LightSample(defaultCtx())
		if err = store.SaveSample(smp); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func defaultCtx() context.Context { return context.TODO() }
