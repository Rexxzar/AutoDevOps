@echo off
cd /d "%~dp0web"
echo Starting AutoDevOps dashboard (dev server)...
echo Backend must be running on http://localhost:8080
echo.
npm run dev
