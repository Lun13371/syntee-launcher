@echo off
chcp 65001 >nul 2>&1
title Syntee Launcher - PRODUCTION Build
color 0E

echo.
echo  ============================================================
echo           SYNTEE LAUNCHER - PRODUCTION BUILD
echo  ============================================================
echo.

set LAUNCHER_DIR=C:\Projects\syntee-launcher
set CLIENT_DIR=C:\Projects\SynteeHelper

REM Production gateway URL - can be overridden by SYNTEE_GATEWAY_URL env var
if "%SYNTEE_GATEWAY_URL%"=="" (
    set GATEWAY_URL=https://syntee.fun/api/public/updates
) else (
    set GATEWAY_URL=%SYNTEE_GATEWAY_URL%
)

echo  Gateway URL: %GATEWAY_URL%
echo.
echo  To override, set env var:
echo     set SYNTEE_GATEWAY_URL=https://staging.syntee.dev/api/public/updates
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
echo  Step 2: Copy to client resources + gateway
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
echo  ------------------------------------------------------------
echo  Step 3: Sync local launcher.json (so dev tests use this URL)
echo  ------------------------------------------------------------
echo.

REM If a previous install left a launcher.json with a stale update_server
REM (e.g. from dev.bat -> localhost:8000), the launcher reads it and IGNORES
REM the freshly-baked DefaultUpdateServer (applyDefaults only fills an empty
REM field). Rewrite the file so the new URL takes effect immediately.
set INSTALL_DIR=%LOCALAPPDATA%\Programs\Syntee Helper
if exist "%INSTALL_DIR%\launcher.json" (
  echo  Updating existing launcher.json -> %GATEWAY_URL%
  powershell -NoProfile -Command "$p='%INSTALL_DIR%\launcher.json'; $j=Get-Content $p -Raw | ConvertFrom-Json; $j.update_server='%GATEWAY_URL%'; $j | ConvertTo-Json -Depth 5 | Set-Content $p -Encoding UTF8"
  if errorlevel 1 (
    echo  [WARNING] Could not update launcher.json - delete it manually:
    echo          %INSTALL_DIR%\launcher.json
  ) else (
    echo  [OK] launcher.json synced
  )
) else (
  echo  launcher.json not present yet - launcher will create it on first run
)

echo.
echo  ============================================================
echo  PRODUCTION BUILD COMPLETE
echo  ============================================================
echo.
echo  Artifact:   %LAUNCHER_DIR%\syntee-launcher.exe
echo  Gateway:    %GATEWAY_URL%
echo.
echo  This launcher.exe is what users download from the website.
echo  On first run it installs to:
echo    %%LOCALAPPDATA%%\Programs\Syntee Helper\
echo  and downloads the client from: %GATEWAY_URL%
echo.
echo  Press any key to exit.
pause >nul
