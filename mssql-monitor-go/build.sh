#!/bin/sh
# ============================================================================
# Сборка MS SQL Monitor (Go). Linux-тест + Windows EXE с иконкой.
#
# Окружение этой песочницы НЕ сохраняет установленный Go между сессиями
# (а кэш модулей ~/go/pkg/mod после восстановления из снапшота бывает битый).
# Скрипт сам ставит тулчейн и при необходимости использует чистый GOMODCACHE.
#
# Особенности: машине доступно ~2 ГБ RAM, пакет modernc.org/sqlite/lib
# (8–10 МБ исходника одной единицей компиляции) при обычной сборке убивается
# по OOM. Поэтому: -p 1 (без параллелизма) и для sqlite/lib -N -l (без
# оптимизаций — компилятору нужно вдвое меньше памяти, EXE чуть больше).
#
# Использование:  sh build.sh [выходной-путь-exe]
# ============================================================================
set -e
OUT="${1:-/home/user/MSSQLMonitor.exe}"
cd "$(dirname "$0")"

GOROOT_CANDID="$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.8.linux-amd64"
if [ ! -x /tmp/go/bin/go ]; then
  echo ">> скачиваю Go 1.26.8 во /tmp…"
  curl -sL -o /tmp/go.tgz https://go.dev/dl/go1.26.8.linux-amd64.tar.gz
  rm -rf /tmp/go && tar xzf /tmp/go.tgz -C /tmp
fi
export PATH=/tmp/go/bin:$PATH GOTOOLCHAIN=local

# если домашний кэш модулей даёт «no required module provides package» —
# берём чистый /tmp/gomod (сеть доступна, докачается)
if ! go build -o /dev/null . 2>/dev/null; then
  echo ">> домашний кэш модулей недействителен, использую /tmp/gomod"
  export GOMODCACHE=/tmp/gomod
fi

echo ">> проверки и Linux-сборка…"
gofmt -l . || true
go vet ./...
go build -p 1 -o /tmp/mssqlmon .
/tmp/mssqlmon --selftest

echo ">> демо-отчёт…"
/tmp/mssqlmon --demo-report /tmp/go_demo.html

echo ">> Windows EXE (amd64, GUI, иконка+манифест из rsrc_windows_amd64.syso)…"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -p 1 -trimpath -gcflags="modernc.org/sqlite/lib=-N -l" \
  -ldflags "-s -w -H windowsgui" -o "$OUT" .
file "$OUT"
echo ">> Готово: $OUT"
