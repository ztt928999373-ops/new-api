"""Offline safety regressions. No test contacts a Docker daemon or registry."""
import contextlib
import copy
import hashlib
import io
import json
import os
import re
import shutil
import sqlite3
import stat
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import acceptance as d3

SHA = 'a' * 40
INFO = dict(OSType='linux', Architecture='x86_64', SecurityOptions=['name=seccomp'],
            CgroupVersion='2', CgroupDriver='systemd', MemoryLimit=True, CpuCfsQuota=True,
            PidsLimit=True, ServerVersion='29.0.0', Driver='overlay2')


class FakeDocker:
    """External Docker boundary backed by private local fixtures, never a daemon."""
    def __init__(self, root, corrupt_restore=False):
        self.root, self.corrupt_restore = root, corrupt_restore
        self.containers, self.volumes, self.calls = {}, {}, []
        self.stopped_hashes, self.removed_hashes = {}, {}

    def resource_output(self, container):
        return (f"memory={container['memory']}\ncpu={container['nano_cpus']//10000} 100000\n"
                f"pids={container['pids']}\n")

    def new_container(self, name, project, volume, env, probe=False, limits=None):
        memory, cpus, pids = limits or (1024**3, 2_000_000_000, 256)
        labels = {d3.LABEL: project, 'io.suanjie.d3.role': 'probe'} if probe else {
            'com.docker.compose.project': project, 'com.docker.compose.service': 'app'}
        c = dict(id='c' * 64, state=dict(Status='created', Running=False, ExitCode=0), image=d3.CONFIG,
                 labels=labels, user='0:0', network='none', readonly=True, userns='', ports={},
                 caps=['ALL'], security=['no-new-privileges:true'], memory=memory, nano_cpus=cpus, pids=pids,
                 environment=['SESSION_SECRET=' + env.get('D3_SESSION_SECRET', ''),
                              'CRYPTO_SECRET=' + env.get('D3_CRYPTO_SECRET', ''), 'SQL_DSN=local',
                              'SQLITE_PATH=/data/test.sqlite', 'LOG_SQL_DSN=', 'REDIS_CONN_STRING='],
                 mounts=[dict(name=volume, type='volume', destination='/data', rw=True)] if volume else [])
        self.containers[name] = c
        return c

    def run(self, command, **kwargs):
        self.calls.append(command)
        env = kwargs['env']
        if command[0] == 'git':
            out = SHA + ('\trefs/heads/product/v1\n' if 'ls-remote' in command else '\n')
            return mock.Mock(returncode=0, stdout=out)
        assert command[:2] == ['docker', '--host=unix:///var/run/docker.sock']
        a = command[2:]
        if a[0] == 'info':out = json.dumps(INFO | dict(DockerRootDir=str(self.root)))
        elif a[:3] == ['compose', 'version', '--short']:out = '2.40.3'
        elif a == ['pull', d3.IMAGE]:out = ''
        elif a[:2] == ['image', 'inspect']:
            out = json.dumps(dict(id=d3.CONFIG, digests=[d3.IMAGE], os='linux', arch='amd64',
                                  labels={'org.opencontainers.image.revision': d3.REVISION}, entrypoint=['/new-api']))
        elif a[:2] == ['volume', 'ls']:out = '\n'.join(self.volumes)
        elif a[:2] == ['volume', 'create']:
            name = a[-1];owner = a[a.index('--label') + 1].split('=', 1)[1]
            directory = self.root / name;directory.mkdir()
            self.volumes[name] = dict(path=directory, owner=owner);out = name
        elif a[:2] == ['volume', 'inspect']:
            out = json.dumps(dict(Driver='local', Options=None, Labels={d3.LABEL: self.volumes[a[-1]]['owner']}))
        elif a[:2] == ['volume', 'rm']:
            volume = self.volumes.pop(a[-1]);db = volume['path'] / 'test.sqlite'
            self.removed_hashes[a[-1]] = hashlib.sha256(db.read_bytes()).hexdigest()
            shutil.rmtree(volume['path']);out = ''
        elif a[0] == 'ps':
            names = list(self.containers)
            if '--filter' in a:
                project = a[a.index('--filter') + 1].split('=', 2)[-1]
                names = [n for n in names if self.containers[n]['labels'].get('com.docker.compose.project') == project]
            out = '\n'.join(names)
        elif a[0] == 'inspect':out = json.dumps(self.containers[a[-1]])
        elif a[0] == 'compose':
            operation = a[a.index('-f') + 2:]
            project, volume = env['D3_PROJECT'], env['D3_DATA_VOLUME'];name = project + '-app-1'
            if operation[0] == 'config':
                out = json.dumps(dict(name=project, services={'app': dict(image=d3.IMAGE, pull_policy='never')},
                                      volumes={'data': dict(name=volume, external=True)}))
            elif operation[0] == 'create':self.new_container(name, project, volume, env);out = ''
            elif operation[0] == 'up':
                c = self.containers[name];c['state'].update(Status='running', Running=True, Health={'Status': 'healthy'})
                # Minimal external app fixture: tables needed by the existing database verifier.
                with sqlite3.connect(self.volumes[volume]['path'] / 'test.sqlite') as db:
                    db.execute('CREATE TABLE IF NOT EXISTS channels (id INTEGER)')
                    db.execute('CREATE TABLE IF NOT EXISTS users (username TEXT)')
                out = ''
            else:raise AssertionError(a)
        elif a[0] == 'create':
            name = a[a.index('--name') + 1]
            project = a[a.index('--label') + 1].split('=', 1)[1]
            volume = None
            if '--mount' in a:volume = a[a.index('--mount') + 1].split('src=', 1)[1].split(',')[0]
            limits = (int(next(x.split('=', 1)[1] for x in a if x.startswith('--memory='))),
                      int(float(next(x.split('=', 1)[1] for x in a if x.startswith('--cpus='))) * 1e9),
                      int(next(x.split('=', 1)[1] for x in a if x.startswith('--pids-limit='))))
            self.new_container(name, project, volume, env, probe=True, limits=limits);out = name
        elif a[:2] == ['start', '--attach']:
            c = self.containers[a[-1]];c['state'].update(Status='exited', Running=False)
            out = self.resource_output(c)
            if c['mounts']:
                assert (self.volumes[c['mounts'][0]['name']]['path'] / 'test.sqlite').is_file()
                out += 'access=PASS\n'
        elif a[0] == 'cp':
            source, destination = a[-2:]
            if ':' in source:
                c = self.containers[source.split(':')[0]]
                source = self.volumes[c['mounts'][0]['name']]['path']
                shutil.copytree(source, destination, dirs_exist_ok=True)
            else:
                c = self.containers[destination.split(':')[0]]
                path = self.volumes[c['mounts'][0]['name']]['path']
                shutil.copytree(source, path, dirs_exist_ok=True)
                if self.corrupt_restore and '-restore-app-1' in destination:
                    (path / 'test.sqlite').write_bytes(b'corrupted after Docker copy')
            out = ''
        elif a[0] == 'exec':
            c = self.containers[a[1]]
            if a[2] == 'wget':out = '{"success":true}'
            elif a[2] == 'cat':out = (self.volumes[c['mounts'][0]['name']]['path'] / 'd2-proof').read_text()
            elif a[2] == '/bin/sh':out = self.resource_output(c)
            else:raise AssertionError(a)
        elif a[0] == 'stop':
            c = self.containers[a[-1]];c['state'].update(Status='exited', Running=False)
            volume = c['mounts'][0]['name']
            self.stopped_hashes.setdefault(volume, hashlib.sha256((self.volumes[volume]['path']/'test.sqlite').read_bytes()).hexdigest())
            out = ''
        elif a[0] == 'rm':self.containers.pop(a[-1]);out = ''
        else:raise AssertionError(a)
        return mock.Mock(returncode=0, stdout=out)


class AcceptanceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.work = self.root / 'work'
        self.work.mkdir()
        self.context = dict(GITHUB_REPOSITORY='ztt928999373-ops/new-api', GITHUB_REF='refs/heads/product/v1',
                            GITHUB_EVENT_NAME='workflow_dispatch', GITHUB_SHA=SHA, GITHUB_ACTOR='ztt928999373-ops',
                            GITHUB_TRIGGERING_ACTOR='ztt928999373-ops', RUNNER_ENVIRONMENT='github-hosted',
                            RUNNER_OS='Linux', RUNNER_ARCH='X64', GITHUB_RUN_ID='123', GITHUB_RUN_ATTEMPT='1')
        self.job = d3.Acceptance(self.work, self.context, 'preflight', SHA)
        self.owner = 'suanjie-d3-' + 'b' * 24
        self.job.state['owner'] = self.owner
        self.target = dict(name=self.owner + '-app-1', project=self.owner, volume=self.owner + '-data')

    def container(self, target=None):
        t = target or self.target
        return dict(id='c' * 64, state=dict(Status='created', Running=False, ExitCode=0), image=d3.CONFIG,
                    user='0:0', labels={'com.docker.compose.project': t['project'], 'com.docker.compose.service': 'app'},
                    environment=['SESSION_SECRET=' + '1' * 64, 'CRYPTO_SECRET=' + '2' * 64,
                                 'SQL_DSN=local', 'SQLITE_PATH=/data/test.sqlite', 'LOG_SQL_DSN=', 'REDIS_CONN_STRING='],
                    mounts=[dict(name=t['volume'], type='volume', destination='/data', rw=True)],
                    network='none', readonly=True, userns='', ports={}, caps=['ALL'],
                    security=['no-new-privileges:true'], memory=1024**3, nano_cpus=2_000_000_000, pids=256)

    @contextlib.contextmanager
    def runner_boundaries(self, info=None, pull_error=False):
        """Mock only external git, OS and Docker observations, execute real gates."""
        info = dict(INFO if info is None else info, DockerRootDir=str(self.root))
        original_stat, original_read = Path.stat, Path.read_text
        commands = []

        def path_stat(path, *args, **kwargs):
            if str(path) == '/var/run/docker.sock':
                return mock.Mock(st_mode=stat.S_IFSOCK | 0o660, st_uid=0)
            return original_stat(path, *args, **kwargs)

        def path_read(path, *args, **kwargs):
            if str(path) == '/sys/fs/cgroup/cgroup.controllers':
                return 'cpu memory pids'
            return original_read(path, *args, **kwargs)

        def run(command, **kwargs):
            commands.append(command)
            if command[0] == 'git':
                out = SHA + ('\trefs/heads/product/v1\n' if 'ls-remote' in command else '\n')
            elif command[2:] == ['info', '--format', '{{json .}}']:
                out = json.dumps(info)
            elif command[2:] == ['compose', 'version', '--short']:
                out = '2.40.3\n'
            elif command[2:] == ['pull', d3.IMAGE] and pull_error:
                return mock.Mock(returncode=1, stdout='', stderr='sensitive registry failure')
            else:
                raise AssertionError('Unexpected external command: ' + repr(command))
            return mock.Mock(returncode=0, stdout=out)

        with mock.patch('acceptance.subprocess.run', side_effect=run), \
                mock.patch('acceptance.os.geteuid', return_value=0), \
                mock.patch('acceptance.platform.system', return_value='Linux'), \
                mock.patch('acceptance.platform.machine', return_value='x86_64'), \
                mock.patch.object(Path, 'stat', path_stat), mock.patch.object(Path, 'read_text', path_read), \
                mock.patch('acceptance.shutil.disk_usage', return_value=mock.Mock(free=20 * 1024**3)):
            yield commands

    def test_preflight_never_creates_resources_or_claims_d3_pass(self):
        with self.runner_boundaries() as commands:
            self.assertTrue(self.job.run())
        self.assertEqual(self.job.report['outcome'], 'preflight_pass')
        self.assertEqual(set(self.job.report['phases'].values()), {'success', 'skipped'})
        self.assertFalse(self.job.private.exists())
        self.assertFalse(any('pull' in cmd or 'create' in cmd or 'start' in cmd for cmd in commands))

    def test_rootless_full_run_stops_before_pull(self):
        self.job.mode = 'full'
        with self.runner_boundaries(INFO | dict(SecurityOptions=['name=rootless'])) as commands:
            self.assertFalse(self.job.run())
        self.assertEqual(self.job.report['error_code'], 'D3_ROOTLESS_REJECTED')
        self.assertEqual(self.job.report['phases']['image'], 'not_run')
        self.assertFalse(any('pull' in cmd or 'create' in cmd for cmd in commands))

    def test_pull_failure_keeps_future_phases_not_run_and_sanitizes_error(self):
        self.job.mode = 'full'
        with self.runner_boundaries(pull_error=True):
            self.assertFalse(self.job.run())
        self.assertEqual(self.job.report['phases']['image'], 'failure')
        self.assertEqual(self.job.report['phases']['restore'], 'not_run')
        self.assertNotIn('sensitive registry failure', json.dumps(self.job.report))
        self.assertEqual(self.job.report['phases']['cleanup'], 'success')

    def test_source_rejects_other_refs_actors_and_main_movement(self):
        for key, value in [('GITHUB_REF', 'refs/heads/other'), ('GITHUB_ACTOR', 'other'),
                           ('GITHUB_TRIGGERING_ACTOR', 'other'), ('GITHUB_SHA', 'c' * 40),
                           ('GITHUB_EVENT_NAME', 'pull_request'), ('RUNNER_ENVIRONMENT', 'self-hosted')]:
            with self.subTest(key=key):
                context = self.context | {key: value}
                job = d3.Acceptance(self.work, context, 'preflight', SHA)
                with mock.patch.object(job, 'call') as call:
                    with self.assertRaises(RuntimeError):
                        job.source()
                    call.assert_not_called()
        with mock.patch.object(self.job, 'call', side_effect=[SHA, 'c' * 40 + '\trefs/heads/product/v1']):
            with self.assertRaisesRegex(RuntimeError, 'D3_MAIN_MOVED'):
                self.job.source()

    def test_environment_and_disk_gates_fail_closed(self):
        for delta in [dict(CgroupDriver='none'), dict(CgroupVersion='1'), dict(MemoryLimit=False),
                      dict(CpuCfsQuota=False), dict(PidsLimit=False), dict(Architecture='arm64')]:
            with self.subTest(delta=delta), self.assertRaises(RuntimeError):
                d3.check_runner(INFO | delta, 'cpu memory pids')
        with self.assertRaisesRegex(RuntimeError, 'D3_CGROUP_UNAVAILABLE'):
            d3.check_runner(INFO, 'cpu memory')
        self.job.root_dir = self.root
        with mock.patch('acceptance.shutil.disk_usage', return_value=mock.Mock(free=d3.MIN_FREE - 1)):
            with self.assertRaisesRegex(RuntimeError, 'D3_DISK_INSUFFICIENT'):
                self.job.capacity()

    def test_actual_cgroup_values_required_not_just_requested_flags(self):
        good = 'memory=134217728\ncpu=25000 100000\npids=32\n'
        d3.check_limits(good, 134217728, 250_000_000, 32)
        for bad in [good.replace('134217728', 'max'), good.replace('25000', 'max'),
                    good.replace('pids=32', 'pids=max'), good + 'memory=134217728\n']:
            with self.subTest(output=bad), self.assertRaises((RuntimeError, ValueError)):
                d3.check_limits(bad, 134217728, 250_000_000, 32)

    def test_phase_is_unknown_until_completed_and_cancellation_not_success(self):
        with self.assertRaises(d3.Cancelled):
            self.job.stage('environment', lambda: d3.cancel(None, None))
        saved = json.loads((self.work / 'report.json').read_text())
        self.assertEqual(saved['phases']['environment'], 'unknown')
        with mock.patch.object(self.job, 'source', side_effect=d3.Cancelled('D3_CANCELLED')):
            self.assertFalse(self.job.run())
        self.assertEqual(self.job.report['outcome'], 'cancelled')
        self.assertEqual(self.job.report['phases']['source'], 'cancelled')

    def test_pairing_mount_isolation_and_concurrent_apps_rejected(self):
        self.job.keys = ('1' * 64, '2' * 64)
        cases = [dict(user='1000:1000'), dict(network='bridge'), dict(readonly=False), dict(userns='host'),
                 dict(memory=0), dict(ports={'3000/tcp': []}),
                 dict(mounts=[dict(name='foreign', type='volume', destination='/data', rw=True)]),
                 dict(environment=['SESSION_SECRET=wrong', 'CRYPTO_SECRET=' + '2' * 64])]
        for delta in [{}] + cases:
            c = self.container() | delta
            def docker(*args, **kwargs):
                if args[0] == 'inspect':return json.dumps(c)
                if args[:2] == ('volume', 'inspect'):return json.dumps(dict(Labels={d3.LABEL: self.owner}, Driver='local', Options=None))
                raise AssertionError(args)
            with self.subTest(delta=delta), mock.patch.object(self.job, 'docker', side_effect=docker):
                if delta:
                    with self.assertRaises((RuntimeError, KeyError)):self.job.check_target(self.target, 'created')
                else:self.job.check_target(self.target, 'created')

    def test_inventory_detects_copy_loss_corruption_extra_and_links(self):
        source, target = self.root / 'source', self.root / 'target'
        source.mkdir(); target.mkdir()
        (source / 'test.sqlite').write_bytes(b'original')
        (target / 'test.sqlite').write_bytes(b'original')
        expected = d3.inventory(source)
        self.assertEqual(expected, d3.inventory(target))
        (target / 'test.sqlite').write_bytes(b'corrupt')
        self.assertNotEqual(expected, d3.inventory(target))
        (target / 'test.sqlite').unlink()
        self.assertNotEqual(expected, d3.inventory(target))
        (target / 'test.sqlite').symlink_to(source / 'test.sqlite')
        with self.assertRaisesRegex(RuntimeError, 'D3_UNSUPPORTED_DATA'):d3.inventory(target)
        (target / 'test.sqlite').unlink()
        (target / 'supplier.ring').write_bytes(b'not-supported')
        with self.assertRaisesRegex(RuntimeError, 'D3_UNSUPPORTED_DATA'):d3.inventory(target)

    def test_archive_rejects_escape_links_duplicates_and_special_files(self):
        for name, kind in [('../escape', tarfile.REGTYPE), ('/data/escape', tarfile.REGTYPE),
                           ('data/link', tarfile.SYMTYPE), ('data/hard', tarfile.LNKTYPE),
                           ('data/device', tarfile.CHRTYPE)]:
            with self.subTest(name=name), tarfile.open(fileobj=io.BytesIO(), mode='w') as archive:
                entry = tarfile.TarInfo(name); entry.type = kind
                archive.addfile(entry)
                with self.assertRaises(RuntimeError):d3.validate_archive(archive)
        with tarfile.open(fileobj=io.BytesIO(), mode='w') as archive:
            for _ in range(2):archive.addfile(tarfile.TarInfo('data/file'))
            with self.assertRaises(RuntimeError):d3.validate_archive(archive)

    def test_changed_env_or_archive_rejected_before_restore_resource_creation(self):
        self.job.private.mkdir()
        self.job.envfile = self.job.private / 'app.env'
        self.job.archive = self.job.private / 'data.tar'
        self.job.envfile.write_bytes(b'synthetic-pair')
        self.job.archive.write_bytes(b'synthetic-archive')
        self.job.manifest = dict(data_sha256=hashlib.sha256(self.job.archive.read_bytes()).hexdigest(),
                                 environment_sha256=hashlib.sha256(self.job.envfile.read_bytes()).hexdigest(),
                                 compose_sha256=hashlib.sha256(d3.COMPOSE.read_bytes()).hexdigest())
        d3.write_json(self.job.private / 'manifest.json', self.job.manifest)
        for path in [self.job.envfile, self.job.archive]:
            original = path.read_bytes();path.write_bytes(b'changed')
            with mock.patch.object(self.job, 'docker') as docker:
                with self.assertRaisesRegex(RuntimeError, 'D3_BACKUP_PAIRING'):self.job.restore()
                docker.assert_not_called()
            path.write_bytes(original)

    def test_cleanup_keeps_foreign_container_and_continues_owned_resources(self):
        other = dict(name=self.owner + '-restore-app-1', project=self.owner + '-restore', volume=self.owner + '-data-restore')
        self.job.state['targets'] = [self.target, other]
        calls = []
        def docker(*args, **kwargs):
            calls.append(args)
            if args[0] == 'ps':return self.target['name'] + '\n' + other['name']
            if args[:2] == ('volume', 'ls'):return ''
            if args[0] == 'inspect':
                t = self.target if args[-1] == self.target['name'] else other
                c = self.container(t)
                if t == self.target:c['labels']['com.docker.compose.project'] = 'foreign'
                return json.dumps(c)
            if args[:2] == ('volume', 'inspect'):return json.dumps(dict(Labels={d3.LABEL: other['project']}, Driver='local', Options=None))
            if args[:2] == ('rm', '--force'):return ''
            raise AssertionError(args)
        with mock.patch.object(self.job, 'docker', side_effect=docker):
            with self.assertRaisesRegex(RuntimeError, 'D3_CLEANUP_UNCONFIRMED'):self.job.cleanup()
        self.assertIn(('rm', '--force', other['name']), calls)
        self.assertNotIn(('rm', '--force', self.target['name']), calls)

    def test_cleanup_daemon_error_is_not_resource_absence(self):
        self.job.state['targets'] = [self.target]
        with mock.patch.object(self.job, 'docker', side_effect=RuntimeError('D3_COMMAND_FAILED')):
            with self.assertRaisesRegex(RuntimeError, 'D3_CLEANUP_UNCONFIRMED'):self.job.cleanup()
        with mock.patch.object(self.job, 'docker', return_value='') as docker:
            self.job.cleanup()
        self.assertFalse(any(call.args[0] == 'rm' for call in docker.call_args_list))

    def test_redaction_handles_keys_private_keys_urls_and_control_sequences(self):
        value = '3' * 64
        self.job.env['D3_SESSION_SECRET'] = value
        raw = value + '\nPASSWORD=hidden\nhttps://user:password@example.org/path\n' \
              '-----BEGIN PRIVATE KEY-----\nnever-public\n-----END PRIVATE KEY-----\n\x1b[31msecret=other'
        safe = self.job.redact(raw)
        for forbidden in [value, 'hidden', 'user:password', 'never-public', 'secret=other']:
            self.assertNotIn(forbidden, safe)
        self.assertEqual(d3.safe_code(ValueError(raw)), 'D3_OPERATION_FAILED')

    def test_workflow_is_manual_owner_only_and_does_not_forward_tokens(self):
        text = (d3.REPO / '.github/workflows/v1-d3-isolated-acceptance.yml').read_text()
        self.assertIn('on:\n  workflow_dispatch:', text)
        self.assertIn('permissions:\n  contents: read\nconcurrency:', text)
        self.assertIn('default: preflight', text)
        self.assertIn("github.ref == 'refs/heads/product/v1'", text)
        self.assertIn('github.triggering_actor == github.repository_owner', text)
        self.assertIn('runs-on: ubuntu-24.04', text)
        for forbidden in ['pull_request:', 'push:', 'schedule:', 'secrets.', 'packages:', 'sudo -E', 'continue-on-error:', 'docker prune']:
            self.assertNotIn(forbidden, text)

    def test_workflow_export_marks_cancelled_and_missing_evidence_without_secrets(self):
        text = (d3.REPO / '.github/workflows/v1-d3-isolated-acceptance.yml').read_text()
        # Execute the actual embedded export code with local files only.
        code = next(x for x in re.findall(r"<<'PY'\n(.*?)\n\s*PY", text, re.S)
                    if 'workflow_status_before_upload' in x)
        import textwrap
        code = textwrap.dedent(code)
        for status, prior in [('success', None), ('failure', 'd3_pass'), ('cancelled', 'd3_pass')]:
            with self.subTest(status=status), tempfile.TemporaryDirectory() as temp:
                root = Path(temp);evidence = root / 'd3-evidence';evidence.mkdir()
                if prior:
                    (evidence/'report.json').write_text(json.dumps(dict(outcome=prior, phases=dict.fromkeys(d3.PHASES,'success'))))
                env = dict(RUNNER_TEMP=temp, D3_JOB_STATUS=status, GITHUB_STEP_SUMMARY=str(root/'summary'))
                with mock.patch.dict(os.environ, env):exec(compile(code,'export-evidence','exec'), {})
                report = json.loads((evidence/'report.json').read_text())
                self.assertEqual(set(report['phases']), set(d3.PHASES))
                self.assertEqual(report['outcome'], 'unknown' if prior is None else status)
                digest = (evidence/'SHA256SUMS').read_text().split()[0]
                self.assertEqual(digest, hashlib.sha256((evidence/'report.json').read_bytes()).hexdigest())

    def test_full_orchestration_and_corrupt_restore_with_external_docker_fixture(self):
        for corrupt in [False, True]:
            with self.subTest(corrupt_restore=corrupt), tempfile.TemporaryDirectory() as temp:
                root = Path(temp);work = root / 'work';work.mkdir()
                job = d3.Acceptance(work, self.context, 'full', SHA)
                engine = FakeDocker(root, corrupt_restore=corrupt)
                real_stat, real_lstat, real_read = Path.stat, Path.lstat, Path.read_text
                def path_stat(path, *a, **kw):
                    if str(path) == '/var/run/docker.sock':return mock.Mock(st_mode=stat.S_IFSOCK | 0o660, st_uid=0)
                    return real_stat(path, *a, **kw)
                def path_lstat(path, *a, **kw):
                    # Ownership is an OS boundary; real rootful UID checks remain unverified.
                    values = list(real_lstat(path, *a, **kw));values[4] = values[5] = 0
                    return os.stat_result(values)
                def path_read(path, *a, **kw):
                    if str(path) == '/sys/fs/cgroup/cgroup.controllers':return 'cpu memory pids'
                    return real_read(path, *a, **kw)
                with mock.patch('acceptance.subprocess.run', side_effect=engine.run), \
                        mock.patch('acceptance.os.geteuid', return_value=0), \
                        mock.patch('acceptance.platform.system', return_value='Linux'), \
                        mock.patch('acceptance.platform.machine', return_value='x86_64'), \
                        mock.patch.object(Path, 'stat', path_stat), mock.patch.object(Path, 'lstat', path_lstat), \
                        mock.patch.object(Path, 'read_text', path_read), \
                        mock.patch.object(d3.Acceptance, 'container_logs', return_value=dict(stdout='', stderr='', truncated=False, error_code=None)), \
                        mock.patch('acceptance.shutil.disk_usage', return_value=mock.Mock(free=20 * 1024**3)):
                    result = job.run()
                self.assertEqual(result, not corrupt)
                self.assertEqual(job.report['outcome'], 'failure' if corrupt else 'd3_pass')
                if corrupt:
                    self.assertEqual(job.report['phases']['restore'], 'failure')
                    self.assertEqual(job.report['phases']['restored_startup'], 'not_run')
                else:self.assertEqual(set(job.report['phases'].values()), {'success'})
                self.assertEqual(job.report['phases']['cleanup'], 'success')
                primary_volume = job.state['owner'] + '-data'
                self.assertEqual(engine.stopped_hashes[primary_volume], engine.removed_hashes[primary_volume])
                self.assertEqual(engine.containers, {})
                self.assertEqual(engine.volumes, {})
                self.assertFalse(job.private.exists())
                report = (work/'report.json').read_text()
                self.assertNotIn(job.keys[0], report)
                self.assertNotIn(job.keys[1], report)


if __name__ == '__main__':
    unittest.main()
