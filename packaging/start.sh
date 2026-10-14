#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
DATA_DIR=${DATA_DIR:-"$ROOT/data"}
if [ ! -f "$DATA_DIR/runtime.env" ]; then
    "$ROOT/ganwen" init-admin --data "$DATA_DIR"
fi
printf 'Blog: http://127.0.0.1:%s/\nAdmin: http://127.0.0.1:%s/admin/\n' "${PORT:-8097}" "${PORT:-8097}"
printf 'Administrator details: %s/admin-access.txt\n' "$DATA_DIR"
exec "$ROOT/ganwen" serve --data "$DATA_DIR" --listen "127.0.0.1:${PORT:-8097}" "$@"
