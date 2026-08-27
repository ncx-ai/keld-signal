@echo off
setlocal enabledelayedexpansion
rem Keld setup - runs after install. Redeems your one-time setup code (from the Keld
rem download page) for a non-interactive login, configures your AI tools, then starts
rem the background agent. Safe to re-run.
rem
rem This is the Windows sibling of installers/macos/onboard.command. It exists because
rem the Inno [Run] step used to launch `keld-agent install` with `runhidden`, so the
rem interactive login ran in a window nobody could see and no machine was ever
rem onboarded: the daemon idled on awaitConfig forever, collecting nothing and saying
rem nothing. Do not put this back behind runhidden.
set "AGENT=%~dp0keld-agent.exe"
if not exist "%AGENT%" set "AGENT=%LOCALAPPDATA%\Programs\keld\keld-agent.exe"

echo.
echo ==== Set up Keld ====
echo.
set /p CODE="Paste your setup code from the Keld download page (or press Enter to log in with a browser): "

if defined CODE (
  "%AGENT%" install --code "%CODE%"
  if errorlevel 1 (
    echo Setup code didn't work; falling back to browser login...
    "%AGENT%" install --yes
  )
) else (
  "%AGENT%" install --yes
)

rem Claim success only if it is true: setup is done when an ingest token exists in
rem hook.json, the same file the daemon reads. `keld-agent install` can exit 0 after
rem merely registering the scheduled task.
set "KELD_HOME_DIR=%KELD_HOME%"
if not defined KELD_HOME_DIR set "KELD_HOME_DIR=%USERPROFILE%\.keld"

echo.
findstr /r /c:"\"ingest_token\"[ ]*:[ ]*\"[^\"]" "%KELD_HOME_DIR%\hook.json" >nul 2>&1
if errorlevel 1 (
  echo Keld is installed, but NOT set up yet ^(nothing is being collected^).
  echo Run:  keld login  then  keld signal setup
  echo The agent is already running and picks the configuration up on its own.
) else (
  echo Keld is set up and running. You can close this window.
  echo   Enrichment runs on-device with no model download - nothing multi-gigabyte
  echo   is fetched, now or later.
)

echo.
echo ^(Re-run anytime: "%~f0"^)
echo.
pause
