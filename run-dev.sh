#!/usr/bin/env bash
set -euo pipefail

export DATABASE_URL="postgres://autodevops:autodevops@localhost:5432/autodevops?sslmode=disable"
export JWT_SECRET="dev-secret-32-chars-long-xxxxxxxx"
export MASTER_KEY="dev-master-key-32-chars-xxxxxxxxx"
export ADMIN_EMAIL="admin@autodevops.local"
export ADMIN_PASSWORD="autodevops"

go run ./cmd/server
