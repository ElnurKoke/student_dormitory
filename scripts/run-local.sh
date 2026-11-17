#!/bin/sh
# Build the current source and run it with the private local configuration.
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
if [ -f .env ]; then
  set -a
  . ./.env
  set +a
fi
mkdir -p bin
go build -o bin/server ./cmd/server
exec ./bin/server
