"""Read-only SQLite integrity and row fingerprints. Never emits field contents."""
import hashlib
import json
import sqlite3
import sys
from pathlib import Path

path = Path(sys.argv[1])
if not path.is_file():
    raise SystemExit('SQLite backup missing')
connection = sqlite3.connect(path.resolve().as_uri() + '?mode=ro', uri=True)
assert connection.execute('PRAGMA integrity_check').fetchall() == [('ok',)], 'SQLite integrity failure'
tables = {}
for (name,) in connection.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"):
    quoted = '"' + name.replace('"', '""') + '"'
    columns = [row[1] for row in connection.execute('PRAGMA table_info(' + quoted + ')')]
    # Hash each canonical row then sort hashes, independent of physical order.
    hashes = []
    for row in connection.execute('SELECT * FROM ' + quoted):
        encoded = [{'blob_hex': value.hex()} if isinstance(value, bytes) else value for value in row]
        hashes.append(hashlib.sha256(json.dumps(encoded, separators=(',', ':'), ensure_ascii=True).encode()).hexdigest())
    tables[name] = {'rows': len(hashes), 'columns': columns,
                    'sha256': hashlib.sha256(''.join(sorted(hashes)).encode()).hexdigest()}
for required in ('users', 'tokens', 'channels', 'logs'):
    assert required in tables, 'Required fixture table missing: ' + required
print(json.dumps({'integrity': 'ok', 'tables': tables}, sort_keys=True))
