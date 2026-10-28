@echo off
setlocal
"%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\install-portable-tools.ps1" -Launch %*
set "platform_exit=%ERRORLEVEL%"
if not "%platform_exit%"=="0" pause
exit /b %platform_exit%
