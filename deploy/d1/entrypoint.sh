#!/bin/sh
set -eu
umask 077
fail() { echo 'D1 隔离启动被拒绝；检查测试目录、配置及随机值。' >&2; exit 1; }
[ "$#" -eq 0 ] || fail
[ "${SQL_DSN:-}" = local ] || fail
[ "${SQLITE_PATH:-}" = /data/test.sqlite ] || fail
[ "${#SESSION_SECRET}" -ge 32 ] || fail
[ "${#CRYPTO_SECRET}" -ge 32 ] || fail
[ "$SESSION_SECRET" != "$CRYPTO_SECRET" ] || fail
db=/data/test.sqlite
marker=/data/.d1-isolated-only
if [ ! -f "$marker" ]; then
  # Never accept an existing database, foreign volume or partial initialization.
  [ -z "$(find /data -mindepth 1 -maxdepth 1 -print -quit)" ] || fail
  sqlite3 "$db" <<'SQL'
BEGIN IMMEDIATE;
CREATE TABLE options (key TEXT PRIMARY KEY, value TEXT);
INSERT INTO options VALUES ('RegisterEnabled','false');
INSERT INTO options VALUES ('PasswordRegisterEnabled','false');
INSERT INTO options VALUES ('payment_setting.compliance_confirmed','false');
COMMIT;
SQL
  printf 'D1 synthetic-only\n' > "$marker"
fi
[ -f "$db" ] || fail
[ "$(sqlite3 "$db" 'PRAGMA quick_check;')" = ok ] || fail
[ "$(sqlite3 "$db" "SELECT count(*) FROM options WHERE key IN ('RegisterEnabled','PasswordRegisterEnabled','payment_setting.compliance_confirmed') AND value='false';")" = 3 ] || fail
if [ "$(sqlite3 "$db" "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='channels';")" = 1 ]; then
  [ "$(sqlite3 "$db" 'SELECT count(*) FROM channels;')" = 0 ] || fail
fi
# No dotenv, host files or supplier/payment environment is accepted by Compose.
cd /data
exec /new-api
