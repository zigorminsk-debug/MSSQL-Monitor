package main

// Настройки приложения и пути к данным (портативная папка data/ или %LOCALAPPDATA%).

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	AppName    = "MSSQLMonitor"
	AppTitle   = "MS SQL Monitor — мониторинг и диагностика MS SQL Server"
	AppVersion = "1.0.0"
)

// Settings — настройки программы.
type Settings struct {
	Server           string `json:"server"`
	Port             int    `json:"port"`
	Auth             string `json:"auth"` // windows | sql
	Login            string `json:"login"`
	Password         string `json:"password"`
	Database         string `json:"database"`
	Encrypt          bool   `json:"encrypt"`
	TrustCert        bool   `json:"trust_cert"`
	ConnectTimeout   int    `json:"connect_timeout"`
	SampleInterval   int    `json:"sample_interval"`
	SlowQueryMS      int    `json:"slow_query_ms"`
	RetentionDays    int    `json:"retention_days"`
	DailyReportOn    bool   `json:"daily_report_enabled"`
	DailyReportTime  string `json:"daily_report_time"`
	DeepIndexScan    bool   `json:"deep_index_scan"`
	AutostartMonitor bool   `json:"autostart_monitoring"`
}

func NewSettings() Settings {
	return Settings{
		Server: "localhost", Port: 1433, Auth: "windows", Database: "master",
		TrustCert: true, ConnectTimeout: 15, SampleInterval: 30,
		SlowQueryMS: 10000, RetentionDays: 7, DailyReportOn: true,
		DailyReportTime: "08:00", DeepIndexScan: true,
	}
}

func (s *Settings) normalize() {
	if s.Server == "" {
		s.Server = "localhost"
	}
	if s.Port <= 0 {
		s.Port = 1433
	}
	if s.Auth != "sql" {
		s.Auth = "windows"
	}
	if s.Database == "" {
		s.Database = "master"
	}
	if s.SampleInterval < 10 {
		s.SampleInterval = 10
	}
	if s.SlowQueryMS < 500 {
		s.SlowQueryMS = 500
	}
	if s.RetentionDays < 1 {
		s.RetentionDays = 7
	}
	if s.DailyReportTime == "" {
		s.DailyReportTime = "08:00"
	}
	if s.ConnectTimeout < 5 {
		s.ConnectTimeout = 15
	}
}

func appRoot() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	wd, _ := os.Getwd()
	return wd
}

func writableDir(path string) bool {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(path, ".write_test")
	if err := os.WriteFile(probe, []byte("1"), 0o644); err != nil {
		return false
	}
	os.Remove(probe)
	return true
}

// DataDir возвращает data/ рядом с программой (если доступна) или профиль пользователя.
func DataDir() string {
	root := appRoot()
	if !strings.HasPrefix(root, os.TempDir()) {
		cand := filepath.Join(root, "data")
		if writableDir(cand) {
			return cand
		}
	}
	var base string
	if runtime.GOOS == "windows" {
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = os.Getenv("USERPROFILE")
		}
	} else {
		base = os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".local", "share")
		}
	}
	if base == "" {
		base = root
	}
	p := filepath.Join(base, AppName)
	os.MkdirAll(p, 0o755)
	return p
}

func ReportsDir() string {
	p := filepath.Join(DataDir(), "reports")
	os.MkdirAll(p, 0o755)
	return p
}

func DBPath() string     { return filepath.Join(DataDir(), "monitor.db") }
func ConfigPath() string { return filepath.Join(DataDir(), "config.json") }
func LogPath() string    { return filepath.Join(DataDir(), "app.log") }

func LoadSettings() Settings {
	s := NewSettings()
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return s
	}
	var loaded Settings
	if json.Unmarshal(data, &loaded) == nil {
		loaded.normalize()
		return loaded
	}
	return s
}

func (s Settings) Save() {
	s.normalize()
	// пароль храним в base64 (лёгкая маскировка, не защита)
	type dump struct {
		Settings
		Password string `json:"password"`
	}
	d := dump{Settings: s, Password: ""}
	if s.Password != "" {
		d.Password = base64.StdEncoding.EncodeToString([]byte(s.Password))
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return
	}
	os.MkdirAll(DataDir(), 0o755)
	os.WriteFile(ConfigPath(), data, 0o644)
}

func (s Settings) DecodedPassword() string {
	if s.Password == "" {
		return ""
	}
	if raw, err := base64.StdEncoding.DecodeString(s.Password); err == nil {
		return string(raw)
	}
	return s.Password
}
