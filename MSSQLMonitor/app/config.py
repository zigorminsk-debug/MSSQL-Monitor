# -*- coding: utf-8 -*-
"""Настройки приложения, пути к данным (портативная папка данных или %LOCALAPPDATA%)."""
import base64
import json
import os
import sys
from dataclasses import dataclass, asdict, fields

APP_NAME = "MSSQLMonitor"
APP_TITLE = "MS SQL Monitor — мониторинг и диагностика MS SQL Server"
APP_VERSION = "1.0.0"


def app_root() -> str:
    """Каталог приложения (для EXE — папка с EXE, для скрипта — корень проекта)."""
    if getattr(sys, "frozen", False):  # PyInstaller
        return os.path.dirname(os.path.abspath(sys.executable))
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def _writable(path: str) -> bool:
    try:
        os.makedirs(path, exist_ok=True)
        probe = os.path.join(path, ".write_test")
        with open(probe, "w") as f:
            f.write("1")
        os.remove(probe)
        return True
    except Exception:
        return False


def data_dir() -> str:
    """data/ рядом с программой, если доступна для записи, иначе %LOCALAPPDATA%."""
    root = app_root()
    if not root.startswith(("/tmp", "/var/tmp")):
        cand = os.path.join(root, "data")
        if _writable(cand):
            return cand
    if os.name == "nt":
        base = os.environ.get("LOCALAPPDATA") or os.path.expanduser("~")
    else:
        base = os.path.expanduser("~/.local/share")
    cand = os.path.join(base, APP_NAME)
    os.makedirs(cand, exist_ok=True)
    return cand


def reports_dir() -> str:
    p = os.path.join(data_dir(), "reports")
    os.makedirs(p, exist_ok=True)
    return p


def db_path() -> str:
    return os.path.join(data_dir(), "monitor.db")


def config_path() -> str:
    return os.path.join(data_dir(), "config.json")


def log_path() -> str:
    return os.path.join(data_dir(), "app.log")


# ---------------------------------------------------------------------------
@dataclass
class Settings:
    # Подключение
    server: str = "localhost"
    port: int = 1433
    auth: str = "windows"          # windows | sql
    login: str = ""
    password: str = ""
    database: str = "master"
    encrypt: bool = False          # шифровать подключение (Encrypt=yes)
    trust_cert: bool = True        # TrustServerCertificate=yes
    connect_timeout: int = 15
    # Мониторинг
    sample_interval: int = 30      # сек, интервал съёма показаний
    slow_query_ms: int = 10000     # порог «медленного» запроса
    retention_days: int = 7        # сколько дней хранить историю в SQLite
    # Отчёты
    daily_report_enabled: bool = True
    daily_report_time: str = "08:00"
    deep_index_scan: bool = True   # фрагментация/статистика (тяжёлые запросы)
    autostart_monitoring: bool = False

    # -- (де)сериализация ---------------------------------------------------
    @staticmethod
    def _obfuscate(text: str) -> str:
        return base64.b64encode(text.encode("utf-8")).decode("ascii")

    @staticmethod
    def _deobfuscate(text: str) -> str:
        try:
            return base64.b64decode(text.encode("ascii")).decode("utf-8")
        except Exception:
            return ""

    def to_dict(self) -> dict:
        d = asdict(self)
        d["password"] = self._obfuscate(self.password) if self.password else ""
        return d

    @classmethod
    def from_dict(cls, d: dict) -> "Settings":
        known = {f.name for f in fields(cls)}
        d = {k: v for k, v in d.items() if k in known}
        if d.get("password"):
            d["password"] = cls._deobfuscate(d["password"])
        return cls(**d)

    def save(self) -> None:
        try:
            with open(config_path(), "w", encoding="utf-8") as f:
                json.dump(self.to_dict(), f, ensure_ascii=False, indent=2)
        except Exception:
            pass

    @classmethod
    def load(cls) -> "Settings":
        try:
            with open(config_path(), "r", encoding="utf-8") as f:
                return cls.from_dict(json.load(f))
        except Exception:
            return cls()


def load_settings() -> Settings:
    return Settings.load()


def save_settings(s: Settings) -> None:
    s.save()
