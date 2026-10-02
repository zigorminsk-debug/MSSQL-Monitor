@echo off
rem ============================================================================
rem  Сборка EXE: MS SQL Monitor
rem  Требуется: Windows 8.1/10/11, Python 3.11-3.13 (https://python.org)
rem  Запуск: двойной клик или из консоли. Результат: dist\MSSQLMonitor\
rem ============================================================================
chcp 65001 >nul
cd /d "%~dp0"
setlocal

echo === MS SQL Monitor: сборка EXE ===
echo.

where python >nul 2>nul
if errorlevel 1 (
    echo [ОШИБКА] Python не найден. Установите Python 3.11+ с https://python.org
    echo          При установке отметьте галочку "Add Python to PATH".
    pause & exit /b 1
)

echo [1/5] Создание виртуального окружения...
if not exist .venv (
    python -m venv .venv || (echo [ОШИБКА] Не удалось создать venv & pause & exit /b 1)
)
call .venv\Scripts\activate.bat

echo [2/5] Установка зависимостей...
python -m pip install --upgrade pip -q
pip install -q pyinstaller pyodbc pillow || (echo [ОШИБКА] установка зависимостей & pause & exit /b 1)
pip install -q pymssql 2>nul || echo        [i] pymssql недоступен - не критично (pyodbc достаточно)

echo [3/5] Самопроверка на демо-данных...
python main.py --selftest || (echo [ОШИБКА] самопроверка не пройдена & pause & exit /b 1)

echo [4/5] Генерация иконки...
python make_icon.py || echo        [i] не удалось создать иконку, продолжаю без неё

echo [5/5] PyInstaller...
pyinstaller --clean --noconfirm mssql_monitor.spec || (echo [ОШИБКА] PyInstaller & pause & exit /b 1)

echo.
echo ============================================================
echo  Готово!  EXE:  dist\MSSQLMonitor\MSSQLMonitor.exe
echo  (одна папка - переносится целиком на любой Windows-сервер)
echo ============================================================
if exist dist\MSSQLMonitor\MSSQLMonitor.exe start "" explorer dist\MSSQLMonitor
pause
