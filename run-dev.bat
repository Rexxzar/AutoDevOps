@echo off
cd /d %~dp0
set DATABASE_URL=postgres://autodevops:autodevops@localhost:5432/autodevops?sslmode=disable
set JWT_SECRET=dev-secret-32-chars-long-xxxxxxxx
set MASTER_KEY=dev-master-key-32-chars-xxxxxxxxx
set ADMIN_EMAIL=admin@autodevops.local
set ADMIN_PASSWORD=autodevops
"C:\Program Files\Go\bin\go.exe" run ./cmd/server
