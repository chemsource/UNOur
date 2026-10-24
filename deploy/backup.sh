#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    printf 'Run this backup script as root to manage the UID 10001 bind mount and private backup files.\n' >&2
    exit 1
fi
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
backup_dir=${1:-"$repo_dir/backups"}
if [ ! -d "$backup_dir" ]; then
    install -d -m 0750 -o 10001 -g 10001 "$backup_dir"
fi
backup_dir=$(CDPATH= cd -- "$backup_dir" && pwd)
if [ "$(stat -c %u "$backup_dir")" != 10001 ] || [ "$(stat -c %A "$backup_dir" | cut -c 2-4)" != rwx ]; then
    printf 'Backup directory must be owned by UID 10001 with owner read/write/search permission: %s\n' "$backup_dir" >&2
    exit 1
fi
filename="unour-$(date -u +%Y%m%dT%H%M%SZ).db"
cd "$repo_dir"

# SQLite's online backup API includes committed WAL data without stopping play.
# The one-off container shares the data volume but never starts a game server.
docker compose run --rm --no-deps -T --interactive=false \
    --volume "$backup_dir:/backup" --entrypoint sqlite3 \
    api /data/unour.db ".backup /backup/$filename"
test -s "$backup_dir/$filename"
chmod 0600 "$backup_dir/$filename"
printf 'Backup saved: %s\n' "$backup_dir/$filename"
