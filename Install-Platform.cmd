@echo off
setlocal
"%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\install-portable-tools.ps1"
if errorlevel 1 exit /b %errorlevel%
"%~dp0..\work\runtime\toolchain\powershell-7.6.5\pwsh.exe" -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\install-platform.ps1"
exit /b %errorlevel%
