-- Run only on empty PGDATA by the official PostgreSQL entrypoint.
-- psql quotes the value as a SQL literal; do not echo interpolated commands.
\getenv app_password SUANJIE_DB_PASSWORD
BEGIN;
CREATE ROLE suanjie LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD :'app_password';
ALTER DATABASE suanjie OWNER TO suanjie;
COMMIT;
