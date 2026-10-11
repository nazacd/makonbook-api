#!/usr/bin/env bash
# Nightly backup of every database in the shared `db` container.
#
# Install on the VPS (DEPLOY.md step 7):
#   chmod 700 /opt/makonbook-api/backup-postgres.sh
#   crontab -e   →   30 2 * * * /opt/makonbook-api/backup-postgres.sh >> /var/log/pg-backup.log 2>&1
#
# Writes custom-format dumps (restore with pg_restore) plus the roles, keeps
# KEEP_DAYS days. These copies sit on the same disk as the database: they
# protect against mistakes, not against losing the server. Copy BACKUP_DIR
# off the machine too (e.g. a Hetzner Storage Box with rsync).
set -euo pipefail

CONTAINER=${CONTAINER:-db}
BACKUP_DIR=${BACKUP_DIR:-/opt/backups/postgres}
KEEP_DAYS=${KEEP_DAYS:-14}
stamp=$(date -u +%Y%m%d-%H%M)

mkdir -p "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR"

dbs=$(docker exec "$CONTAINER" psql -U postgres -d postgres -Atc \
  "select datname from pg_database where not datistemplate and datname not in ('postgres') and datname not like 'test\_%'")

for db in $dbs; do
  out="$BACKUP_DIR/$db-$stamp.dump"
  docker exec "$CONTAINER" pg_dump -U postgres -Fc "$db" > "$out.partial"
  mv "$out.partial" "$out"
  echo "$(date -u +%FT%TZ) $db $(du -h "$out" | cut -f1)"
done

# Roles and their passwords (needed to restore onto a fresh server).
docker exec "$CONTAINER" pg_dumpall -U postgres --globals-only > "$BACKUP_DIR/globals-$stamp.sql"
chmod 600 "$BACKUP_DIR"/*

find "$BACKUP_DIR" -type f \( -name '*.dump' -o -name '*.sql' \) -mtime +"$KEEP_DAYS" -delete
