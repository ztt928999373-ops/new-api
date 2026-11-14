"""Manual D2 acceptance of an existing image; never builds or publishes."""
import hashlib
import base64
import http.client
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import secrets
import selectors
import socket
import stat
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time
from urllib.parse import quote

IMAGE = 'ghcr.io/ztt928999373-ops/suanjie-api@sha256:725d45a3fe8deb3155950a0935bc9873bf0439ef99a425be2dd5f13cbaa21ddf'
REVISION = 'cd8937c602640fc31c0144994a73266c3d792782'
CONFIG = 'sha256:a0cc42d43e45ee9ccf69bc12fad8e7c150df78867817dfc727e155e7b186f66a'
MIN_FREE = 5 * 1024**3
DIAGNOSTIC_BYTES = 64 * 1024
DIAGNOSTIC_TIMEOUT = 15
SENSITIVE_FIELD = re.compile(
    r'secret|token|password|passwd|credential|authorization|cookie|(?:api|private|access)[_ -]?key|dsn|bearer',
    re.I)
LABEL = 'io.suanjie.d2.acceptance'
OPTIONS = ('RegisterEnabled', 'PasswordRegisterEnabled', 'payment_setting.compliance_confirmed')
LICENSES = {
    'LICENSE': '8486a10c4393cee1c25392769ddd3b2d6c242d6ec7928e1414efff7dfb2f07ef',
    'NOTICE': '528067fcdf4f9d7e3fdb489d02cbdd36a0efa63fc2eb1686340612c26beb9f33',
    'THIRD-PARTY-LICENSES.md': '3848811f99c7b7ad802eea23775ccd262228c980f81eef64027f587689b0841d',
}
PHASES = ('capacity', 'pull', 'identity', 'seed', 'licenses', 'startup', 'health', 'network',
          'policy', 'stopped_database', 'restart_persistence', 'final_database', 'cleanup')

# Only metadata/access checks; never opens SQLite or reads database contents.
# Missing tools and failed protocol fields remain UNKNOWN in the host parser.
STORAGE_PROBE = r'''
LC_ALL=C; export LC_ALL
umask 077
printf 'VERSION=1\n'
printf 'UID=%s\n' "$(/usr/bin/id -u 2>/dev/null)"
printf 'GID=%s\n' "$(/usr/bin/id -g 2>/dev/null)"
printf 'DATA=%s\n' "$(/usr/bin/stat -c '%u|%g|%a|%F|%s' -- /data 2>/dev/null)"
printf 'DB=%s\n' "$(/usr/bin/stat -c '%u|%g|%a|%F|%s' -- /data/test.sqlite 2>/dev/null)"
if [ ! -x /data ]; then printf 'EXISTS=UNKNOWN\n'
elif [ -e /data/test.sqlite ] || [ -L /data/test.sqlite ]; then printf 'EXISTS=YES\n'; else printf 'EXISTS=NO\n'; fi
if [ -x /data ]; then printf 'TRAVERSE=PASS\n'; else printf 'TRAVERSE=FAIL\n'; fi
if [ -f /data/test.sqlite ] && [ ! -L /data/test.sqlite ]; then
  if [ -r /data/test.sqlite ]; then printf 'READ_ACCESS=PASS\n'; else printf 'READ_ACCESS=FAIL\n'; fi
  if [ -w /data/test.sqlite ]; then printf 'WRITE_ACCESS=PASS\n'; else printf 'WRITE_ACCESS=FAIL\n'; fi
fi
probe=''
trap '[ -z "$probe" ] || /usr/bin/rm -f -- "$probe" >/dev/null 2>&1' EXIT HUP INT TERM
result=$(/usr/bin/mktemp /data/.d2-diagnostic-XXXXXXXXXX 2>&1)
if [ "$?" -eq 0 ]; then
  probe=$result
  printf 'CREATE=PASS\nCREATE_ERROR_CLASS=NONE\n'
  if /usr/bin/rm -- "$probe" 2>/dev/null; then probe=''; printf 'REMOVE=PASS\n'; else printf 'REMOVE=FAIL\n'; fi
else
  printf 'CREATE=FAIL\n'
  case "$result" in
    *'Permission denied'*) printf 'CREATE_ERROR_CLASS=EACCES\n';;
    *'Read-only file system'*) printf 'CREATE_ERROR_CLASS=EROFS\n';;
    *'No space left on device'*) printf 'CREATE_ERROR_CLASS=ENOSPC\n';;
    *'No such file or directory'*) printf 'CREATE_ERROR_CLASS=ENOENT\n';;
    *) printf 'CREATE_ERROR_CLASS=UNKNOWN\n';;
  esac
fi
for kind in uid gid; do
  while IFS= read -r line; do printf '%s_MAP=%s\n' "$kind" "$line"; done < /proc/self/${kind}_map
done
'''


def require(value, code):
    if not value:
        raise RuntimeError(code)


def trusted(env):
    require(env.get('GITHUB_REPOSITORY') == 'ztt928999373-ops/new-api'
            and env.get('GITHUB_REF') == 'refs/heads/product/v1'
            and env.get('GITHUB_EVENT_NAME') == 'workflow_dispatch'
            and re.fullmatch('[a-f0-9]{40}', env.get('GITHUB_SHA', '')), 'D2_UNTRUSTED_CONTEXT')


def capacity(root):
    free = shutil.disk_usage(root).free
    require(free >= MIN_FREE, 'D2_DISK_INSUFFICIENT')
    return free


def seed(directory, proof):
    directory.mkdir(mode=0o700)
    db = directory / 'test.sqlite'
    with sqlite3.connect(db) as conn:
        conn.execute('CREATE TABLE options (key TEXT PRIMARY KEY, value TEXT)')
        conn.executemany('INSERT INTO options VALUES (?, ?)', [(key, 'false') for key in OPTIONS])
    db.chmod(0o600)
    (directory / 'd2-proof').write_text(proof)


def database(directory, proof):
    require((directory / 'd2-proof').read_text() == proof, 'D2_PROOF_MISMATCH')
    db = directory / 'test.sqlite'
    require(db.is_file() and not db.is_symlink(), 'D2_DATABASE_MISSING')
    with sqlite3.connect(db.as_uri() + '?mode=ro', uri=True) as conn:
        require(conn.execute('PRAGMA integrity_check').fetchall() == [('ok',)], 'D2_DATABASE_INTEGRITY')
        for key in OPTIONS:
            require(conn.execute('SELECT value FROM options WHERE key=?', (key,)).fetchone()
                    == ('false',), 'D2_POLICY_ENABLED')
        require(conn.execute('SELECT count(*) FROM channels').fetchone() == (0,), 'D2_CHANNELS_PRESENT')
        require(conn.execute("SELECT count(*) FROM users WHERE username='d2-denied'").fetchone() == (0,), 'D2_REGISTRATION_CREATED_USER')


class Acceptance:
    def __init__(self, work, env):
        self.work = work
        self.env = dict(env)
        for key in ('DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_TLS', 'DOCKER_TLS_VERIFY', 'DOCKER_CERT_PATH'):
            self.env.pop(key, None)
        anonymous = work / 'anonymous-docker'
        anonymous.mkdir(mode=0o700, exist_ok=True)
        self.env['DOCKER_CONFIG'] = str(anonymous)
        self.state_file = work / 'd2-acceptance-state.json'
        self.fresh = not self.state_file.exists()
        if not self.fresh:
            self.state = json.loads(self.state_file.read_text())
        else:
            name = 'd2-accept-' + secrets.token_hex(12)
            self.state = {'container': name, 'volume': name + '-data', 'owner': name}
            self.state_file.write_text(json.dumps(self.state))
            self.state_file.chmod(0o600)
        self.report = {'image': IMAGE, 'image_revision': REVISION,
                       'verifier_sha': env.get('GITHUB_SHA'), 'phases': {p: 'not_run' for p in PHASES}}
        self.phase = 'capacity'

    def call(self, command, timeout=120, env=None):
        result = subprocess.run(command, env=env or self.env, capture_output=True, text=True, timeout=timeout)
        require(result.returncode == 0, 'D2_COMMAND_FAILED')
        return result.stdout

    def docker(self, *args, timeout=120):
        return self.call(['docker', '--host=unix:///var/run/docker.sock', *args], timeout=timeout)

    def resource(self, kind, name):
        result = subprocess.run(['docker', '--host=unix:///var/run/docker.sock', kind, 'inspect', name],
                                env=self.env, capture_output=True, text=True, timeout=30)
        if result.returncode:
            require(any(marker in result.stderr.lower() for marker in
                        ('no such container', 'no such object', 'no such volume')), 'D2_RESOURCE_INSPECT_FAILED')
            return None
        return json.loads(result.stdout)[0]

    def step(self, phase):
        self.phase = phase

    def passed(self):
        self.report['phases'][self.phase] = 'success'

    def health(self):
        name = self.state['container']
        for _ in range(60):
            state = json.loads(self.docker('inspect', '--format', '{{json .State}}', name))
            require(state['Status'] not in ('exited', 'dead'), 'D2_CONTAINER_EXITED')
            if state.get('Health', {}).get('Status') == 'healthy':
                body = self.docker('exec', name, 'wget', '-q', '-T', '5', '-O', '-', 'http://127.0.0.1:3000/api/status')
                require(json.loads(body).get('success') is True, 'D2_STATUS_FAILED')
                return
            time.sleep(2)
        raise RuntimeError('D2_HEALTH_TIMEOUT')

    def snapshot(self, destination, proof):
        state = json.loads(self.docker('inspect', '--format', '{{json .State}}', self.state['container']))
        require(not state['Running'], 'D2_DATABASE_NOT_STOPPED')
        destination.mkdir(mode=0o700)
        self.docker('cp', self.state['container'] + ':/data/.', str(destination))
        database(destination, proof)

    def redact(self, text):
        """Fail closed on credential-bearing lines; never publish raw Docker output."""
        text = str(text)
        # Normalize terminal/control obfuscation before searching for secret fields.
        text = re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]', '', text)
        text = ''.join(c for c in text if c.isprintable() or c in '\n\t')
        for key, value in self.env.items():
            if (SENSITIVE_FIELD.search(key) or 'proxy' in key.lower()) and len(value) >= 8:
                text = text.replace(value, '[REDACTED]')
        text = re.sub(r'-----BEGIN [^-]*PRIVATE KEY-----.*?(?:-----END [^-]*PRIVATE KEY-----|\Z)',
                      '[REDACTED PRIVATE KEY]', text, flags=re.S)
        lines = []
        for line in text.splitlines():
            if SENSITIVE_FIELD.search(line):
                line = '[REDACTED SENSITIVE LINE]'
            line = re.sub(r'(?i)\b[a-z][a-z0-9+.-]*://[^\s\"\'<>]+', '[REDACTED URL]', line)
            line = re.sub(r'(?<![\w])[A-Za-z0-9_+/=-]{32,}(?![\w])', '[REDACTED OPAQUE VALUE]', line)
            lines.append(line)
        return '\n'.join(lines)

    def diagnostic_value(self, value, depth=0):
        """Bound and sanitize the complete State tree, excluding all Config/Env data."""
        if depth > 8:
            return '[TRUNCATED DEPTH]'
        if isinstance(value, dict):
            return {self.redact(key)[:128]: ('[REDACTED]' if SENSITIVE_FIELD.search(key)
                        or self.redact(key).startswith('[REDACTED')
                        else self.diagnostic_value(item, depth + 1))
                    for key, item in list(value.items())[:64]}
        if isinstance(value, list):
            return [self.diagnostic_value(item, depth + 1) for item in value[:16]]
        if isinstance(value, str):
            return self.redact(value)[:8192]
        if value is None or type(value) in (bool, int, float):
            return value
        return '[UNSUPPORTED VALUE]'

    def container_logs(self, name=None, timestamps=True):
        """Read separate streams with a combined byte ceiling and wall-clock timeout."""
        command = ['docker', '--host=unix:///var/run/docker.sock', 'logs']
        if timestamps:
            command.append('--timestamps')
        command += ['--tail=200', name or self.state['container']]
        buffers = {'stdout': bytearray(), 'stderr': bytearray()}
        total, truncated = 0, False
        deadline = time.monotonic() + DIAGNOSTIC_TIMEOUT
        error = None
        with subprocess.Popen(command, env=self.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE) as process:
            try:
                with selectors.DefaultSelector() as selector:
                    for stream, name in ((process.stdout, 'stdout'), (process.stderr, 'stderr')):
                        os.set_blocking(stream.fileno(), False)
                        selector.register(stream, selectors.EVENT_READ, name)
                    while selector.get_map():
                        remaining = deadline - time.monotonic()
                        require(remaining > 0, 'D2_DIAGNOSTIC_LOG_TIMEOUT')
                        for key, _ in selector.select(min(remaining, 0.1)):
                            chunk = os.read(key.fd, min(4096, DIAGNOSTIC_BYTES - total))
                            if not chunk:
                                selector.unregister(key.fileobj)
                                continue
                            buffers[key.data].extend(chunk)
                            total += len(chunk)
                            if total >= DIAGNOSTIC_BYTES:
                                truncated = True
                                break
                        if truncated:
                            break
                if not truncated:
                    require(process.wait(timeout=max(0.01, deadline - time.monotonic())) == 0,
                            'D2_DIAGNOSTIC_LOG_FAILED')
            except Exception as failure:
                error = str(failure) if str(failure) in ('D2_DIAGNOSTIC_LOG_TIMEOUT', 'D2_DIAGNOSTIC_LOG_FAILED') else 'D2_DIAGNOSTIC_LOG_FAILED'
            finally:
                if process.poll() is None:
                    process.kill()
                process.wait(timeout=5)
        # Discard any incomplete trailing line: a byte limit may split a secret value.
        logs = {}
        budget = DIAGNOSTIC_BYTES
        for name, data in buffers.items():
            if (truncated or error) and data and not data.endswith(b'\n'):
                data = data[:data.rfind(b'\n') + 1]
            safe = self.redact(bytes(data).decode('utf-8', errors='replace')).encode('utf-8')
            truncated = truncated or len(safe) > budget
            logs[name] = safe[:budget].decode('utf-8', errors='ignore')
            budget -= len(logs[name].encode('utf-8'))
        return {**logs, 'truncated': truncated, 'error_code': error}

    def diagnose(self):
        diagnostics = {'status': 'success', 'errors': [], 'runtime_seconds': None,
                       'capture_phase': self.phase,
                       'limits': {'log_bytes_combined': DIAGNOSTIC_BYTES, 'log_tail_lines': 200,
                                  'timeout_seconds_per_command': DIAGNOSTIC_TIMEOUT,
                                  'state_string_characters': 8192, 'state_depth': 8}}
        self.report['diagnostics'] = diagnostics
        try:
            raw = self.docker('inspect', '--format',
                              '{"State":{{json .State}},"RestartCount":{{json .RestartCount}}}',
                              self.state['container'], timeout=DIAGNOSTIC_TIMEOUT)
            require(len(raw.encode('utf-8')) <= 2 * DIAGNOSTIC_BYTES, 'D2_DIAGNOSTIC_STATE_TOO_LARGE')
            inspected = json.loads(raw)
            state = inspected['State']
            require(isinstance(state, dict) and type(inspected['RestartCount']) is int,
                    'D2_DIAGNOSTIC_STATE_INVALID')
            diagnostics['State'] = self.diagnostic_value(state)
            diagnostics['RestartCount'] = inspected['RestartCount']
            try:
                start = datetime.fromisoformat(state['StartedAt'].replace('Z', '+00:00'))
                require(start.year > 1 and start.tzinfo is not None, 'D2_DIAGNOSTIC_TIME_INVALID')
                end = datetime.now(timezone.utc) if state.get('Running') else datetime.fromisoformat(state['FinishedAt'].replace('Z', '+00:00'))
                require(end.year > 1 and end.tzinfo is not None and end >= start, 'D2_DIAGNOSTIC_TIME_INVALID')
                diagnostics['runtime_seconds'] = (end - start).total_seconds()
                diagnostics['runtime_kind'] = 'elapsed_at_capture' if state.get('Running') else 'completed_execution'
            except Exception:
                diagnostics['errors'].append('D2_DIAGNOSTIC_TIME_INVALID')
        except Exception:
            diagnostics['errors'].append('D2_DIAGNOSTIC_STATE_FAILED')
        try:
            diagnostics['logs'] = self.container_logs()
            if diagnostics['logs']['error_code']:
                diagnostics['errors'].append(diagnostics['logs']['error_code'])
        except Exception:
            diagnostics['errors'].append('D2_DIAGNOSTIC_LOG_FAILED')
        if diagnostics['errors']:
            diagnostics['status'] = 'incomplete'
        try:
            diagnostics['storage'] = self.storage_diagnostics()
        except Exception as error:
            code = 'D2_STORAGE_DIAGNOSTIC_TIMEOUT' if isinstance(error, (TimeoutError, subprocess.TimeoutExpired)) else 'D2_STORAGE_DIAGNOSTIC_FAILED'
            diagnostics.setdefault('storage', {}).update(status='UNKNOWN', error_code=code)
        if diagnostics['storage']['status'] == 'UNKNOWN':
            diagnostics['status'] = 'incomplete'

    def observe_primary_identity(self):
        """Best effort while alive; bind host /proc evidence to the exact container ID."""
        identity = {'status': 'UNKNOWN', 'source': 'host_proc_bound_to_container_id'}
        self.report['primary_process_identity'] = identity
        try:
            projection = '{"Id":{{json .Id}},"Pid":{{json .State.Pid}},"Running":{{json .State.Running}}}'
            first = json.loads(self.docker('inspect', '--format', projection, self.state['container'], timeout=DIAGNOSTIC_TIMEOUT))
            require(first['Running'] is True and type(first['Pid']) is int and first['Pid'] > 0
                    and re.fullmatch('[a-f0-9]{64}', first['Id']), 'D2_PRIMARY_PROCESS_UNAVAILABLE')
            values = {}
            for name in ('cgroup', 'status', 'uid_map', 'gid_map'):
                with (Path('/proc') / str(first['Pid']) / name).open() as stream:
                    text = stream.read(8193)
                require(len(text) <= 8192, 'D2_PRIMARY_METADATA_LIMIT')
                values[name] = text
            require(first['Id'] in values['cgroup'], 'D2_PRIMARY_HOST_MISMATCH')
            second = json.loads(self.docker('inspect', '--format', projection, self.state['container'], timeout=DIAGNOSTIC_TIMEOUT))
            require(first == second, 'D2_PRIMARY_PROCESS_CHANGED')
            for label, mapping_name in (('Uid', 'uid_map'), ('Gid', 'gid_map')):
                line = next(s for s in values['status'].splitlines() if s.startswith(label + ':'))
                host_id = int(line.split()[2])
                mapping = [[int(n) for n in row.split()] for row in values[mapping_name].splitlines()]
                require(all(len(row) == 3 and all(0 <= n <= 2**32 for n in row) for row in mapping), 'D2_PRIMARY_MAP_INVALID')
                translated = [inside + host_id - outside for inside, outside, count in mapping
                              if outside <= host_id < outside + count]
                require(len(translated) == 1, 'D2_PRIMARY_MAP_UNKNOWN')
                identity[label.lower()] = translated[0]
                identity[mapping_name] = mapping
            identity['status'] = 'MEASURED'
            identity['host_pid'] = first['Pid']
        except Exception:
            identity.update(status='UNKNOWN', error_code='D2_PRIMARY_IDENTITY_UNAVAILABLE')

    def image_tool(self, path):
        """Docker archive HEAD reads path metadata only, not file or database bodies."""
        for _ in range(4):
            require(path.startswith(('/bin/', '/usr/bin/')) and '..' not in path,
                    'D2_TOOL_PATH_REJECTED')
            connection = http.client.HTTPConnection('localhost', timeout=DIAGNOSTIC_TIMEOUT)
            try:
                connection.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                connection.sock.settimeout(DIAGNOSTIC_TIMEOUT)
                connection.sock.connect('/var/run/docker.sock')
                connection.request('HEAD', '/v1.45/containers/' + quote(self.state['container'], safe='')
                                   + '/archive?path=' + quote(path, safe=''))
                response = connection.getresponse()
                require(response.status == 200, 'D2_TOOL_UNAVAILABLE')
                header = response.getheader('X-Docker-Container-Path-Stat', '')
                require(len(header) <= 8192, 'D2_TOOL_METADATA_LIMIT')
                entry = json.loads(base64.b64decode(header, validate=True))
            finally:
                connection.close()
            mode = entry['mode']
            if mode & (1 << 27):  # Go FileMode.ModeSymlink
                target = entry['linkTarget']
                path = target if target.startswith('/') else str(Path(path).parent / target)
                continue
            require(type(mode) is int and mode & ~0o777 == 0 and mode & 0o111 and entry['size'] > 0,
                    'D2_TOOL_NOT_EXECUTABLE')
            return
        raise RuntimeError('D2_TOOL_LINK_LIMIT')

    def storage_result(self, output, mount):
        """Parse a fixed protocol, never publish arbitrary probe stdout/stderr."""
        result = {'status': 'UNKNOWN', 'source': 'same_image_auxiliary_process',
                  'primary_process_identity': 'UNKNOWN', 'sqlite_open_errno': 'UNKNOWN',
                  'database_write_open': 'UNKNOWN', 'checks': {}, 'metadata': {}}
        fields = {}
        for line in output.splitlines():
            key, separator, value = line.partition('=')
            if separator and key not in ('uid_MAP', 'gid_MAP'):
                require(key not in fields, 'D2_PROBE_PROTOCOL_DUPLICATE')
                fields[key] = value
            elif key in ('uid_MAP', 'gid_MAP'):
                mapping = [int(n) for n in value.split()]
                require(len(mapping) == 3 and all(0 <= n <= 2**32 for n in mapping), 'D2_PROBE_PROTOCOL_INVALID')
                result.setdefault(key, []).append(mapping)
                require(len(result[key]) <= 8, 'D2_PROBE_PROTOCOL_LIMIT')
        require(fields.get('VERSION') == '1', 'D2_PROBE_PROTOCOL_INVALID')
        for name in ('UID', 'GID'):
            if re.fullmatch(r'\d{1,10}', fields.get(name, '')):
                result[name] = int(fields[name])
        for key in ('DATA', 'DB'):
            parts = fields.get(key, '').split('|')
            if (len(parts) == 5 and re.fullmatch(r'\d{1,10}', parts[0])
                    and re.fullmatch(r'\d{1,10}', parts[1]) and re.fullmatch('[0-7]{1,4}', parts[2])
                    and parts[3] in ('directory', 'regular file', 'regular empty file', 'symbolic link', 'fifo', 'character special file', 'block special file', 'socket')
                    and re.fullmatch(r'\d{1,20}', parts[4])):
                result['metadata'][key] = {'uid': int(parts[0]), 'gid': int(parts[1]),
                                           'mode': parts[2], 'type': parts[3], 'size': int(parts[4])}
        checks = result['checks']
        checks['mount_rw'] = 'PASS' if mount['RW'] is True else 'FAIL' if mount['RW'] is False else 'UNKNOWN'
        for key in ('TRAVERSE', 'READ_ACCESS', 'WRITE_ACCESS', 'CREATE', 'REMOVE'):
            checks[key] = fields.get(key) if fields.get(key) in ('PASS', 'FAIL') else 'UNKNOWN'
        if checks['CREATE'] == 'FAIL':
            checks['REMOVE'] = 'NOT_APPLICABLE'
        result['create_error_class'] = fields.get('CREATE_ERROR_CLASS') if fields.get('CREATE_ERROR_CLASS') in ('NONE', 'EACCES', 'EROFS', 'ENOSPC', 'ENOENT') else 'UNKNOWN'
        db, directory = result['metadata'].get('DB'), result['metadata'].get('DATA')
        checks['file_exists'] = 'PASS' if fields.get('EXISTS') == 'YES' else 'FAIL' if fields.get('EXISTS') == 'NO' else 'UNKNOWN'
        checks['regular_database'] = ('PASS' if db['type'] == 'regular file' and db['size'] > 0 else 'FAIL') if db else 'UNKNOWN'
        checks['directory'] = ('PASS' if directory['type'] == 'directory' else 'FAIL') if directory else 'UNKNOWN'
        # Mismatch is evidence, not proof of denial: group/ACL permissions may grant access.
        result['owner_matches_probe_uid'] = {key: (item['uid'] == result['UID'] if 'UID' in result else 'UNKNOWN')
                                             for key, item in result['metadata'].items()}
        complete = ('UID' in result and 'GID' in result and 'uid_MAP' in result and 'gid_MAP' in result)
        result['access_status'] = 'FAIL' if 'FAIL' in checks.values() else 'PASS' if complete and all(v in ('PASS', 'NOT_APPLICABLE') for v in checks.values()) else 'UNKNOWN'
        result['status'] = 'FAIL' if result['access_status'] == 'FAIL' else 'UNKNOWN'
        return result

    def storage_diagnostics(self):
        result = {'status': 'UNKNOWN', 'primary_process_identity': 'UNKNOWN', 'source': 'docker_inspect_configuration'}
        self.report.setdefault('diagnostics', {})['storage'] = result
        result['primary_process_identity'] = self.report.get('primary_process_identity', {'status': 'UNKNOWN'})
        projection = ('{"Id":{{json .Id}},"Image":{{json .Image}},"User":{{json .Config.User}},'
                      '"WorkingDir":{{json .Config.WorkingDir}},"UsernsMode":{{json .HostConfig.UsernsMode}},'
                      '"CapDrop":{{json .HostConfig.CapDrop}},"SecurityOpt":{{json .HostConfig.SecurityOpt}},'
                      '"CapAdd":{{json .HostConfig.CapAdd}},"Privileged":{{json .HostConfig.Privileged}},"GroupAdd":{{json .HostConfig.GroupAdd}},'
                      '"ReadonlyRootfs":{{json .HostConfig.ReadonlyRootfs}},"NetworkMode":{{json .HostConfig.NetworkMode}},'
                      '"Owner":{{json (index .Config.Labels "' + LABEL + '")}},"Mounts":{{json .Mounts}}}')
        config = json.loads(self.docker('inspect', '--format', projection, self.state['container'], timeout=DIAGNOSTIC_TIMEOUT))
        require(config['Owner'] == self.state['owner'] and config['Image'] == CONFIG, 'D2_PROBE_OWNERSHIP')
        result['container_config'] = self.diagnostic_value({key: config[key] for key in
            ('User', 'WorkingDir', 'UsernsMode', 'CapDrop', 'CapAdd', 'Privileged', 'GroupAdd', 'SecurityOpt', 'ReadonlyRootfs', 'NetworkMode')})
        image = json.loads(self.docker('image', 'inspect', '--format', '{{json .Config.User}}', IMAGE, timeout=DIAGNOSTIC_TIMEOUT))
        require(isinstance(image, str) and isinstance(config['User'], str), 'D2_PROBE_CONFIG_INVALID')
        result['image_config_user'] = self.redact(image)
        mounts = [m for m in config['Mounts'] if m.get('Destination') == '/data']
        require(len(mounts) == 1, 'D2_PROBE_MOUNT_INVALID')
        mount = mounts[0]
        require(mount.get('Type') == 'volume' and mount.get('Name') == self.state['volume']
                and type(mount.get('RW')) is bool, 'D2_PROBE_MOUNT_INVALID')
        result['mount'] = {key: mount[key] for key in ('Type', 'Name', 'Destination', 'RW')}
        volume = json.loads(self.docker('volume', 'inspect', self.state['volume'], timeout=DIAGNOSTIC_TIMEOUT))[0]
        require((volume.get('Labels') or {}).get(LABEL) == self.state['owner']
                and volume.get('Driver') == 'local' and not volume.get('Options'), 'D2_PROBE_OWNERSHIP')
        require(config['ReadonlyRootfs'] is True and config['NetworkMode'] == 'none'
                and config['CapAdd'] in (None, []) and config['Privileged'] is False and config['GroupAdd'] in (None, [])
                and config['CapDrop'] == ['ALL'] and config['SecurityOpt'] == ['no-new-privileges:true'],
                'D2_PROBE_CONFIG_MISMATCH')
        capabilities = {}
        result['capabilities'] = capabilities
        for path in ('/bin/sh', '/usr/bin/id', '/usr/bin/stat', '/usr/bin/mktemp', '/usr/bin/rm'):
            try:
                self.image_tool(path)
                capabilities[path] = 'EXECUTABLE_METADATA_PRESENT'
            except Exception:
                capabilities[path] = 'UNKNOWN'
        if 'UNKNOWN' in capabilities.values():
            result['error_code'] = 'D2_PROBE_TOOLS_UNKNOWN'
            return result
        probe = self.state['owner'] + '-probe'
        require(self.resource('container', probe) is None, 'D2_PROBE_ALREADY_EXISTS')
        self.state['probe'] = probe
        # Keep the previous valid cleanup record if persisting the probe record fails.
        with tempfile.NamedTemporaryFile(mode='w', dir=self.work, delete=False) as pending:
            pending.write(json.dumps(self.state))
        try:
            os.replace(pending.name, self.state_file)
        finally:
            Path(pending.name).unlink(missing_ok=True)
        command = ['create', '--name', probe, '--label', LABEL + '=' + self.state['owner'],
                   '--pull=never', '--network=none', '--restart=no', '--read-only', '--cap-drop=ALL',
                   '--security-opt=no-new-privileges:true', '--memory=1g', '--cpus=2', '--pids-limit=256',
                   '--tmpfs=/tmp:rw,noexec,nosuid,size=128m', '--log-opt=max-size=10m', '--log-opt=max-file=3',
                   '--mount', 'type=volume,src=' + self.state['volume'] + ',dst=/data' + ('' if mount['RW'] else ',readonly'),
                   '--entrypoint=/bin/sh']
        if config['User']:
            command += ['--user', config['User']]
        if config['UsernsMode']:
            command += ['--userns', config['UsernsMode']]
        self.docker(*command, IMAGE, '-c', STORAGE_PROBE, timeout=DIAGNOSTIC_TIMEOUT)
        self.docker('start', probe, timeout=DIAGNOSTIC_TIMEOUT)
        exit_code = self.docker('wait', probe, timeout=DIAGNOSTIC_TIMEOUT).strip()
        logs = self.container_logs(probe, timestamps=False)
        require(exit_code == '0' and not logs['error_code'] and not logs['truncated'], 'D2_PROBE_EXECUTION_FAILED')
        result['measurement'] = self.storage_result(logs['stdout'], mount)
        primary = result['primary_process_identity']
        measured = result['measurement']
        result['probe_identity_matches_observed_primary'] = (
            primary['uid'] == measured.get('UID') and primary['gid'] == measured.get('GID')
            if primary.get('status') == 'MEASURED' and 'UID' in measured and 'GID' in measured else 'UNKNOWN')
        db = result['measurement']['metadata'].get('DB')
        result['database_size_matches_seed'] = (db['size'] == self.report['seed_file']['size']
            if db and 'seed_file' in self.report else 'UNKNOWN')
        result['status'] = result['measurement']['status']
        result['source'] = 'docker_inspect_configuration_and_auxiliary_process_measurement'
        return result

    def run(self):
        require(self.fresh, 'D2_STATE_REUSE_REJECTED')
        root = self.docker('info', '--format', '{{.DockerRootDir}}').strip()
        self.report['free_before'] = capacity(root)
        self.passed()
        self.step('pull')
        self.docker('pull', IMAGE, timeout=600)
        self.report['free_after_pull'] = capacity(root)
        self.passed()
        self.step('identity')
        image = json.loads(self.docker('image', 'inspect', IMAGE))[0]
        require(IMAGE in image.get('RepoDigests', []) and image['Id'] == CONFIG, 'D2_IMAGE_DIGEST_MISMATCH')
        # Reuse the existing D2 metadata and image-secret validator, not a new policy.
        validator = Path(__file__).with_name('ghcr-release.cjs')
        program = "let s='';process.stdin.on('data',b=>s+=b);process.stdin.on('end',()=>require(process.argv[1]).validateMetadata(JSON.parse(s),process.argv[2]));"
        result = subprocess.run(['node', '-e', program, str(validator), REVISION], input=json.dumps(image),
                                env=self.env, capture_output=True, text=True, timeout=30)
        require(result.returncode == 0, 'D2_IMAGE_METADATA_MISMATCH')
        self.report['image_size'] = image['Size']
        self.report['local_config_digest'] = image['Id']
        self.report['platform'] = image['Os'] + '/' + image['Architecture']
        self.passed()
        self.step('seed')
        name, volume, owner = (self.state[k] for k in ('container', 'volume', 'owner'))
        require(self.resource('volume', volume) is None and self.resource('container', name) is None,
                'D2_RESOURCE_ALREADY_EXISTS')
        self.docker('volume', 'create', '--label', LABEL + '=' + owner, volume)
        owned = self.resource('volume', volume)
        require(owned is not None and (owned.get('Labels') or {}).get(LABEL) == owner
                and owned.get('Driver') == 'local' and not owned.get('Options'), 'D2_VOLUME_OWNERSHIP')
        self.env['SESSION_SECRET'] = secrets.token_hex(32)
        self.env['CRYPTO_SECRET'] = secrets.token_hex(32)
        require(self.env['SESSION_SECRET'] != self.env['CRYPTO_SECRET'], 'D2_RANDOM_KEYS_COLLIDED')
        self.docker('create', '--name', name, '--label', LABEL + '=' + owner,
                    '--pull=never', '--network=none', '--restart=no', '--read-only',
                    '--cap-drop=ALL', '--security-opt=no-new-privileges:true',
                    '--memory=1g', '--cpus=2', '--pids-limit=256',
                    '--tmpfs=/tmp:rw,noexec,nosuid,size=128m',
                    '--log-opt=max-size=10m', '--log-opt=max-file=3',
                    '--mount', 'type=volume,src=' + volume + ',dst=/data',
                    '--env', 'SESSION_SECRET', '--env', 'CRYPTO_SECRET',
                    '--env', 'SQL_DSN=local', '--env', 'LOG_SQL_DSN=', '--env', 'SQLITE_PATH=/data/test.sqlite',
                    '--env', 'REDIS_CONN_STRING=', '--env', 'PORT=3000', '--env', 'BIND_ADDRESS=0.0.0.0',
                    '--env', 'TRUSTED_PROXIES=none', '--env', 'ENABLE_PPROF=false', '--env', 'DEBUG=false',
                    '--env', 'BATCH_UPDATE_ENABLED=false',
                    '--health-cmd', 'wget -q -T 5 -O - http://127.0.0.1:3000/api/status | grep -q \'"success":true\'',
                    '--health-interval=5s', '--health-timeout=5s', '--health-retries=6',
                    '--health-start-period=30s', IMAGE)
        proof = secrets.token_hex(24)
        directory = self.work / 'seed'
        seed(directory, proof)
        self.prepare_seed_owner(directory / 'test.sqlite')
        self.copy_seed(directory)
        self.preflight_seed()
        self.passed()
        self.step('licenses')
        for filename, digest in LICENSES.items():
            target = self.work / filename
            self.docker('cp', name + ':/licenses/' + filename, str(target))
            require(hashlib.sha256(target.read_bytes()).hexdigest() == digest, 'D2_LICENSE_MISMATCH')
        self.passed()
        self.step('startup')
        self.docker('start', name)
        self.observe_primary_identity()
        self.passed()
        self.step('health')
        self.health()
        self.passed()
        self.step('network')
        host = json.loads(self.docker('inspect', '--format', '{{json .HostConfig}}', name))
        require(host['NetworkMode'] == 'none' and not host.get('PortBindings'), 'D2_NETWORK_EXPOSED')
        routes = self.docker('exec', name, 'cat', '/proc/net/route')
        require(all(line.split()[1] != '00000000' for line in routes.splitlines()[1:] if line.split()), 'D2_DEFAULT_ROUTE')
        self.passed()
        self.step('policy')
        denial = json.loads(self.docker('exec', name, 'wget', '-q', '-T', '5', '-O', '-',
                            '--header=Content-Type: application/json',
                            '--post-data={"username":"d2-denied","password":"Synthetic-D2-Denied-Only!"}',
                            'http://127.0.0.1:3000/api/user/register'))
        require(denial.get('success') is False, 'D2_REGISTRATION_NOT_DENIED')
        self.passed()
        self.step('stopped_database')
        self.docker('stop', '--time=30', name)
        self.snapshot(self.work / 'before-restart', proof)
        self.passed()
        self.step('restart_persistence')
        self.docker('start', name)
        self.health()
        require(self.docker('exec', name, 'cat', '/data/d2-proof') == proof, 'D2_PROOF_MISMATCH')
        self.passed()
        self.step('final_database')
        self.docker('stop', '--time=30', name)
        self.snapshot(self.work / 'after-restart', proof)
        self.report['free_after_tests'] = capacity(root)
        self.passed()

    def prepare_seed_owner(self, db):
        require(db == self.work / 'seed' / 'test.sqlite' and not db.parent.is_symlink(), 'D2_SEED_PATH_INVALID')
        try:
            before = db.lstat()
        except OSError:
            raise RuntimeError('D2_SEED_FILE_INVALID') from None
        require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and stat.S_IMODE(before.st_mode) == 0o600,
                'D2_SEED_FILE_INVALID')
        try:
            if os.geteuid() == 0:
                os.chown(db, 0, 0, follow_symlinks=False)
            else:
                self.call(['/usr/bin/sudo', '-n', '--', '/usr/bin/chown', '--no-dereference', '0:0', str(db)],
                          timeout=DIAGNOSTIC_TIMEOUT)
        except Exception:
            raise RuntimeError('D2_SEED_OWNER_CHANGE_FAILED') from None
        try:
            after = db.lstat()
        except OSError:
            raise RuntimeError('D2_SEED_OWNER_VERIFICATION_FAILED') from None
        require((after.st_dev, after.st_ino) == (before.st_dev, before.st_ino)
                and stat.S_ISREG(after.st_mode) and after.st_nlink == 1
                and after.st_uid == 0 and after.st_gid == 0 and stat.S_IMODE(after.st_mode) == 0o600,
                'D2_SEED_OWNER_VERIFICATION_FAILED')
        self.report['seed_file'] = {'source': 'host_lstat_before_copy', 'size': after.st_size,
                                    'mode': oct(stat.S_IMODE(after.st_mode)), 'uid': after.st_uid, 'gid': after.st_gid,
                                    'regular_file': stat.S_ISREG(after.st_mode)}

    def copy_seed(self, directory):
        require(directory == self.work / 'seed', 'D2_SEED_PATH_INVALID')
        try:
            if os.geteuid() == 0:
                self.docker('cp', str(directory) + '/.', self.state['container'] + ':/data')
            else:
                # Only this copy needs root to read the newly root-owned 0600 file.
                self.call(['/usr/bin/sudo', '-n', '--', '/usr/bin/docker',
                           '--host=unix:///var/run/docker.sock', '--config=' + self.env['DOCKER_CONFIG'],
                           'cp', str(directory) + '/.', self.state['container'] + ':/data'], timeout=DIAGNOSTIC_TIMEOUT)
        except Exception:
            raise RuntimeError('D2_SEED_COPY_FAILED') from None

    def preflight_seed(self):
        preflight = {'status': 'UNKNOWN', 'scope': 'copied_seed_permissions_before_application_start'}
        self.report['seed_preflight'] = preflight
        try:
            evidence = self.storage_diagnostics()
            preflight['evidence'] = evidence
            measured = evidence.get('measurement', {})
            db = measured.get('metadata', {}).get('DB', {})
            require(all(key in db for key in ('uid', 'gid', 'mode', 'type', 'size'))
                    and all(key in measured for key in ('UID', 'GID', 'checks', 'access_status')),
                    'D2_SEED_PREFLIGHT_UNKNOWN')
            require(type(measured['UID']) is int and type(measured['GID']) is int
                    and measured['UID'] == 0 and measured['GID'] == 0, 'D2_SEED_PREFLIGHT_IDENTITY')
            require(type(db['uid']) is int and type(db['gid']) is int and db['uid'] == 0 and db['gid'] == 0,
                    'D2_SEED_PREFLIGHT_OWNER')
            require(db['mode'] == '600' and db['type'] == 'regular file' and type(db['size']) is int and db['size'] > 0,
                    'D2_SEED_PREFLIGHT_FILE')
            checks = measured['checks']
            require(measured['access_status'] == 'PASS' and all(checks.get(k) == 'PASS' for k in
                    ('mount_rw', 'file_exists', 'regular_database', 'directory', 'TRAVERSE', 'READ_ACCESS', 'WRITE_ACCESS', 'CREATE', 'REMOVE')),
                    'D2_SEED_PREFLIGHT_ACCESS')
            require(evidence.get('database_size_matches_seed') is True, 'D2_SEED_PREFLIGHT_SIZE')
            # Remove only this finished, owned probe so finally diagnostics can reuse its name.
            probe = self.state.get('probe')
            require(probe == self.state['owner'] + '-probe', 'D2_SEED_PREFLIGHT_PROBE_INVALID')
            item = self.resource('container', probe)
            require(item is not None and (item.get('Config', {}).get('Labels') or {}).get(LABEL) == self.state['owner']
                    and item.get('State', {}).get('Running') is False, 'D2_SEED_PREFLIGHT_PROBE_INVALID')
            self.docker('container', 'rm', probe, timeout=DIAGNOSTIC_TIMEOUT)
            preflight['status'] = 'PASS'
        except Exception as error:
            code = str(error) if re.fullmatch('D2_SEED_PREFLIGHT_[A-Z_]+', str(error)) else 'D2_SEED_PREFLIGHT_FAILED'
            preflight.update(status='FAIL', error_code=code)
            raise RuntimeError(code) from None

    def cleanup(self):
        state = self.state
        require(re.fullmatch('d2-accept-[a-f0-9]{24}', state.get('owner', ''))
                and state.get('container') == state['owner']
                and state.get('volume') == state['owner'] + '-data', 'D2_CLEANUP_STATE_INVALID')
        probe_error = False
        if state.get('probe'):
            try:
                require(state['probe'] == state['owner'] + '-probe', 'D2_CLEANUP_STATE_INVALID')
                item = self.resource('container', state['probe'])
                if item is not None:
                    require((item.get('Config', {}).get('Labels') or {}).get(LABEL) == state['owner'], 'D2_CLEANUP_OWNERSHIP')
                    self.docker('container', 'rm', '--force', state['probe'])
            except Exception:
                probe_error = True
        for kind, key in (('container', 'container'), ('volume', 'volume')):
            name = state[key]
            item = self.resource(kind, name)
            if item is None:
                continue
            labels = item.get('Config', {}).get('Labels', {}) if kind == 'container' else item.get('Labels', {})
            require(labels.get(LABEL) == state['owner'], 'D2_CLEANUP_OWNERSHIP')
            self.docker(kind, 'rm', *(['--force'] if kind == 'container' else []), name)
        require(not probe_error, 'D2_PROBE_CLEANUP_FAILED')


def main():
    trusted(os.environ)
    work = Path(os.environ['RUNNER_TEMP']) / ('d2-acceptance-' + os.environ['GITHUB_RUN_ID'] + '-' + os.environ['GITHUB_RUN_ATTEMPT'])
    work.mkdir(mode=0o700, exist_ok=True)
    job = Acceptance(work, os.environ)
    if sys.argv[1:] == ['--cleanup']:
        job.cleanup()
        return
    require(not sys.argv[1:], 'D2_ARGUMENTS_REJECTED')
    try:
        job.run()
    except Exception as error:
        code = str(error) if re.fullmatch('D2_[A-Z0-9_]+', str(error)) else 'D2_ACCEPTANCE_FAILED'
        job.report['phases'][job.phase] = 'failure'
        job.report['error_code'] = code
    finally:
        try:
            job.diagnose()
        except Exception:
            # Auxiliary evidence must never replace the original phase/error or skip cleanup.
            job.report['diagnostics'] = {'status': 'incomplete', 'errors': ['D2_DIAGNOSTIC_FAILED']}
        try:
            job.cleanup()
            job.report['phases']['cleanup'] = 'success'
        except Exception:
            job.report['phases']['cleanup'] = 'failure'
            job.report['cleanup_error'] = 'D2_CLEANUP_FAILED'
        report = work / 'result.json'
        report.write_text(json.dumps(job.report, ensure_ascii=False, indent=2))
        with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as summary:
            summary.write('## D2 固定 Digest 隔离验收\n')
            for phase, status in job.report['phases'].items():
                summary.write(f'- {phase}: {status}\n')
            summary.write('\n```json\n' + json.dumps(job.report, ensure_ascii=False, indent=2) + '\n```\n')
            if job.report.get('error_code'):
                summary.write('- ' + job.report['error_code'] + '\n')
    require(all(value == 'success' for value in job.report['phases'].values()), 'D2_ACCEPTANCE_INCOMPLETE')


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(str(error) if re.fullmatch('D2_[A-Z0-9_]+', str(error)) else 'D2_ACCEPTANCE_FAILED', file=sys.stderr)
        sys.exit(1)
