#!/bin/sh
# Runs once, when the Postgres volume is empty (docker-entrypoint-initdb.d).
#
# Each app gets its own login role and database, and never connects as the
# superuser. Postgres grants CONNECT on every database to PUBLIC by default, so
# without the REVOKE the public demo's role could open the personal database.
set -eu

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
	-v home_pw="$STARTPAGE_DB_PASSWORD" \
	-v demo_pw="$STARTPAGE_DEMO_DB_PASSWORD" <<'SQL'
CREATE ROLE startpage LOGIN PASSWORD :'home_pw';
CREATE DATABASE startpage OWNER startpage;
REVOKE CONNECT, TEMPORARY ON DATABASE startpage FROM PUBLIC;

CREATE ROLE startpage_demo LOGIN PASSWORD :'demo_pw';
CREATE DATABASE startpage_demo OWNER startpage_demo;
REVOKE CONNECT, TEMPORARY ON DATABASE startpage_demo FROM PUBLIC;
SQL
