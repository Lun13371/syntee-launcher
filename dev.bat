@echo off
chcp 65001 >nul 2>&1
title Syntee Launcher - DEV Build
color 0A

echo.
echo  ============================================================
echo             SYNTEE LAUNCHER - DEV BUILD
echo  ============================================================
echo.

set LAUNCHER_DIR=C:\Projects\syntee-launcher
set CLIENT_DIR=C:\Projects\SynteeHelper

REM Default dev gateway URL - can be overridden by SYNTEE_GATEWAY_URL env var
if "%SYNTEE_GATEWAY_URL%"=="" (
    set GATEWAY_URL=http://localhost:8000/api/public/updates
) else (
    set GATEWAY_URL=%SYNTEE_GATEWAY_URL%
)

echo  Gateway URL: %GATEWAY_URL%
echo.
echo  To override, set env var:
echo     set SYNTEE_GATEWAY_URL=http://192.168.1.100:8000/api/public/updates
echo.

echo  ------------------------------------------------------------
echo  Step 1: Build launcher (with embedded gateway URL)
echo  ------------------------------------------------------------
echo.

pushd "%LAUNCHER_DIR%"

go build -ldflags="-s -w -H windowsgui -X github.com/Lun13371/syntee-launcher/internal/config.DefaultUpdateServer=%GATEWAY_URL%" -o syntee-launcher.exe .
if errorlevel 1 (
    echo  [ERROR] Build failed!
    popd
    pause
    exit /b 1
)
echo  [OK] syntee-launcher.exe built

echo.
echo  ------------------------------------------------------------
echo  Step 2: Ensure install dir + config
echo  ------------------------------------------------------------
echo.

REM Install dir: %LOCALAPPDATA%\Programs\Syntee Helper
set INSTALL_DIR=%LOCALAPPDATA%\Programs\Syntee Helper
mkdir "%INSTALL_DIR%" 2>nul

REM Write launcher.json to install dir
(
echo {
echo   "update_server": "%GATEWAY_URL%",
echo   "channel": "stable",
echo   "current_version": "0.0.0",
echo   "pinned_version": null,
echo   "bad_versions": [],
echo   "crash_count": 0,
echo   "max_crash_retries": 3,
echo   "check_timeout_sec": 5,
echo   "download_timeout_sec": 0,
echo   "client_exe": "Syntee Helper.exe",
echo   "ready_marker": ".ready"
echo }
) > "%INSTALL_DIR%\launcher.json"

echo  [OK] Config written to %INSTALL_DIR%\launcher.json

echo.
echo  ------------------------------------------------------------
echo  Step 3: Copy to client resources + gateway
echo  ------------------------------------------------------------
echo.

copy /Y syntee-launcher.exe "%CLIENT_DIR%\resources\syntee-launcher.exe" >nul
echo  [OK] Copied to client resources

set GATEWAY_DIR=C:\Projects\Syntee-Gateway
if exist "%GATEWAY_DIR%\backend\data\launcher" (
    copy /Y syntee-launcher.exe "%GATEWAY_DIR%\backend\data\launcher\syntee-launcher.exe" >nul
    echo  [OK] Copied to gateway data\launcher\
) else (
    mkdir "%GATEWAY_DIR%\backend\data\launcher" 2>nul
    copy /Y syntee-launcher.exe "%GATEWAY_DIR%\backend\data\launcher\syntee-launcher.exe" >nul
    echo  [OK] Created gateway data\launcher\ and copied
)

popd

echo.
echo  ============================================================
echo  DEV BUILD COMPLETE
echo  ============================================================
echo.
echo  Launcher:    %LAUNCHER_DIR%\syntee-launcher.exe
echo  Install dir: %INSTALL_DIR%
echo  Gateway:     %GATEWAY_URL%
echo.
echo  Launching launcher...
echo  Close the launcher window or press Ctrl+C to stop.
echo.

"%LAUNCHER_DIR%\syntee-launcher.exe"

echo.
echo  Launcher exited. Press any key to close.
pause >nul
