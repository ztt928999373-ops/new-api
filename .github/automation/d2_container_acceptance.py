"""Manual D2 acceptance of an existing image; never builds or publishes."""
import hashlib
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import secrets
import selectors
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time

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

    def container_logs(self):
        """Read separate streams with a combined byte ceiling and wall-clock timeout."""
        command = ['docker', '--host=unix:///var/run/docker.sock', 'logs', '--timestamps', '--tail=200',
                   self.state['container']]
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
        self.docker('cp', str(directory) + '/.', name + ':/data')
        self.passed()
        self.step('licenses')
        for filename, digest in LICENSES.items():
            target = self.work / filename
            self.docker('cp', name + ':/licenses/' + filename, str(target))
            require(hashlib.sha256(target.read_bytes()).hexdigest() == digest, 'D2_LICENSE_MISMATCH')
        self.passed()
        self.step('startup')
        self.docker('start', name)
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

    def cleanup(self):
        state = self.state
        require(re.fullmatch('d2-accept-[a-f0-9]{24}', state.get('owner', ''))
                and state.get('container') == state['owner']
                and state.get('volume') == state['owner'] + '-data', 'D2_CLEANUP_STATE_INVALID')
        for kind, key in (('container', 'container'), ('volume', 'volume')):
            name = state[key]
            item = self.resource(kind, name)
            if item is None:
                continue
            labels = item.get('Config', {}).get('Labels', {}) if kind == 'container' else item.get('Labels', {})
            require(labels.get(LABEL) == state['owner'], 'D2_CLEANUP_OWNERSHIP')
            self.docker(kind, 'rm', *(['--force'] if kind == 'container' else []), name)


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
