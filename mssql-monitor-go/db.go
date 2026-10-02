package main

// Подключение к MS SQL Server через официальный драйвер Microsoft (go-mssqldb).
// Чистый Go: на целевой машине НЕ нужен установленный ODBC-драйвер.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/microsoft/go-mssqldb" // регистрация драйвера "sqlserver"
)

// Querier — источник строк для коллектора (реальная БД или демо-движок).
type Querier interface {
	Query(ctx context.Context, sql string) ([]map[string]any, error)
	Close()
	Name() string
}

// DBConn — живое подключение к SQL Server.
type DBConn struct {
	db   *sql.DB
	name string
}

// buildDSN собирает строку подключения в формате go-mssqldb.
func buildDSN(s Settings) string {
	server := s.Server
	if s.Port != 0 && s.Port != 1433 && !strings.Contains(server, `\`) {
		server = fmt.Sprintf("%s:%d", server, s.Port)
	}
	parts := []string{
		"server=" + server,
		"database=" + s.Database,
		fmt.Sprintf("connection timeout=%d", s.ConnectTimeout),
		"app name=" + AppName,
	}
	if s.Auth == "sql" {
		parts = append(parts, "user id="+s.Login, "password="+s.DecodedPassword())
	} // иначе — интегрированная аутентификация Windows (SSPI)
	if s.Encrypt {
		parts = append(parts, "encrypt=true")
		if s.TrustCert {
			parts = append(parts, "TrustServerCertificate=true")
		}
	} else {
		parts = append(parts, "encrypt=disable")
	}
	return strings.Join(parts, ";")
}

// Connect устанавливает подключение и проверяет его.
func Connect(ctx context.Context, s Settings) (*DBConn, error) {
	db, err := sql.Open("sqlserver", buildDSN(s))
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть подключение: %w", err)
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(10 * time.Minute)
	cctx, cancel := context.WithTimeout(ctx, time.Duration(s.ConnectTimeout)*time.Second)
	defer cancel()
	if err := db.PingContext(cctx); err != nil {
		db.Close()
		return nil, describeConnErr(err)
	}
	return &DBConn{db: db, name: "go-mssqldb (" + s.Server + ")"}, nil
}

func describeConnErr(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Login failed"), strings.Contains(msg, "18456"):
		return fmt.Errorf("Ошибка входа: неверный логин или пароль (ошибка 18456).")
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "No connection"),
		strings.Contains(msg, "refused"), strings.Contains(msg, "unable to"),
		strings.Contains(msg, "no such host"), strings.Contains(msg, "Unknown host"):
		return fmt.Errorf("%s", "Не удалось подключиться к серверу. Проверьте имя сервера, порт, "+
			"что SQL Server запущен и разрешены удалённые подключения (TCP/IP). Подробности: "+msg)
	case strings.Contains(msg, "certificate"):
		return fmt.Errorf("%s", "Проблема с SSL-сертификатом: включите «Доверять сертификату сервера» или отключите шифрование. Подробности: "+msg)
	}
	return fmt.Errorf("Ошибка подключения: %s", msg)
}

// Query выполняет SQL и возвращает строки как map с ключами в нижнем регистре.
func (c *DBConn) Query(ctx context.Context, query string) ([]map[string]any, error) {
	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return rowsToMaps(rows)
}

func (c *DBConn) Close()       { c.db.Close() }
func (c *DBConn) Name() string { return c.name }

// TestConnection возвращает строку с версией сервера.
func TestConnection(ctx context.Context, s Settings) (string, error) {
	conn, err := Connect(ctx, s)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	rows, err := conn.Query(ctx, "SELECT @@SERVERNAME AS srv, CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(32)) AS ver")
	if err != nil || len(rows) == 0 {
		return "Подключение успешно", nil
	}
	return fmt.Sprintf("%s — SQL Server %s", sstr(rows[0]["srv"]), sstr(rows[0]["ver"])), nil
}

// rowsToMaps превращает sql.Rows в список map (ключи — в нижнем регистре).
func rowsToMaps(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		scan := make([]any, len(cols))
		for i := range vals {
			scan[i] = &vals[i]
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			key := strings.ToLower(strings.TrimSpace(c))
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			m[key] = v
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
