#!/usr/bin/env python3
"""Manual D3 orchestration. Importing this module never operates Docker."""
import argparse
import hashlib
import importlib.util
import json
import os
import platform
import re
import secrets
import signal
import shutil
import sqlite3
import stat
import subprocess
import sys
import tarfile
from pathlib import Path, PurePosixPath

REPO = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('d3_d2_library', REPO / '.github/automation/d2_container_acceptance.py')
D2 = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(D2)
IMAGE, REVISION, CONFIG = D2.IMAGE, D2.REVISION, D2.CONFIG
COMPOSE = REPO / 'deploy/d3/compose.yml'
PHASES = ('source', 'environment', 'image', 'resource_limits', 'seed', 'startup',
          'backup', 'restore', 'restored_startup', 'persistence', 'rollback', 'diagnostics', 'cleanup')
MIN_FREE = 5 * 1024**3
MAX_DATA = 256 * 1024**2
LABEL = 'io.suanjie.d3.owner'
STATUSES = ('success', 'failure', 'skipped', 'cancelled', 'not_run', 'unknown')
PROJECTION = '''{"id":{{json .Id}},"state":{{json .State}},"image":{{json .Image}},"user":{{json .Config.User}},"labels":{{json .Config.Labels}},"environment":{{json .Config.Env}},"mounts":[{{range $i,$m := .Mounts}}{{if $i}},{{end}}{"name":{{json $m.Name}},"type":{{json $m.Type}},"destination":{{json $m.Destination}},"rw":{{json $m.RW}}}{{end}}],"network":{{json .HostConfig.NetworkMode}},"readonly":{{json .HostConfig.ReadonlyRootfs}},"userns":{{json .HostConfig.UsernsMode}},"ports":{{json .HostConfig.PortBindings}},"caps":{{json .HostConfig.CapDrop}},"security":{{json .HostConfig.SecurityOpt}},"memory":{{json .HostConfig.Memory}},"nano_cpus":{{json .HostConfig.NanoCpus}},"pids":{{json .HostConfig.PidsLimit}}}'''
RESOURCE_PROBE = r'''
test "$(id -u):$(id -g)" = 0:0
printf 'memory=%s\n' "$(cat /sys/fs/cgroup/memory.max)"
printf 'cpu=%s\n' "$(cat /sys/fs/cgroup/cpu.max)"
printf 'pids=%s\n' "$(cat /sys/fs/cgroup/pids.max)"
'''
VOLUME_PROBE = r'''
export LC_ALL=C
umask 077
test -d /data && test ! -L /data && test -x /data
test -f /data/test.sqlite && test ! -L /data/test.sqlite
test "$(stat -c '%u:%g:%a:%F' /data/test.sqlite)" = '0:0:600:regular file'
test -r /data/test.sqlite && test -w /data/test.sqlite
exec 3<>/data/test.sqlite
exec 3>&-
probe=$(mktemp /data/.d3-access.XXXXXXXX)
trap 'rm -f -- "$probe"' EXIT
test -f "$probe"
rm -- "$probe"
trap - EXIT
printf 'access=PASS\n'
'''


def require(value, code):
    if not value:
        raise RuntimeError(code)


class Cancelled(RuntimeError):
    pass


def cancel(signum, frame):
    raise Cancelled('D3_CANCELLED')


def safe_code(error):
    code = str(error)
    return code if re.fullmatch('D3_[A-Z0-9_]+', code) else 'D3_OPERATION_FAILED'


def write_json(path, value):
    temporary = path.with_suffix('.pending')
    with temporary.open('w', encoding='utf-8') as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
    temporary.chmod(0o600)
    os.replace(temporary, path)


def inventory(directory):
    """Bound complete private snapshots; reject links, special files and rings."""
    result, total = {}, 0
    for path in directory.rglob('*'):
        metadata = path.lstat()
        require(not path.is_symlink() and 'ring' not in path.name.lower(), 'D3_UNSUPPORTED_DATA')
        if stat.S_ISDIR(metadata.st_mode):
            result[str(path.relative_to(directory))] = None
        else:
            require(stat.S_ISREG(metadata.st_mode) and metadata.st_nlink == 1, 'D3_UNSUPPORTED_DATA')
            total += metadata.st_size
            require(total <= MAX_DATA, 'D3_DATA_LIMIT')
            result[str(path.relative_to(directory))] = hashlib.sha256(path.read_bytes()).hexdigest()
        require(len(result) <= 10000, 'D3_DATA_LIMIT')
    return result


def check_database(directory, proof, initialized=True):
    inventory(directory)
    db = directory / 'test.sqlite'
    metadata = db.lstat()
    require(stat.S_ISREG(metadata.st_mode) and metadata.st_uid == metadata.st_gid == 0
            and stat.S_IMODE(metadata.st_mode) == 0o600, 'D3_DATABASE_OWNER_MODE')
    require((directory / 'd2-proof').read_text() == proof, 'D3_PROOF_MISMATCH')
    with sqlite3.connect(db.as_uri() + '?mode=ro', uri=True) as connection:
        require(connection.execute('PRAGMA integrity_check').fetchall() == [('ok',)], 'D3_DATABASE_INTEGRITY')
        for key in D2.OPTIONS:
            require(connection.execute('SELECT value FROM options WHERE key=?', (key,)).fetchone()
                    == ('false',), 'D3_POLICY_ENABLED')
        if initialized:
            require(connection.execute('SELECT count(*) FROM users').fetchone() == (0,), 'D3_USERS_PRESENT')
    if initialized:
        D2.database(directory, proof)


def check_limits(output, memory, cpus, pids):
    lines = output.splitlines()
    values = dict(line.split('=', 1) for line in lines)
    require(len(values) == len(lines) and set(values) >= {'memory', 'cpu', 'pids'}, 'D3_CGROUP_UNKNOWN')
    require(values['memory'] == str(memory) and values['pids'] == str(pids), 'D3_CGROUP_LIMIT_MISMATCH')
    quota, period = values['cpu'].split()
    require(quota.isdigit() and period.isdigit() and int(period) > 0
            and int(quota) * 1_000_000_000 == int(period) * cpus, 'D3_CGROUP_LIMIT_MISMATCH')


def check_runner(info, controller_text):
    require(info.get('OSType') == 'linux' and info.get('Architecture') in ('x86_64', 'amd64'), 'D3_PLATFORM')
    security = info.get('SecurityOptions')
    require(isinstance(security, list) and security and not any('rootless' in s for s in security), 'D3_ROOTLESS_REJECTED')
    require(info.get('CgroupVersion') == '2' and info.get('CgroupDriver') in ('systemd', 'cgroupfs'), 'D3_CGROUP_UNAVAILABLE')
    require(all(info.get(key) is True for key in ('MemoryLimit', 'CpuCfsQuota', 'PidsLimit'))
            and {'memory', 'cpu', 'pids'} <= set(controller_text.split()), 'D3_CGROUP_UNAVAILABLE')


def validate_archive(archive):
    entries = archive.getmembers()
    require(len(entries) <= 10000 and sum(x.size for x in entries) <= MAX_DATA, 'D3_ARCHIVE_LIMIT')
    names = set()
    for item in entries:
        parts = PurePosixPath(item.name).parts
        require(parts and parts[0] == 'data' and '..' not in parts and not item.name.startswith('/')
                and (item.isfile() or item.isdir()) and item.name not in names, 'D3_ARCHIVE_REJECTED')
        names.add(item.name)


class Acceptance:
    # Reuse only bounded collection/redaction, never D2 main or Acceptance initialization.
    redact = D2.Acceptance.redact
    container_logs = D2.Acceptance.container_logs

    def __init__(self, work, context, mode, expected_sha):
        self.work, self.context, self.mode = Path(work), dict(context), mode
        self.expected_sha = expected_sha
        self.private = self.work / 'private'
        self.env = {'PATH': '/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin', 'LC_ALL': 'C',
                    'DOCKER_HOST': 'unix:///var/run/docker.sock', 'DOCKER_CONFIG': str(self.private / 'anonymous-docker')}
        self.report = {'mode': mode, 'source_sha': expected_sha, 'image': IMAGE, 'image_revision': REVISION,
                       'config_digest': CONFIG, 'phases': dict.fromkeys(PHASES, 'not_run'), 'logs': {},
                       'outcome': 'not_run', 'limits_verified': False}
        self.state = {'run_id': context.get('GITHUB_RUN_ID'), 'attempt': context.get('GITHUB_RUN_ATTEMPT'),
                      'owner': 'suanjie-d3-' + secrets.token_hex(12), 'targets': [], 'probe': None}
        self.phase, self.proof, self.keys = 'source', None, None

    def call(self, command, timeout=120):
        result = subprocess.run(command, cwd=REPO, env=self.env, capture_output=True, text=True, timeout=timeout)
        require(result.returncode == 0, 'D3_COMMAND_FAILED')
        return result.stdout

    def docker(self, *args, timeout=120):
        return self.call(['docker', '--host=unix:///var/run/docker.sock', *args], timeout)

    def save(self):
        write_json(self.work / 'state.json', self.state)
        write_json(self.work / 'report.json', self.report)

    def stage(self, name, operation):
        self.phase = name
        self.report['phases'][name] = 'unknown'
        self.save()
        operation()
        self.report['phases'][name] = 'success'
        self.save()

    def source(self, live=True):
        c = self.context
        require(c.get('GITHUB_REPOSITORY') == 'ztt928999373-ops/new-api'
                and c.get('GITHUB_REF') == 'refs/heads/product/v1'
                and c.get('GITHUB_EVENT_NAME') == 'workflow_dispatch'
                and c.get('GITHUB_ACTOR') == c.get('GITHUB_TRIGGERING_ACTOR') == 'ztt928999373-ops', 'D3_UNTRUSTED_CONTEXT')
        require(re.fullmatch('[a-f0-9]{40}', self.expected_sha or '')
                and c.get('GITHUB_SHA') == self.expected_sha, 'D3_SOURCE_MISMATCH')
        require(c.get('RUNNER_ENVIRONMENT') == 'github-hosted' and c.get('RUNNER_OS') == 'Linux'
                and c.get('RUNNER_ARCH') == 'X64', 'D3_UNTRUSTED_RUNNER')
        head = self.call(['git', '-c', f'safe.directory={REPO}', 'rev-parse', 'HEAD']).strip()
        require(head == self.expected_sha, 'D3_SOURCE_MISMATCH')
        if live:
            remote = self.call(['git', 'ls-remote', '--exit-code', 'https://github.com/ztt928999373-ops/new-api.git',
                                'refs/heads/product/v1']).split()
            require(len(remote) == 2 and remote[0] == head, 'D3_MAIN_MOVED')

    def capacity(self, margin=0):
        require(shutil.disk_usage(self.root_dir).free >= MIN_FREE + margin, 'D3_DISK_INSUFFICIENT')
        require(shutil.disk_usage(self.work).free >= MIN_FREE + margin, 'D3_WORKSPACE_DISK_INSUFFICIENT')

    def environment(self):
        require(os.geteuid() == 0 and platform.system() == 'Linux' and platform.machine() == 'x86_64', 'D3_ROOT_REQUIRED')
        socket = Path('/var/run/docker.sock').stat()
        require(stat.S_ISSOCK(socket.st_mode) and socket.st_uid == 0, 'D3_LOCAL_SOCKET_REQUIRED')
        info = json.loads(self.docker('info', '--format', '{{json .}}'))
        check_runner(info, Path('/sys/fs/cgroup/cgroup.controllers').read_text())
        require(re.match(r'2\.', self.docker('compose', 'version', '--short').strip().removeprefix('v')), 'D3_COMPOSE_REQUIRED')
        self.root_dir = Path(info['DockerRootDir'])
        require(self.root_dir.is_absolute() and self.root_dir.is_dir(), 'D3_DOCKER_DISK_UNKNOWN')
        self.capacity(3 * 1024**3)
        self.report['environment'] = {key: info[key] for key in ('ServerVersion', 'OSType', 'Architecture',
                                      'CgroupVersion', 'CgroupDriver', 'MemoryLimit', 'CpuCfsQuota', 'PidsLimit', 'Driver')}
        self.report['environment'].update(docker_free_bytes=shutil.disk_usage(self.root_dir).free,
                                          work_free_bytes=shutil.disk_usage(self.work).free)

    def image(self):
        self.private.mkdir(mode=0o700)
        (self.private / 'anonymous-docker').mkdir(mode=0o700)
        self.docker('pull', IMAGE, timeout=300)
        fmt = '{"id":{{json .Id}},"digests":{{json .RepoDigests}},"os":{{json .Os}},"arch":{{json .Architecture}},"labels":{{json .Config.Labels}},"entrypoint":{{json .Config.Entrypoint}}}'
        image = json.loads(self.docker('image', 'inspect', '--format', fmt, IMAGE))
        require(image['id'] == CONFIG and IMAGE in image['digests'] and image['os'] == 'linux'
                and image['arch'] == 'amd64' and image['entrypoint'] == ['/new-api']
                and image['labels']['org.opencontainers.image.revision'] == REVISION, 'D3_IMAGE_MISMATCH')
        self.capacity(2 * 1024**3)

    def target_env(self, target):
        self.env.update(D3_PROJECT=target['project'], D3_DATA_VOLUME=target['volume'])

    def compose(self, target, *args):
        self.target_env(target)
        return self.docker('compose', '--env-file', str(self.private / 'keys/app.env'), '-f', str(COMPOSE), *args)

    def present(self, kind, name):
        # Listing errors are fatal, never confused with resource absence.
        if kind == 'container':
            return name in self.docker('ps', '-a', '--format', '{{.Names}}').splitlines()
        return name in self.docker('volume', 'ls', '--format', '{{.Name}}').splitlines()

    def volume_owned(self, target):
        data = json.loads(self.docker('volume', 'inspect', '--format', '{{json .}}', target['volume']))
        require(data.get('Labels', {}).get(LABEL) == target['project'] and data.get('Driver') == 'local'
                and not data.get('Options'), 'D3_VOLUME_OWNERSHIP')

    def inspect(self, name):
        return json.loads(self.docker('inspect', '--format', PROJECTION, name))

    def owned(self, target, probe=False):
        c = self.inspect(target['name'])
        labels = c.get('labels') or {}
        if probe:
            require(labels.get(LABEL) == target['project'] and labels.get('io.suanjie.d3.role') == 'probe', 'D3_CONTAINER_OWNERSHIP')
        else:
            require(labels.get('com.docker.compose.project') == target['project']
                    and labels.get('com.docker.compose.service') == 'app', 'D3_CONTAINER_OWNERSHIP')
        require(c['image'] == CONFIG, 'D3_IMAGE_MISMATCH')
        expected_mounts = [] if not target.get('volume') else [dict(name=target['volume'], type='volume', destination='/data', rw=True)]
        # tmpfs /tmp is an explicit app mount, not a data source.
        mounts = [m for m in c['mounts'] if m['destination'] != '/tmp']
        require(mounts == expected_mounts, 'D3_VOLUME_MISMATCH')
        if target.get('volume'):
            self.volume_owned(target)
        return c

    def check_target(self, target, status):
        c = self.owned(target)
        require(c['state']['Status'] == status and c['user'] == '0:0' and c['network'] == 'none'
                and c['readonly'] is True and c['userns'] == '' and not c['ports']
                and c['caps'] == ['ALL'] and 'no-new-privileges:true' in c['security'], 'D3_ISOLATION_MISMATCH')
        require(c['memory'] == 1024**3 and c['nano_cpus'] == 2_000_000_000 and c['pids'] == 256, 'D3_RESOURCE_CONFIGURATION')
        env = dict(line.split('=', 1) for line in c['environment'])
        require(env.get('SESSION_SECRET') == self.keys[0] and env.get('CRYPTO_SECRET') == self.keys[1]
                and env.get('SQL_DSN') == 'local' and env.get('SQLITE_PATH') == '/data/test.sqlite'
                and env.get('LOG_SQL_DSN') == env.get('REDIS_CONN_STRING') == '', 'D3_ENVIRONMENT_PAIRING')
        for other in self.state['targets']:
            if other['name'] != target['name'] and self.present('container', other['name']):
                require(not self.owned(other)['state']['Running'], 'D3_CONCURRENT_APPLICATIONS')
        return c

    def probe(self, project, volume=None):
        self.source()
        name = project + '-volume-check'
        require(not self.present('container', name), 'D3_RESOURCE_EXISTS')
        require(not any(self.present('container', t['name']) and self.owned(t)['state']['Running']
                        for t in self.state['targets']), 'D3_CONCURRENT_APPLICATIONS')
        target = dict(name=name, project=project, volume=volume)
        self.state['probe'] = target
        self.save()  # Record precise ownership before any create.
        memory, cpus, pids = (128 * 1024**2, 250_000_000, 32) if volume else (1024**3, 2_000_000_000, 256)
        args = ['create', '--name', name, '--pull=never', '--label', LABEL + '=' + project,
                '--label', 'io.suanjie.d3.role=probe', '--network=none', '--read-only', '--user=0:0',
                '--cap-drop=ALL', '--security-opt=no-new-privileges:true', '--log-driver=none',
                '--memory=' + str(memory), '--cpus=' + str(cpus / 1_000_000_000), '--pids-limit=' + str(pids)]
        if volume:
            self.volume_owned(target)
            args += ['--mount', 'type=volume,src=' + volume + ',dst=/data,volume-nocopy']
        args += ['--entrypoint=/bin/sh', IMAGE, '-eu', '-c', RESOURCE_PROBE + (VOLUME_PROBE if volume else '')]
        self.docker(*args)
        self.owned(target, probe=True)
        output = self.docker('start', '--attach', name, timeout=45)
        c = self.owned(target, probe=True)
        require(c['state']['Status'] == 'exited' and c['state']['ExitCode'] == 0, 'D3_PROBE_FAILED')
        check_limits(output, memory, cpus, pids)
        if volume:
            require(dict(line.split('=', 1) for line in output.splitlines()).get('access') == 'PASS', 'D3_VOLUME_ACCESS')
        self.docker('rm', name)
        self.state['probe'] = None
        self.save()

    def create_target(self, project, volume):
        target = dict(name=project + '-app-1', project=project, volume=volume)
        require(not self.present('container', target['name']) and not self.present('volume', volume), 'D3_RESOURCE_EXISTS')
        require(not self.docker('ps', '-aq', '--filter', 'label=com.docker.compose.project=' + project).strip(), 'D3_RESOURCE_EXISTS')
        resolved = json.loads(self.compose(target, 'config', '--format', 'json'))
        app = resolved['services']['app']
        require(set(resolved['services']) == {'app'} and resolved['name'] == project and app.get('image') == IMAGE
                and not app.get('build') and not app.get('ports') and app.get('pull_policy') == 'never'
                and resolved['volumes']['data'].get('external') is True
                and resolved['volumes']['data']['name'] == volume, 'D3_COMPOSE_MISMATCH')
        self.state['targets'].append(target)
        self.save()
        self.docker('volume', 'create', '--label', LABEL + '=' + project, volume)
        self.volume_owned(target)
        self.compose(target, 'create', '--no-build', '--pull', 'never', 'app')
        self.check_target(target, 'created')
        return target

    def snapshot(self, target, name, initialized=True):
        before = self.check_target(target, 'exited' if initialized else 'created')
        destination = self.private / name
        destination.mkdir(mode=0o700)
        self.docker('cp', '-a', target['name'] + ':/data/.', str(destination))
        after = self.check_target(target, before['state']['Status'])
        require(before['id'] == after['id'], 'D3_CONTAINER_CHANGED')
        check_database(destination, self.proof, initialized)
        return destination

    def seed(self):
        self.source()
        self.keys = (secrets.token_hex(32), secrets.token_hex(32))
        require(self.keys[0] != self.keys[1], 'D3_KEYS_COLLIDED')
        project = self.state['owner']
        self.env.update(D3_SESSION_SECRET=self.keys[0], D3_CRYPTO_SECRET=self.keys[1])
        keys = self.private / 'keys'
        keys.mkdir(mode=0o700)
        self.envfile = keys / 'app.env'
        self.envfile.write_text(f'D3_PROJECT={project}\nD3_DATA_VOLUME={project}-data\n'
                                f'D3_SESSION_SECRET={self.keys[0]}\nD3_CRYPTO_SECRET={self.keys[1]}\n')
        self.envfile.chmod(0o600)
        self.proof = secrets.token_hex(24)
        D2.seed(self.private / 'seed', self.proof)
        self.primary = self.create_target(project, project + '-data')
        self.docker('cp', str(self.private / 'seed') + '/.', self.primary['name'] + ':/data')
        self.probe(project, self.primary['volume'])
        actual = self.snapshot(self.primary, 'seed-readback', initialized=False)
        require(inventory(actual) == inventory(self.private / 'seed'), 'D3_COPY_MISMATCH')

    def start(self, target):
        self.source()
        status = self.owned(target)['state']['Status']
        require(status in ('created', 'exited'), 'D3_START_STATE')
        self.check_target(target, status)
        self.compose(target, 'up', '-d', '--no-build', '--no-recreate', '--pull', 'never', '--wait', '--wait-timeout', '120', 'app')
        c = self.check_target(target, 'running')
        require(c['state'].get('Health', {}).get('Status') == 'healthy', 'D3_HEALTH_FAILED')
        body = self.docker('exec', target['name'], 'wget', '-q', '-T', '5', '-O', '-', 'http://127.0.0.1:3000/api/status')
        require(json.loads(body).get('success') is True, 'D3_HEALTH_FAILED')
        check_limits(self.docker('exec', target['name'], '/bin/sh', '-eu', '-c', RESOURCE_PROBE), 1024**3, 2_000_000_000, 256)
        require(self.docker('exec', target['name'], 'cat', '/data/d2-proof').strip() == self.proof, 'D3_PROOF_MISMATCH')

    def stop(self, target):
        self.check_target(target, 'running')
        self.docker('stop', '--time=30', target['name'], timeout=45)
        c = self.check_target(target, 'exited')
        require(c['state']['ExitCode'] == 0, 'D3_STOP_FAILED')

    def backup(self):
        self.stop(self.primary)
        self.backup_data = self.snapshot(self.primary, 'backup-data')
        self.backup_inventory = inventory(self.backup_data)
        self.archive = self.private / 'data.tar'
        with tarfile.open(self.archive, 'w') as archive:
            archive.add(self.backup_data, arcname='data')
        self.manifest = dict(image=IMAGE, config_digest=CONFIG, image_revision=REVISION,
                             preparation_sha=self.expected_sha, source_project=self.primary['project'],
                             source_volume=self.primary['volume'], compose_sha256=hashlib.sha256(COMPOSE.read_bytes()).hexdigest(),
                             data_sha256=hashlib.sha256(self.archive.read_bytes()).hexdigest(),
                             environment_sha256=hashlib.sha256(self.envfile.read_bytes()).hexdigest())
        write_json(self.private / 'manifest.json', self.manifest)
        self.report['backup'] = {key: self.manifest[key] for key in ('preparation_sha', 'compose_sha256', 'data_sha256')}
        self.capacity()

    def restore(self):
        m = json.loads((self.private / 'manifest.json').read_text())
        require(m == self.manifest and hashlib.sha256(self.archive.read_bytes()).hexdigest() == m['data_sha256']
                and hashlib.sha256(self.envfile.read_bytes()).hexdigest() == m['environment_sha256']
                and hashlib.sha256(COMPOSE.read_bytes()).hexdigest() == m['compose_sha256'], 'D3_BACKUP_PAIRING')
        root = self.private / 'restore'
        root.mkdir(mode=0o700)
        with tarfile.open(self.archive) as archive:
            validate_archive(archive)
            archive.extractall(root, filter='data')
        require(inventory(root / 'data') == self.backup_inventory, 'D3_BACKUP_PAIRING')
        self.restored = self.create_target(self.primary['project'] + '-restore', self.primary['volume'] + '-restore')
        self.docker('cp', str(root / 'data') + '/.', self.restored['name'] + ':/data')
        self.probe(self.restored['project'], self.restored['volume'])
        copied = self.snapshot(self.restored, 'restore-readback', initialized=False)
        check_database(copied, self.proof)
        require(inventory(copied) == self.backup_inventory, 'D3_COPY_MISMATCH')

    def persistence(self):
        self.stop(self.restored)
        self.start(self.restored)
        self.stop(self.restored)
        self.probe(self.restored['project'], self.restored['volume'])
        self.snapshot(self.restored, 'restored-final')

    def rollback(self):
        require(inventory(self.snapshot(self.primary, 'rollback-readback')) == self.backup_inventory, 'D3_ORIGINAL_CHANGED')
        self.probe(self.primary['project'], self.primary['volume'])
        self.start(self.primary)
        self.stop(self.primary)
        self.snapshot(self.primary, 'rollback-final')
        self.capacity()

    def diagnostics(self):
        for target in self.state['targets']:
            if self.present('container', target['name']):
                self.owned(target)
                logs = self.container_logs(target['name'])
                self.report['logs'][target['project']] = logs
                require(not logs['error_code'] and not logs['truncated'], 'D3_LOG_EVIDENCE_INCOMPLETE')

    def cleanup(self):
        failed = False
        probe = self.state.get('probe')
        for target in ([probe] if probe else []) + list(reversed(self.state['targets'])):
            try:
                if self.present('container', target['name']):
                    self.owned(target, probe=(target == probe))
                    self.docker('rm', '--force', target['name'])
            except Exception:
                failed = True
        for target in reversed(self.state['targets']):
            try:
                if self.present('volume', target['volume']):
                    self.volume_owned(target)
                    self.docker('volume', 'rm', target['volume'])
            except Exception:
                failed = True
        require(not failed, 'D3_CLEANUP_UNCONFIRMED')
        if self.private.exists():
            shutil.rmtree(self.private)

    def run(self):
        try:
            self.stage('source', self.source)
            self.stage('environment', self.environment)
            if self.mode == 'full':
                self.stage('image', self.image)
                self.stage('resource_limits', lambda: self.probe(self.state['owner']))
                self.report['limits_verified'] = True
                self.stage('seed', self.seed)
                self.stage('startup', lambda: self.start(self.primary))
                self.stage('backup', self.backup)
                self.stage('restore', self.restore)
                self.stage('restored_startup', lambda: self.start(self.restored))
                self.stage('persistence', self.persistence)
                self.stage('rollback', self.rollback)
        except Exception as error:
            self.report['phases'][self.phase] = 'cancelled' if isinstance(error, Cancelled) else 'failure'
            self.report['error_code'] = safe_code(error)
        finally:
            if self.mode == 'full':
                for phase, operation in [('diagnostics', self.diagnostics), ('cleanup', self.cleanup)]:
                    try:
                        self.stage(phase, operation)
                    except Exception as error:
                        self.report['phases'][phase] = 'cancelled' if isinstance(error, Cancelled) else 'failure'
                        self.report[phase + '_error'] = safe_code(error)
            required = PHASES if self.mode == 'full' else ('source', 'environment')
            success = all(self.report['phases'][p] == 'success' for p in required)
            if self.mode == 'preflight':
                for phase in PHASES[2:]:
                    self.report['phases'][phase] = 'skipped'
                self.report['skip_reason'] = 'preflight_only_no_docker_resources'
            self.report['outcome'] = ('d3_pass' if self.mode == 'full' else 'preflight_pass') if success else 'failure'
            if 'cancelled' in self.report['phases'].values():
                self.report['outcome'] = 'cancelled'
            self.save()
        return self.report['outcome'] in ('preflight_pass', 'd3_pass')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--mode', choices=('preflight', 'full'), default='preflight')
    parser.add_argument('--expected-sha', required=True)
    parser.add_argument('--confirmation', default='')
    parser.add_argument('--cleanup', action='store_true')
    args = parser.parse_args()
    require(not os.environ.get('PYTHONOPTIMIZE'), 'D3_OPTIMIZED_PYTHON_REJECTED')
    require(re.fullmatch('[0-9]+', os.environ.get('GITHUB_RUN_ID', ''))
            and re.fullmatch('[0-9]+', os.environ.get('GITHUB_RUN_ATTEMPT', '')), 'D3_UNTRUSTED_CONTEXT')
    work = Path(os.environ['RUNNER_TEMP']) / ('d3-' + os.environ['GITHUB_RUN_ID'] + '-' + os.environ['GITHUB_RUN_ATTEMPT'])
    job = Acceptance(work, os.environ, args.mode, args.expected_sha)
    if args.cleanup:
        # No live-main check: main advancing must never prevent cleanup of this run.
        job.source(live=False)
        require(work.is_dir() and not work.is_symlink(), 'D3_STATE_MISSING')
        state = json.loads((work / 'state.json').read_text())
        owner = state.get('owner', '')
        require(re.fullmatch('suanjie-d3-[a-f0-9]{24}', owner) and state.get('run_id') == os.environ['GITHUB_RUN_ID']
                and state.get('attempt') == os.environ['GITHUB_RUN_ATTEMPT'], 'D3_STATE_REJECTED')
        for target in state.get('targets', []) + ([state['probe']] if state.get('probe') else []):
            require(target['project'] in (owner, owner + '-restore') and target['name'] in
                    (target['project'] + '-app-1', target['project'] + '-volume-check')
                    and target.get('volume') in (None, target['project'] + '-data', owner + '-data-restore'), 'D3_STATE_REJECTED')
        job.state = state
        job.cleanup()
        return
    require(args.mode != 'full' or args.confirmation == 'D3-SYNTHETIC-ONLY', 'D3_APPROVAL_REQUIRED')
    os.umask(0o077)
    work.mkdir(mode=0o700, exist_ok=False)
    signal.signal(signal.SIGTERM, cancel)
    signal.signal(signal.SIGINT, cancel)
    require(job.run(), 'D3_ACCEPTANCE_INCOMPLETE')


if __name__ == '__main__':
    try:
        main()
    except Exception as failure:
        print(safe_code(failure), file=sys.stderr)
        raise SystemExit(1)
