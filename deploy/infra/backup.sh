#!/bin/sh
# Dumps the personal startpage database. Run daily from root's crontab:
#   15 3 * * * /opt/infra/backup.sh >> /var/log/startpage-backup.log 2>&1
#
# The demo database is skipped: it reseeds itself. The dumps stay on this
# server, so they protect against mistakes, not against losing the server;
# copy them off the box for that.
#
# Check a dump by restoring it into a scratch database:
#   docker exec postgres createdb -U postgres restore_check
#   docker exec -i postgres pg_restore -U postgres -d restore_check --no-owner < FILE
set -eu

container=${PG_CONTAINER:-postgres}
db=${DB:-startpage}
dir=${BACKUP_DIR:-/var/backups/startpage}
keep_days=${KEEP_DAYS:-14}

umask 077 # dumps hold session hashes: owner-only
mkdir -p "$dir"
file="$dir/$db-$(date -u +%Y%m%dT%H%M%SZ).dump"

# Write to a temp name first, so a failed dump never looks like a good one.
docker exec "$container" pg_dump -U postgres --format=custom "$db" > "$file.tmp"
mv "$file.tmp" "$file"
find "$dir" -name "$db-*.dump" -mtime +"$keep_days" -delete
echo "$(date -u +%FT%TZ) backup ok: $file ($(wc -c < "$file") bytes)"
