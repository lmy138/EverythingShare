@echo off
cd /d "%~dp0"
EverythingShare.exe
if errorlevel 1 (
  echo.
  echo EverythingShare stopped with an error. Review the message above.
  pause
)
