@echo off
chcp 65001 >nul
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\本机环境.ps1" -Action Start
set "snack_result=%errorlevel%"
if not "%snack_result%"=="0" echo Operation failed. Please read the message above.
pause
exit /b %snack_result%