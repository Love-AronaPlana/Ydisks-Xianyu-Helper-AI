@echo off
setlocal EnableExtensions

rem Use the directory containing this file as the project root.
cd /d "%~dp0"

set "APP_ADDR=0.0.0.0:59188"
set "DATA_DIR=%~dp0data"
set "DB_PATH=%DATA_DIR%\xianyu_data.db"
set "LOG_DIR=%DATA_DIR%\logs"
set "SERVER_EXE=%~dp0xianyu-server.exe"
set "XIANYU_LOG_DIR=%LOG_DIR%"

where node >nul 2>nul
if errorlevel 1 (
  echo [ERROR] Node.js was not found. Install Node.js 24 and try again.
  exit /b 1
)

where npm >nul 2>nul
if errorlevel 1 (
  echo [ERROR] npm was not found. Install Node.js 24 and try again.
  exit /b 1
)

where go >nul 2>nul
if errorlevel 1 (
  echo [ERROR] Go was not found. Install Go 1.26.4 or newer and try again.
  exit /b 1
)

if not exist "%DATA_DIR%" mkdir "%DATA_DIR%"
if not exist "%LOG_DIR%" mkdir "%LOG_DIR%"

if not exist "%~dp0frontend\node_modules" (
  echo [1/4] Installing frontend dependencies...
  call npm ci --prefix frontend
  if errorlevel 1 (
    echo [ERROR] Frontend dependency installation failed.
    exit /b 1
  )
)

echo [2/4] Building frontend...
call npm run build --prefix frontend
if errorlevel 1 (
  echo [ERROR] Frontend build failed.
  exit /b 1
)

echo [3/4] Building server...
go build -o "%SERVER_EXE%" ./cmd/server
if errorlevel 1 (
  echo [ERROR] Server build failed.
  exit /b 1
)

echo [4/4] Starting server...
echo Open http://127.0.0.1:59188 locally or use this LAN address: %APP_ADDR%
echo Database: %DB_PATH%
echo Log file: %LOG_DIR%\server.log
echo Press Ctrl+C to stop. Existing data will not be deleted.
echo.

"%SERVER_EXE%" -addr "%APP_ADDR%" -db "%DB_PATH%"
set "EXIT_CODE=%ERRORLEVEL%"

echo.
echo Server exited with code %EXIT_CODE%.
pause
exit /b %EXIT_CODE%
