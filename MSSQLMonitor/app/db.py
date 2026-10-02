# -*- coding: utf-8 -*-
"""Подключение к MS SQL Server: pyodbc (основной) или pymssql (резервный).

Оба движка дают одинаковый интерфейс: backend.query(sql) -> список словарей.
"""
import decimal
import logging
from typing import List, Dict, Any, Optional

log = logging.getLogger("mssql.db")

DRIVER_PRIORITY = [
    "ODBC Driver 18 for SQL Server",
    "ODBC Driver 17 for SQL Server",
    "ODBC Driver 13 for SQL Server",
    "ODBC Driver 13 for SQL",
    "SQL Server Native Client 11.0",
    "SQL Server",
]

ODBC_HELP = ("Не найден ODBC-драйвер для SQL Server. Установите «ODBC Driver 18 for SQL Server»: "
             "https://learn.microsoft.com/sql/connect/odbc/download-odbc-driver-for-sql-server")


class ConnectionError(Exception):
    """Ошибка подключения с человекочитаемым описанием."""


def normalize_rows(columns, rows) -> List[Dict[str, Any]]:
    """Приводит результат запроса к списку словарей с понятными типами."""
    names = [str(c[0]).strip().lower() for c in columns]
    out = []
    for row in rows:
        d = {}
        for name, val in zip(names, row):
            d[name] = _clean(val)
        out.append(d)
    return out


def _clean(val):
    if isinstance(val, decimal.Decimal):
        val = float(val)
        return int(val) if val == int(val) else val
    if isinstance(val, bytes):
        for enc in ("utf-16-le", "utf-8", "cp1251"):
            try:
                return val.decode(enc).rstrip("\x00")
            except Exception:
                continue
        return None
    if isinstance(val, bytearray):
        return _clean(bytes(val))
    return val


class Backend:
    """Абстрактный бэкенд: один метод query()."""
    name = "abstract"

    def query(self, sql: str, params: Optional[tuple] = None) -> List[Dict[str, Any]]:
        raise NotImplementedError

    def close(self):
        pass


class PyodbcBackend(Backend):
    def __init__(self, cnxn, driver):
        self.cn = cnxn
        self.name = "pyodbc (%s)" % driver
        self.driver = driver

    @classmethod
    def connect(cls, s):
        try:
            import pyodbc
        except ImportError as e:
            raise ConnectionError("Модуль pyodbc не установлен: %s" % e)
        drivers = [d for d in pyodbc.drivers() if "SQL Server" in d or "SQLServer" in d]
        driver = next((d for d in DRIVER_PRIORITY if d in drivers), None)
        if driver is None and drivers:
            driver = sorted(drivers)[-1]
        if driver is None:
            raise ConnectionError(ODBC_HELP)
        server = s.server if not s.port or s.port == 1433 else "%s,%d" % (s.server, s.port)
        parts = [
            "DRIVER={%s}" % driver,
            "SERVER=%s" % server,
            "DATABASE=%s" % s.database,
        ]
        if s.auth == "windows":
            parts.append("Trusted_Connection=yes")
        else:
            parts.append("UID=%s" % s.login)
            parts.append("PWD=%s" % s.password)
        if "ODBC Driver 1" in driver:
            parts.append("Encrypt=%s" % ("yes" if s.encrypt else "no"))
            if s.trust_cert:
                parts.append("TrustServerCertificate=yes")
        try:
            cnxn = pyodbc.connect(";".join(parts), timeout=s.connect_timeout)
            cnxn.timeout = max(15, s.connect_timeout)
        except Exception as e:
            raise ConnectionError(_describe(e))
        return cls(cnxn, driver)

    def query(self, sql, params=None):
        cur = self.cn.cursor()
        try:
            cur.execute(sql, params) if params else cur.execute(sql)
            if cur.description:
                rows = cur.fetchall()
                return normalize_rows(cur.description, rows)
            return []
        finally:
            try:
                cur.close()
            except Exception:
                pass

    def close(self):
        try:
            self.cn.close()
        except Exception:
            pass


class PymssqlBackend(Backend):
    def __init__(self, conn):
        self.cn = conn
        self.name = "pymssql"

    @classmethod
    def connect(cls, s):
        try:
            import pymssql
        except ImportError as e:
            raise ConnectionError("Модуль pymssql не установлен: %s" % e)
        kw = dict(server=s.server, port=s.port or 1433, database=s.database,
                  timeout=s.connect_timeout, login_timeout=s.connect_timeout,
                  charset="utf8")
        if s.auth == "windows":
            # FreeTDS попытается использовать доменную аутентификацию
            pass
        else:
            kw.update(user=s.login, password=s.password)
        try:
            conn = pymssql.connect(**kw)
        except Exception as e:
            raise ConnectionError(_describe(e))
        return cls(conn)

    def query(self, sql, params=None):
        cur = self.cn.cursor()
        try:
            cur.execute(sql, params) if params else cur.execute(sql)
            if cur.description:
                rows = cur.fetchall()
                return normalize_rows(cur.description, rows)
            return []
        finally:
            try:
                cur.close()
            except Exception:
                pass

    def close(self):
        try:
            self.cn.close()
        except Exception:
            pass


def _describe(e: Exception) -> str:
    text = str(e)
    if "Login failed" in text or "18456" in text:
        return "Ошибка входа: неверный логин или пароль (ошибка 18456)."
    if "SQL Server does not exist" in text or "Login timeout" in text or \
       "Connection refused" in text or "timed out" in text.lower():
        return ("Не удалось подключиться к серверу. Проверьте имя сервера, порт, "
                "что SQL Server запущен и разрешены удалённые подключения (TCP/IP).")
    if "not associated with a trusted" in text or "trusted connection" in text.lower():
        return ("Ошибка доменной аутентификации. Запустите программу от имени доменной учётной "
                "записи или используйте SQL-аутентификацию.")
    if "SSL" in text and "certificate" in text.lower():
        return "Проблема с SSL-сертификатом: включите «Доверять сертификату сервера» в настройках."
    return "Ошибка подключения: %s" % text


def connect(s) -> Backend:
    """Подключиться, используя pyodbc; при его отсутствии — pymssql."""
    last_err = None
    try:
        return PyodbcBackend.connect(s)
    except ConnectionError as e:
        last_err = e
        log.debug("pyodbc недоступен: %s", e)
    except Exception as e:  # нет libodbc и т.п.
        last_err = ConnectionError(_describe(e))
        log.debug("pyodbc не работает: %s", e)
    try:
        return PymssqlBackend.connect(s)
    except Exception as e:
        raise last_err if last_err else ConnectionError(str(e))


def test_connection(s) -> str:
    """Возвращает строку с версией сервера; бросает ConnectionError при ошибке."""
    b = connect(s)
    try:
        rows = b.query("SELECT @@SERVERNAME AS srv, CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(32)) AS ver")
        if rows:
            return "%s — SQL Server %s" % (rows[0]["srv"], rows[0]["ver"])
        return "Подключение успешно"
    finally:
        b.close()
