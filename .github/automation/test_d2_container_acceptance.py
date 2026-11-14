"""Offline guards only: these tests do not pull or start an image."""
import json
import base64
import io
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch, MagicMock

import d2_container_acceptance as d2


class Guards(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def fixture(self):
        proof = 'synthetic-proof'
        directory = self.root / 'data'
        d2.seed(directory, proof)
        with sqlite3.connect(directory / 'test.sqlite') as conn:
            conn.execute('CREATE TABLE channels (id INTEGER)')
            conn.execute('CREATE TABLE users (username TEXT)')
        return directory, proof

    def job(self):
        return d2.Acceptance(self.root, {})

    def test_only_trusted_product_dispatch(self):
        good = {'GITHUB_REPOSITORY': 'ztt928999373-ops/new-api', 'GITHUB_REF': 'refs/heads/product/v1',
                'GITHUB_EVENT_NAME': 'workflow_dispatch', 'GITHUB_SHA': 'a' * 40}
        d2.trusted(good)
        for key, value in [('GITHUB_REPOSITORY', 'QuantumNous/new-api'),
                           ('GITHUB_REF', 'refs/heads/unreviewed'), ('GITHUB_EVENT_NAME', 'pull_request'),
                           ('GITHUB_SHA', 'short')]:
            with self.subTest(key=key), self.assertRaisesRegex(RuntimeError, 'D2_UNTRUSTED_CONTEXT'):
                d2.trusted({**good, key: value})

    def test_capacity_stops_before_pull(self):
        job = self.job()
        with patch.object(job, 'docker', return_value=str(self.root)) as docker, \
             patch('shutil.disk_usage') as usage:
            usage.return_value.free = d2.MIN_FREE - 1
            with self.assertRaisesRegex(RuntimeError, 'D2_DISK_INSUFFICIENT'):
                job.run()
            self.assertEqual(docker.call_count, 1)
        with patch('shutil.disk_usage') as usage:
            usage.return_value.free = d2.MIN_FREE
            self.assertEqual(d2.capacity(self.root), d2.MIN_FREE)

    def test_seed_is_private_and_never_overwrites_existing_directory(self):
        directory, proof = self.fixture()
        self.assertEqual((directory / 'test.sqlite').stat().st_mode & 0o777, 0o600)
        self.assertEqual(directory.stat().st_mode & 0o777, 0o700)
        d2.database(directory, proof)
        with self.assertRaises(FileExistsError):
            d2.seed(directory, 'replacement')
        self.assertEqual((directory / 'd2-proof').read_text(), proof)

    def test_database_checks_all_shutdown_policies(self):
        directory, proof = self.fixture()
        for key in d2.OPTIONS:
            with sqlite3.connect(directory / 'test.sqlite') as conn:
                conn.execute('UPDATE options SET value=? WHERE key=?', ('true', key))
            with self.assertRaisesRegex(RuntimeError, 'D2_POLICY_ENABLED'):
                d2.database(directory, proof)
            with sqlite3.connect(directory / 'test.sqlite') as conn:
                conn.execute('UPDATE options SET value=? WHERE key=?', ('false', key))

    def test_database_rejects_channels_and_registration(self):
        directory, proof = self.fixture()
        for table, value, code in [('channels', 1, 'D2_CHANNELS_PRESENT'),
                                   ('users', 'd2-denied', 'D2_REGISTRATION_CREATED_USER')]:
            with sqlite3.connect(directory / 'test.sqlite') as conn:
                conn.execute(f'INSERT INTO {table} VALUES (?)', (value,))
            with self.assertRaisesRegex(RuntimeError, code):
                d2.database(directory, proof)
            with sqlite3.connect(directory / 'test.sqlite') as conn:
                conn.execute(f'DELETE FROM {table}')

    def test_persistence_marker_must_match(self):
        directory, _ = self.fixture()
        with self.assertRaisesRegex(RuntimeError, 'D2_PROOF_MISMATCH'):
            d2.database(directory, 'wrong')

    def test_corrupt_or_missing_database_is_not_success(self):
        directory, proof = self.fixture()
        (directory / 'test.sqlite').write_bytes(b'corrupt fixture')
        with self.assertRaises(sqlite3.DatabaseError):
            d2.database(directory, proof)
        (directory / 'test.sqlite').unlink()
        with self.assertRaisesRegex(RuntimeError, 'D2_DATABASE_MISSING'):
            d2.database(directory, proof)

    def test_snapshot_refuses_running_database(self):
        job = self.job()
        with patch.object(job, 'docker', return_value=json.dumps({'Running': True})) as docker:
            with self.assertRaisesRegex(RuntimeError, 'D2_DATABASE_NOT_STOPPED'):
                job.snapshot(self.root / 'copy', 'proof')
            self.assertEqual(docker.call_count, 1)

    def test_existing_state_cannot_start_another_acceptance(self):
        self.job()
        job = self.job()
        with patch.object(job, 'docker') as docker:
            with self.assertRaisesRegex(RuntimeError, 'D2_STATE_REUSE_REJECTED'):
                job.run()
            docker.assert_not_called()

    def test_docker_endpoint_and_anonymous_config_are_scoped(self):
        job = d2.Acceptance(self.root, {'DOCKER_HOST': 'tcp://other', 'DOCKER_CONTEXT': 'other',
                                       'HTTPS_PROXY': 'http://synthetic-proxy', 'DOCKER_CONFIG': '/other'})
        self.assertNotIn('DOCKER_HOST', job.env)
        self.assertNotIn('DOCKER_CONTEXT', job.env)
        self.assertEqual(job.env['HTTPS_PROXY'], 'http://synthetic-proxy')
        self.assertEqual(job.env['DOCKER_CONFIG'], str(self.root / 'anonymous-docker'))

    def test_create_contract_uses_exact_image_and_no_network_or_host_port(self):
        job = self.job()
        commands = []
        def docker(*args, **kwargs):
            commands.append(args)
            if args[0] == 'info':
                return str(self.root)
            if args[:2] == ('image', 'inspect'):
                return json.dumps([{'RepoDigests': [d2.IMAGE], 'Id': d2.CONFIG, 'Size': 100,
                                    'Os': 'linux', 'Architecture': 'amd64'}])
            if args[0] == 'create':
                raise RuntimeError('D2_TEST_STOP')
            return ''
        with patch.object(job, 'docker', side_effect=docker), patch.object(job, 'resource', side_effect=[None, None, {'Labels': {d2.LABEL: job.state['owner']}, 'Driver': 'local', 'Options': None}]), \
             patch('shutil.disk_usage') as usage, patch('subprocess.run', return_value=subprocess.CompletedProcess([], 0, '', '')):
            usage.return_value.free = d2.MIN_FREE + 1024
            with self.assertRaisesRegex(RuntimeError, 'D2_TEST_STOP'):
                job.run()
        create = next(args for args in commands if args[0] == 'create')
        self.assertEqual(create[-1], d2.IMAGE)
        self.assertIn('--network=none', create)
        self.assertIn('--pull=never', create)
        self.assertIn('--cap-drop=ALL', create)
        self.assertFalse(any(arg.startswith(('--publish', '-p=')) for arg in create))
        self.assertNotIn(job.env['SESSION_SECRET'], create)
        self.assertNotIn(job.env['CRYPTO_SECRET'], create)
        self.assertNotEqual(job.env['SESSION_SECRET'], job.env['CRYPTO_SECRET'])
        self.assertFalse(any(args[0] in ('build', 'push', 'login', 'tag') for args in commands))

    def test_health_requires_a_running_healthy_container_and_success_status(self):
        job = self.job()
        for state, body, error in [({'Status': 'exited'}, {}, 'D2_CONTAINER_EXITED'),
                                    ({'Status': 'running', 'Health': {'Status': 'healthy'}}, {'success': False}, 'D2_STATUS_FAILED')]:
            replies = [json.dumps(state), json.dumps(body)]
            with patch.object(job, 'docker', side_effect=replies):
                with self.assertRaisesRegex(RuntimeError, error):
                    job.health()

    def test_resource_inspection_distinguishes_existing_from_absent(self):
        job = self.job()
        item = {'Labels': {'foreign': 'true'}}
        with patch('subprocess.run', return_value=subprocess.CompletedProcess([], 0, json.dumps([item]), '')):
            self.assertEqual(job.resource('volume', 'existing'), item)
        with patch('subprocess.run', return_value=subprocess.CompletedProcess([], 1, '', 'no such volume')):
            self.assertIsNone(job.resource('volume', 'absent'))

    def test_cleanup_only_exact_owned_resources(self):
        job = self.job()
        owner = job.state['owner']
        replies = [subprocess.CompletedProcess([], 0, json.dumps([{'Config': {'Labels': {d2.LABEL: owner}}}]), ''),
                   subprocess.CompletedProcess([], 0, json.dumps([{'Labels': {d2.LABEL: owner}}]), '')]
        with patch('subprocess.run', side_effect=replies), patch.object(job, 'docker') as docker:
            job.cleanup()
        self.assertEqual(docker.call_args_list[0].args, ('container', 'rm', '--force', owner))
        self.assertEqual(docker.call_args_list[1].args, ('volume', 'rm', owner + '-data'))

    def test_cleanup_refuses_foreign_label(self):
        job = self.job()
        reply = subprocess.CompletedProcess([], 0, json.dumps([{'Config': {'Labels': {}}}]), '')
        with patch('subprocess.run', return_value=reply), patch.object(job, 'docker') as docker:
            with self.assertRaisesRegex(RuntimeError, 'D2_CLEANUP_OWNERSHIP'):
                job.cleanup()
            docker.assert_not_called()

    def test_cleanup_refuses_injected_names(self):
        job = self.job()
        job.state['container'] = 'foreign-container'
        with patch('subprocess.run') as run:
            with self.assertRaisesRegex(RuntimeError, 'D2_CLEANUP_STATE_INVALID'):
                job.cleanup()
            run.assert_not_called()

    def test_cleanup_absence_is_idempotent_but_permission_errors_fail(self):
        job = self.job()
        for message in ['No such container', 'no such volume']:
            with patch('subprocess.run', return_value=subprocess.CompletedProcess([], 1, '', message)), \
                 patch.object(job, 'docker') as docker:
                job.cleanup()
                docker.assert_not_called()
        with patch('subprocess.run', return_value=subprocess.CompletedProcess([], 1, '', 'permission denied')):
            with self.assertRaisesRegex(RuntimeError, 'D2_RESOURCE_INSPECT_FAILED'):
                job.cleanup()

    def test_commands_do_not_expose_sensitive_stderr(self):
        job = self.job()
        with patch('subprocess.run', return_value=subprocess.CompletedProcess([], 1, '', 'synthetic-private')):
            with self.assertRaisesRegex(RuntimeError, '^D2_COMMAND_FAILED$'):
                job.call(['docker', 'inspect'])

    def test_exited_container_diagnostics_include_state_restart_count_and_runtime(self):
        job = self.job()
        state = {'Status': 'exited', 'Running': False, 'Paused': False, 'Restarting': False,
                 'OOMKilled': True, 'Dead': False, 'Pid': 0, 'ExitCode': 137,
                 'Error': 'permission denied', 'StartedAt': '2026-11-14T04:19:22.123456789Z',
                 'FinishedAt': '2026-11-14T04:19:25.623456789Z',
                 'Health': {'Status': 'unhealthy', 'FailingStreak': 1,
                            'Log': [{'ExitCode': 1, 'Output': 'database initialization failed'}]}}
        with patch.object(job, 'docker', return_value=json.dumps({'State': state, 'RestartCount': 2})), \
             patch.object(job, 'container_logs', return_value={'stdout': 'init failed', 'stderr': '',
                                                              'truncated': False, 'error_code': None}):
            job.diagnose()
        diagnostic = job.report['diagnostics']
        self.assertEqual(diagnostic['State'], state)
        self.assertEqual(diagnostic['RestartCount'], 2)
        self.assertEqual(diagnostic['runtime_seconds'], 3.5)
        self.assertEqual(diagnostic['runtime_kind'], 'completed_execution')
        self.assertEqual(diagnostic['status'], 'incomplete')  # State succeeded, filesystem evidence is UNKNOWN

    def test_diagnostic_state_and_logs_fail_independently_without_sensitive_errors(self):
        for state_failure, logs_failure in [(True, False), (False, True), (True, True)]:
            with self.subTest(state=state_failure, logs=logs_failure):
                job = self.job()
                state = {'Running': False, 'StartedAt': '2026-11-14T04:19:22Z',
                         'FinishedAt': '2026-11-14T04:19:25Z'}
                with patch.object(job, 'docker', side_effect=RuntimeError('synthetic-private') if state_failure else None,
                                  return_value=json.dumps({'State': state, 'RestartCount': 0})), \
                     patch.object(job, 'container_logs', side_effect=RuntimeError('synthetic-private') if logs_failure else None,
                                  return_value={'stdout': 'database failed', 'stderr': '', 'error_code': None}):
                    job.diagnose()
                diagnostic = job.report['diagnostics']
                self.assertEqual(diagnostic['status'], 'incomplete')
                self.assertNotIn('synthetic-private', json.dumps(diagnostic))
                if not state_failure:
                    self.assertEqual(diagnostic['runtime_seconds'], 3)
                if not logs_failure:
                    self.assertEqual(diagnostic['logs']['stdout'], 'database failed')

    def test_invalid_or_never_started_timestamps_remain_unknown(self):
        for started, finished in [('0001-01-01T00:00:00Z', '0001-01-01T00:00:00Z'),
                                  ('not-a-date', 'not-a-date'),
                                  ('2026-11-14T04:19:25Z', '2026-11-14T04:19:22Z')]:
            job = self.job()
            state = {'Running': False, 'StartedAt': started, 'FinishedAt': finished}
            with patch.object(job, 'docker', return_value=json.dumps({'State': state, 'RestartCount': 0})), \
                 patch.object(job, 'container_logs', return_value={'error_code': None}):
                job.diagnose()
            self.assertIsNone(job.report['diagnostics']['runtime_seconds'])
            self.assertIn('D2_DIAGNOSTIC_TIME_INVALID', job.report['diagnostics']['errors'])

    def test_running_runtime_is_marked_as_elapsed_not_completed(self):
        job = self.job()
        state = {'Running': True, 'StartedAt': '2026-11-14T04:19:22Z', 'FinishedAt': '0001-01-01T00:00:00Z'}
        with patch.object(job, 'docker', return_value=json.dumps({'State': state, 'RestartCount': 0})), \
             patch.object(job, 'container_logs', return_value={'error_code': None}), patch.object(d2, 'datetime') as clock:
            from datetime import datetime, timezone
            clock.fromisoformat.side_effect = datetime.fromisoformat
            clock.now.return_value = datetime(2026, 11, 14, 4, 19, 24, tzinfo=timezone.utc)
            job.diagnose()
        self.assertEqual(job.report['diagnostics']['runtime_seconds'], 2)
        self.assertEqual(job.report['diagnostics']['runtime_kind'], 'elapsed_at_capture')

    def test_diagnostics_redact_state_health_output_credentials_and_control_sequences(self):
        job = self.job()
        job.env.update(SESSION_SECRET='synthetic-session-value', GH_TOKEN='synthetic-gh-value')
        private = '-----BEGIN PRIVATE KEY-----\nsynthetic-private\n-----END PRIVATE KEY-----'
        raw = ('database failed\nSESSION_SECRET=synthetic-session-value\n'
               'Authorization: Bearer short\n{"api_key":"short-key"}\n'
               'pass\x00word=control-hidden\nAuthori\x1b[31mzation: obfuscated-hidden\n'
               'https://private.invalid/path?sig=signed-value\n'
               'mysql://dbuser:dbpass@internal/db\n' + private + '\n'
               'opaque ' + 'a' * 64 + '\n\x1b[31mpermission denied\x00')
        safe = job.diagnostic_value({'Error': raw, 'Health': {'Log': [{'Output': raw}]},
                                    'credential': 'short-hidden', 'extra': ['synthetic-gh-value']})
        text = json.dumps(safe)
        for forbidden in ['synthetic-session-value', 'synthetic-gh-value', 'short-key', 'signed-value',
                          'private.invalid', 'dbuser', 'dbpass', 'synthetic-private', 'short-hidden',
                          'control-hidden', 'obfuscated-hidden', 'a' * 64]:
            self.assertNotIn(forbidden, text)
        self.assertIn('database failed', text)
        self.assertIn('permission denied', text)
        self.assertNotIn('\\u001b', text)
        self.assertNotIn('\\u0000', text)

    def test_real_local_log_streams_are_separate_bounded_and_redacted(self):
        job = self.job()
        real_popen = subprocess.Popen
        script = 'import sys; print("database failed"); print("password=synthetic-private", file=sys.stderr)'
        with patch('subprocess.Popen', side_effect=lambda *args, **kwargs: real_popen([sys.executable, '-c', script], **kwargs)):
            logs = job.container_logs()
        self.assertIn('database failed', logs['stdout'])
        self.assertIn('[REDACTED', logs['stderr'])
        self.assertNotIn('synthetic-private', json.dumps(logs))
        self.assertIsNone(logs['error_code'])
        self.assertFalse(logs['truncated'])
        script = 'import sys; sys.stdout.write("complete line\\n" + "x" * 100000); sys.stdout.flush()'
        with patch('subprocess.Popen', side_effect=lambda *args, **kwargs: real_popen([sys.executable, '-c', script], **kwargs)), \
             patch.object(d2, 'DIAGNOSTIC_BYTES', 128):
            logs = job.container_logs()
        self.assertTrue(logs['truncated'])
        self.assertEqual(logs['stdout'], 'complete line')
        self.assertLessEqual(len(logs['stdout']) + len(logs['stderr']), 128)

    def test_sensitive_fields_redact_short_values_in_text_and_nested_dictionaries(self):
        job = self.job()
        for field in ['passwd', 'private_key', 'bearer', 'access_key', 'ACCESS-KEY', 'private key',
                      'password', 'secret', 'token', 'credential', 'authorization', 'cookie', 'api_key', 'dsn',
                      'pass\x00wd', 'access\x1b[31m_key']:
            with self.subTest(field=field):
                secret = 's3!'
                self.assertNotIn(secret, job.redact(field + '=' + secret))
                for value in [secret, {'nested': secret}, [secret], 42, None]:
                    safe = job.diagnostic_value({'State': {field: value}})
                    self.assertEqual(list(safe['State'].values()), ['[REDACTED]'])
                    self.assertNotIn(secret, json.dumps(safe))
        safe = job.diagnostic_value({'ExitCode': 137, 'OOMKilled': True, 'Error': 'permission denied'})
        self.assertEqual(safe, {'ExitCode': 137, 'OOMKilled': True, 'Error': 'permission denied'})

    def test_log_timeout_and_command_failure_have_safe_codes_and_do_not_hang(self):
        job = self.job()
        real_popen = subprocess.Popen
        for script, timeout, code in [
                ('import time; time.sleep(10)', 0.1, 'D2_DIAGNOSTIC_LOG_TIMEOUT'),
                ('import sys; print("password=synthetic-private", file=sys.stderr); sys.exit(7)', 5,
                 'D2_DIAGNOSTIC_LOG_FAILED')]:
            with self.subTest(code=code), \
                 patch('subprocess.Popen', side_effect=lambda *args, **kwargs: real_popen([sys.executable, '-c', script], **kwargs)), \
                 patch.object(d2, 'DIAGNOSTIC_TIMEOUT', timeout):
                logs = job.container_logs()
            self.assertEqual(logs['error_code'], code)
            self.assertNotIn('synthetic-private', json.dumps(logs))

    def test_failure_diagnostics_precede_cleanup_and_preserve_original_error(self):
        for diagnosis_fails, cleanup_fails in [(False, False), (True, False), (True, True)]:
            with self.subTest(diagnosis=diagnosis_fails, cleanup=cleanup_fails):
                work = self.root / (str(diagnosis_fails) + str(cleanup_fails))
                work.mkdir()
                job = d2.Acceptance(work, {})
                job.phase = 'health'
                calls = []
                def diagnose():
                    calls.append('diagnose')
                    if diagnosis_fails:
                        raise RuntimeError('password=synthetic-private')
                    job.report['diagnostics'] = {'status': 'success'}
                def cleanup():
                    calls.append('cleanup')
                    if cleanup_fails:
                        raise RuntimeError('password=synthetic-private')
                env = {'RUNNER_TEMP': str(work), 'GITHUB_RUN_ID': '1', 'GITHUB_RUN_ATTEMPT': '1',
                       'GITHUB_STEP_SUMMARY': str(work / 'summary')}
                with patch.dict(os.environ, env), patch.object(d2, 'trusted'), \
                     patch.object(d2, 'Acceptance', return_value=job), patch.object(sys, 'argv', ['acceptance.py']), \
                     patch.object(job, 'run', side_effect=RuntimeError('D2_CONTAINER_EXITED')), \
                     patch.object(job, 'diagnose', side_effect=diagnose), patch.object(job, 'cleanup', side_effect=cleanup):
                    with self.assertRaisesRegex(RuntimeError, '^D2_ACCEPTANCE_INCOMPLETE$'):
                        d2.main()
                self.assertEqual(calls, ['diagnose', 'cleanup'])
                self.assertEqual(job.report['error_code'], 'D2_CONTAINER_EXITED')
                self.assertEqual(job.report['phases']['health'], 'failure')
                self.assertEqual(job.report['phases']['cleanup'], 'failure' if cleanup_fails else 'success')
                summary = (work / 'summary').read_text()
                self.assertIn('D2_CONTAINER_EXITED', summary)
                self.assertNotIn('synthetic-private', summary)

    def test_diagnostic_failure_does_not_turn_successful_acceptance_into_failure(self):
        job = self.job()
        def passed():
            job.report['phases'] = {phase: 'success' for phase in d2.PHASES}
        env = {'RUNNER_TEMP': str(self.root), 'GITHUB_RUN_ID': '1', 'GITHUB_RUN_ATTEMPT': '1',
               'GITHUB_STEP_SUMMARY': str(self.root / 'summary')}
        with patch.dict(os.environ, env), patch.object(d2, 'trusted'), \
             patch.object(d2, 'Acceptance', return_value=job), patch.object(sys, 'argv', ['acceptance.py']), \
             patch.object(job, 'run', side_effect=passed), \
             patch.object(job, 'diagnose', side_effect=RuntimeError('synthetic-private')), patch.object(job, 'cleanup') as cleanup:
            d2.main()
        cleanup.assert_called_once()
        self.assertNotIn('error_code', job.report)
        self.assertEqual(job.report['diagnostics']['errors'], ['D2_DIAGNOSTIC_FAILED'])

    def storage_fixture(self):
        return ('VERSION=1\nUID=0\nGID=0\nDATA=0|0|700|directory|4096\n'
                'DB=0|0|600|regular file|8192\nEXISTS=YES\nTRAVERSE=PASS\n'
                'READ_ACCESS=PASS\nWRITE_ACCESS=PASS\nCREATE=PASS\nCREATE_ERROR_CLASS=NONE\nREMOVE=PASS\n'
                'uid_MAP=0 0 4294967295\ngid_MAP=0 0 4294967295\n')

    def storage_config(self, job):
        return {'Id': 'a' * 64, 'Image': d2.CONFIG, 'Owner': job.state['owner'],
                'User': '1001:1001', 'WorkingDir': '/data', 'UsernsMode': '', 'CapDrop': ['ALL'],
                'SecurityOpt': ['no-new-privileges:true'], 'ReadonlyRootfs': True, 'NetworkMode': 'none',
                'CapAdd': None, 'Privileged': False, 'GroupAdd': None,
                'Mounts': [{'Type': 'volume', 'Name': job.state['volume'], 'Destination': '/data', 'RW': True}]}

    def test_storage_missing_type_owner_permissions_and_readonly_are_observations(self):
        job = self.job()
        cases = [
            ('missing', [('EXISTS=YES', 'EXISTS=NO'), ('DB=0|0|600|regular file|8192', 'DB=')], True, 'file_exists'),
            ('directory instead of db', [('regular file|8192', 'directory|4096')], True, 'regular_database'),
            ('owner mismatch', [('DB=0|0|600', 'DB=1001|1001|600'), ('READ_ACCESS=PASS', 'READ_ACCESS=FAIL')], True, 'READ_ACCESS'),
            ('mode denied', [('DB=0|0|600', 'DB=0|0|000'), ('WRITE_ACCESS=PASS', 'WRITE_ACCESS=FAIL')], True, 'WRITE_ACCESS'),
            ('readonly mount', [], False, 'mount_rw'),
            ('unwritable directory', [('CREATE=PASS', 'CREATE=FAIL'), ('CREATE_ERROR_CLASS=NONE', 'CREATE_ERROR_CLASS=EACCES')], True, 'CREATE')]
        for name, replacements, rw, check in cases:
            with self.subTest(name=name):
                output = self.storage_fixture()
                for before, after in replacements:
                    output = output.replace(before, after)
                result = job.storage_result(output, {'RW': rw})
                self.assertEqual(result['checks'][check], 'FAIL')
                self.assertEqual(result['status'], 'FAIL')
                self.assertEqual(result['sqlite_open_errno'], 'UNKNOWN')
        result = job.storage_result(self.storage_fixture().replace('DB=0|0|600', 'DB=1001|1001|644'), {'RW': True})
        self.assertFalse(result['owner_matches_probe_uid']['DB'])
        self.assertEqual(result['access_status'], 'PASS')  # mismatch alone is not denial
        self.assertEqual(result['status'], 'UNKNOWN')  # auxiliary success is not application success

    def test_missing_or_partial_measurements_are_never_success(self):
        job = self.job()
        for missing in ['UID=0\n', 'DB=0|0|600|regular file|8192\n', 'CREATE=PASS\n', 'uid_MAP=0 0 4294967295\n']:
            result = job.storage_result(self.storage_fixture().replace(missing, ''), {'RW': True})
            self.assertEqual(result['access_status'], 'UNKNOWN')
            self.assertEqual(result['status'], 'UNKNOWN')
        with self.assertRaisesRegex(RuntimeError, 'D2_PROBE_PROTOCOL_INVALID'):
            job.storage_result('VERSION=invalid\n', {'RW': True})

    def test_storage_protocol_never_publishes_unrecognized_or_sensitive_output(self):
        job = self.job()
        result = job.storage_result(self.storage_fixture() + 'PASSWORD=short-private\nUNEXPECTED=https://private.invalid/?token=short\n', {'RW': True})
        self.assertNotIn('short-private', json.dumps(result))
        self.assertNotIn('private.invalid', json.dumps(result))

    def test_tool_capability_uses_head_only_and_checks_symlinks_mode_and_timeouts(self):
        job = self.job()
        regular = {'mode': 0o755, 'size': 100, 'linkTarget': ''}
        for status, entry, error in [(200, regular, None), (404, regular, 'D2_TOOL_UNAVAILABLE'),
                                      (200, {'mode': 1 << 31, 'size': 4096}, 'D2_TOOL_NOT_EXECUTABLE')]:
            response = MagicMock(status=status)
            response.getheader.return_value = base64.b64encode(json.dumps(entry).encode()).decode()
            connection = MagicMock()
            connection.getresponse.return_value = response
            with patch('http.client.HTTPConnection', return_value=connection), patch('socket.socket'):
                if error:
                    with self.assertRaisesRegex(RuntimeError, error): job.image_tool('/bin/sh')
                else:
                    job.image_tool('/bin/sh')
                self.assertEqual(connection.request.call_args.args[0], 'HEAD')
                response.read.assert_not_called()
                connection.close.assert_called_once()
        connection = MagicMock()
        connection.getresponse.side_effect = TimeoutError('synthetic-private')
        with patch('http.client.HTTPConnection', return_value=connection), patch('socket.socket'), self.assertRaises(TimeoutError):
            job.image_tool('/bin/sh')
        connection.close.assert_called_once()
        response = MagicMock(status=200)
        response.getheader.return_value = base64.b64encode(json.dumps({'mode': 1 << 27, 'linkTarget': '/private/unsafe'}).encode()).decode()
        connection = MagicMock()
        connection.getresponse.return_value = response
        with patch('http.client.HTTPConnection', return_value=connection), patch('socket.socket'), self.assertRaisesRegex(RuntimeError, 'D2_TOOL_PATH_REJECTED'):
            job.image_tool('/bin/sh')

    def test_probe_protocol_with_local_synthetic_file_never_changes_file_or_leaves_probe(self):
        directory = self.root / 'probe-fixture'
        directory.mkdir(mode=0o700)
        db = directory / 'test.sqlite'
        db.write_bytes(b'synthetic-metadata-only' * 400)
        before = db.stat()
        script = d2.STORAGE_PROBE.replace('/data', str(directory))
        result = subprocess.run(['/bin/sh', '-c', script], capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 0)
        measurement = self.job().storage_result(result.stdout, {'RW': True})
        self.assertEqual(measurement['metadata']['DB']['size'], before.st_size)
        self.assertEqual(measurement['access_status'], 'PASS')
        self.assertEqual(measurement['status'], 'UNKNOWN')
        self.assertEqual(db.stat().st_size, before.st_size)
        self.assertEqual(db.stat().st_mtime_ns, before.st_mtime_ns)
        self.assertEqual(list(directory.iterdir()), [db])
    def test_probe_matches_identity_isolation_mount_and_never_reads_sqlite(self):
        job = self.job()
        config = self.storage_config(job)
        volume = {'Labels': {d2.LABEL: job.state['owner']}, 'Driver': 'local', 'Options': None}
        calls = []
        def docker(*args, **kwargs):
            calls.append(args)
            if args[0] == 'inspect': return json.dumps(config)
            if args[:2] == ('image', 'inspect'): return json.dumps('1001:1001')
            if args[:2] == ('volume', 'inspect'): return json.dumps([volume])
            if args[0] == 'wait': return '0\n'
            return ''
        with patch.object(job, 'docker', side_effect=docker), patch.object(job, 'image_tool'), \
             patch.object(job, 'resource', return_value=None), \
             patch.object(job, 'container_logs', return_value={'stdout': self.storage_fixture(), 'stderr': '', 'truncated': False, 'error_code': None}):
            result = job.storage_diagnostics()
        create = next(args for args in calls if args[0] == 'create')
        self.assertEqual(create[create.index('--user') + 1], '1001:1001')
        self.assertIn('--network=none', create)
        self.assertIn('--cap-drop=ALL', create)
        self.assertEqual(create[-3:], (d2.IMAGE, '-c', d2.STORAGE_PROBE))
        self.assertEqual(result['measurement']['UID'], 0)  # fixture measurement, never inferred from --user
        self.assertEqual(result['status'], 'UNKNOWN')
        self.assertNotIn('chmod', d2.STORAGE_PROBE)
        self.assertNotIn('chown', d2.STORAGE_PROBE)
        self.assertNotIn('sqlite3', d2.STORAGE_PROBE)
        self.assertEqual(json.loads(job.state_file.read_text())['probe'], job.state['owner'] + '-probe')

    def test_unavailable_tools_do_not_create_probe_or_claim_success(self):
        job = self.job()
        config = self.storage_config(job)
        volume = {'Labels': {d2.LABEL: job.state['owner']}, 'Driver': 'local', 'Options': None}
        with patch.object(job, 'docker', side_effect=[json.dumps(config), '"1001:1001"', json.dumps([volume])]) as docker, \
             patch.object(job, 'image_tool', side_effect=TimeoutError('password=synthetic-private')):
            result = job.storage_diagnostics()
        self.assertEqual(result['status'], 'UNKNOWN')
        self.assertEqual(result['error_code'], 'D2_PROBE_TOOLS_UNKNOWN')
        self.assertNotIn('synthetic-private', json.dumps(result))
        self.assertFalse(any(call.args[0] == 'create' for call in docker.call_args_list))

    def test_probe_timeout_or_log_failure_preserves_original_error_and_partial_metadata(self):
        for failure in [subprocess.TimeoutExpired(['synthetic-private'], 15), RuntimeError('synthetic-private')]:
            job = self.job()
            job.report['error_code'] = 'D2_CONTAINER_EXITED'
            config = self.storage_config(job)
            volume = {'Labels': {d2.LABEL: job.state['owner']}, 'Driver': 'local', 'Options': None}
            def docker(*args, **kwargs):
                if args[0] == 'inspect' and 'RestartCount' in args[2]:
                    return json.dumps({'State': {'Running': False}, 'RestartCount': 0})
                if args[0] == 'inspect': return json.dumps(config)
                if args[:2] == ('image', 'inspect'): return '"1001:1001"'
                if args[:2] == ('volume', 'inspect'): return json.dumps([volume])
                if args[0] == 'wait': raise failure
                return ''
            with patch.object(job, 'docker', side_effect=docker), patch.object(job, 'image_tool'), \
                 patch.object(job, 'resource', return_value=None), \
                 patch.object(job, 'container_logs', return_value={'stdout': '', 'stderr': '', 'error_code': None}):
                job.diagnose()
            self.assertEqual(job.report['error_code'], 'D2_CONTAINER_EXITED')
            storage = job.report['diagnostics']['storage']
            self.assertEqual(storage['status'], 'UNKNOWN')
            self.assertEqual(storage['container_config']['User'], '1001:1001')
            self.assertNotIn('synthetic-private', json.dumps(storage))

    def test_probe_cleanup_failure_still_attempts_original_owned_resources(self):
        job = self.job()
        job.state['probe'] = job.state['owner'] + '-probe'
        owner = {'Config': {'Labels': {d2.LABEL: job.state['owner']}}}
        with patch.object(job, 'resource', side_effect=[RuntimeError('synthetic-private'), owner,
                         {'Labels': {d2.LABEL: job.state['owner']}}]), patch.object(job, 'docker') as docker:
            with self.assertRaisesRegex(RuntimeError, 'D2_PROBE_CLEANUP_FAILED'): job.cleanup()
        self.assertEqual(docker.call_args_list[0].args, ('container', 'rm', '--force', job.state['container']))
        self.assertEqual(docker.call_args_list[1].args, ('volume', 'rm', job.state['volume']))

    def test_truncated_or_failed_probe_logs_are_unknown_and_not_published(self):
        for truncated, error in [(True, None), (False, 'D2_DIAGNOSTIC_LOG_FAILED')]:
            job = self.job()
            job.report['error_code'] = 'D2_CONTAINER_EXITED'
            config = self.storage_config(job)
            volume = {'Labels': {d2.LABEL: job.state['owner']}, 'Driver': 'local', 'Options': None}
            def docker(*args, **kwargs):
                if args[0] == 'inspect' and 'RestartCount' in args[2]:
                    return json.dumps({'State': {'Running': False}, 'RestartCount': 0})
                if args[0] == 'inspect': return json.dumps(config)
                if args[:2] == ('image', 'inspect'): return '"1001:1001"'
                if args[:2] == ('volume', 'inspect'): return json.dumps([volume])
                if args[0] == 'wait': return '0'
                return ''
            logs = [{'stdout': '', 'stderr': '', 'error_code': None},
                    {'stdout': 'password=synthetic-private', 'stderr': '', 'error_code': error, 'truncated': truncated}]
            with patch.object(job, 'docker', side_effect=docker), patch.object(job, 'image_tool'), \
                 patch.object(job, 'resource', return_value=None), patch.object(job, 'container_logs', side_effect=logs):
                job.diagnose()
            self.assertEqual(job.report['diagnostics']['storage']['status'], 'UNKNOWN')
            self.assertEqual(job.report['error_code'], 'D2_CONTAINER_EXITED')
            self.assertNotIn('synthetic-private', json.dumps(job.report))

    def test_owned_probe_cleanup_precedes_original_container_and_volume(self):
        job = self.job()
        job.state['probe'] = job.state['owner'] + '-probe'
        def resource(kind, name):
            return {'Config': {'Labels': {d2.LABEL: job.state['owner']}}} if kind == 'container' else {'Labels': {d2.LABEL: job.state['owner']}}
        with patch.object(job, 'resource', side_effect=resource), patch.object(job, 'docker') as docker:
            job.cleanup()
        self.assertEqual([call.args[-1] for call in docker.call_args_list],
                         [job.state['probe'], job.state['container'], job.state['volume']])

    def test_application_log_collector_keeps_rfc3339nano_timestamps_by_default(self):
        job = self.job()
        real_popen = subprocess.Popen
        commands = []
        stamp = '2026-11-14T06:30:44.187484700Z '
        def producer(command, **kwargs):
            commands.append(command)
            payload = (stamp if '--timestamps' in command else '') + 'application startup failed\n'
            return real_popen([sys.executable, '-c', 'import sys; sys.stdout.write(sys.argv[1])', payload], **kwargs)
        with patch('subprocess.Popen', side_effect=producer):
            logs = job.container_logs()
        self.assertEqual(commands[0], ['docker', '--host=unix:///var/run/docker.sock', 'logs',
                                       '--timestamps', '--tail=200', job.state['container']])
        self.assertEqual(logs['stdout'], stamp + 'application startup failed')
        self.assertIsNone(logs['error_code'])

    def test_probe_log_collection_to_parser_handles_docker_format_and_failures_before_cleanup(self):
        real_popen = subprocess.Popen
        for case in ('normal', 'unexpected_timestamp', 'malformed_timestamp', 'missing_field', 'truncated'):
            with self.subTest(case=case):
                work = self.root / case
                work.mkdir()
                job = d2.Acceptance(work, {})
                job.phase = 'health'
                config = self.storage_config(job)
                volume = {'Labels': {d2.LABEL: job.state['owner']}, 'Driver': 'local', 'Options': None}
                commands = []
                def docker(*args, **kwargs):
                    if args[0] == 'inspect' and 'RestartCount' in args[2]:
                        return json.dumps({'State': {'Running': False, 'StartedAt': '2026-11-14T06:30:44Z',
                                                    'FinishedAt': '2026-11-14T06:30:45Z'}, 'RestartCount': 0})
                    if args[0] == 'inspect': return json.dumps(config)
                    if args[:2] == ('image', 'inspect'): return '"1001:1001"'
                    if args[:2] == ('volume', 'inspect'): return json.dumps([volume])
                    if args[0] == 'wait': return '0'
                    return ''
                def producer(command, **kwargs):
                    commands.append(command)
                    payload = self.storage_fixture() if command[-1].endswith('-probe') else 'application startup failed\n'
                    if command[-1].endswith('-probe'):
                        if case == 'unexpected_timestamp': payload = ''.join('2026-11-14T06:30:44.187484700Z ' + line + '\n' for line in payload.splitlines())
                        if case == 'malformed_timestamp': payload = payload.replace('VERSION=1', 'not-a-timestamp VERSION=1')
                        if case == 'missing_field': payload = payload.replace('UID=0\n', '')
                    # Emulate Docker's actual flag-dependent wire format, not a pre-parsed fixture.
                    if '--timestamps' in command:
                        payload = ''.join('2026-11-14T06:30:44.187484700Z ' + line + '\n' for line in payload.splitlines())
                    return real_popen([sys.executable, '-c', 'import sys; sys.stdout.write(sys.argv[1])', payload], **kwargs)
                env = {'RUNNER_TEMP': str(work), 'GITHUB_RUN_ID': '1', 'GITHUB_RUN_ATTEMPT': '1',
                       'GITHUB_STEP_SUMMARY': str(work / 'summary')}
                with patch.dict(os.environ, env), patch.object(d2, 'trusted'), \
                     patch.object(d2, 'Acceptance', return_value=job), patch.object(sys, 'argv', ['acceptance.py']), \
                     patch.object(job, 'run', side_effect=RuntimeError('D2_CONTAINER_EXITED')), \
                     patch.object(job, 'docker', side_effect=docker), patch.object(job, 'image_tool'), \
                     patch.object(job, 'resource', return_value=None), patch('subprocess.Popen', side_effect=producer), \
                     patch.object(d2, 'DIAGNOSTIC_BYTES', 128 if case == 'truncated' else 65536), \
                     patch.object(job, 'cleanup', wraps=job.cleanup) as cleanup:
                    with self.assertRaisesRegex(RuntimeError, '^D2_ACCEPTANCE_INCOMPLETE$'):
                        d2.main()
                self.assertEqual(len(commands), 2)
                self.assertIn('--timestamps', commands[0])
                self.assertNotIn('--timestamps', commands[1])
                self.assertEqual(commands[1][-2:], ['--tail=200', job.state['owner'] + '-probe'])
                storage = job.report['diagnostics']['storage']
                if case == 'normal':
                    measured = storage['measurement']
                    self.assertEqual(measured['UID'], 0)
                    self.assertEqual(measured['GID'], 0)
                    self.assertEqual(measured['metadata']['DATA'], {'uid': 0, 'gid': 0, 'mode': '700', 'type': 'directory', 'size': 4096})
                    self.assertEqual(measured['metadata']['DB'], {'uid': 0, 'gid': 0, 'mode': '600', 'type': 'regular file', 'size': 8192})
                    self.assertEqual(measured['uid_MAP'], [[0, 0, 4294967295]])
                    self.assertEqual(measured['gid_MAP'], [[0, 0, 4294967295]])
                    self.assertEqual(measured['access_status'], 'PASS')  # also requires VERSION=1
                else:
                    self.assertEqual(storage['status'], 'UNKNOWN')
                    self.assertNotEqual(storage.get('measurement', {}).get('access_status'), 'PASS')
                self.assertEqual(job.report['error_code'], 'D2_CONTAINER_EXITED')
                self.assertEqual(job.report['phases']['health'], 'failure')
                self.assertEqual(job.report['phases']['cleanup'], 'success')
                cleanup.assert_called_once()

    def test_foreign_or_injected_probe_is_never_removed(self):
        for name, item in [('foreign', None), ('owned-name', {'Config': {'Labels': {}}})]:
            job = self.job()
            job.state['probe'] = 'foreign' if name == 'foreign' else job.state['owner'] + '-probe'
            def resource(kind, resource_name):
                if resource_name == job.state['probe']: return item
                return {'Config': {'Labels': {d2.LABEL: job.state['owner']}}} if kind == 'container' else {'Labels': {d2.LABEL: job.state['owner']}}
            with patch.object(job, 'resource', side_effect=resource), patch.object(job, 'docker') as docker:
                with self.assertRaisesRegex(RuntimeError, 'D2_PROBE_CLEANUP_FAILED'): job.cleanup()
            self.assertFalse(any(call.args[-1] == job.state['probe'] for call in docker.call_args_list))

    def test_primary_identity_requires_live_pid_cgroup_binding_and_userns_translation(self):
        job = self.job()
        state = {'Id': 'a' * 64, 'Pid': 12345, 'Running': True}
        data = {'cgroup': '0::/docker/' + 'a' * 64, 'status': 'Uid:\t100000 100001 100001 100001\nGid:\t200000 200002 200002 200002\n',
                'uid_map': '0 100000 65536', 'gid_map': '0 200000 65536'}
        with patch.object(job, 'docker', return_value=json.dumps(state)), \
             patch.object(Path, 'open', side_effect=lambda p, *args: io.StringIO(data[p.name]), autospec=True):
            job.observe_primary_identity()
        self.assertEqual(job.report['primary_process_identity']['status'], 'MEASURED')
        self.assertEqual(job.report['primary_process_identity']['uid'], 1)
        self.assertEqual(job.report['primary_process_identity']['gid'], 2)
        with patch.object(job, 'docker', return_value=json.dumps({**state, 'Running': False, 'Pid': 0})):
            job.observe_primary_identity()
        self.assertEqual(job.report['primary_process_identity']['status'], 'UNKNOWN')
        data['cgroup'] = '0::/different-host'
        with patch.object(job, 'docker', return_value=json.dumps(state)), \
             patch.object(Path, 'open', side_effect=lambda p, *args: io.StringIO(data[p.name]), autospec=True):
            job.observe_primary_identity()
        self.assertEqual(job.report['primary_process_identity']['status'], 'UNKNOWN')

    def seed_owner_fixture(self, job):
        directory = job.work / 'seed'
        d2.seed(directory, 'synthetic-proof')
        db = directory / 'test.sqlite'
        original = db.lstat()
        values = list(original)
        values[4], values[5] = 0, 0
        return db, original, os.stat_result(values)

    def test_seed_owner_adjustment_preserves_0600_and_verifies_root_with_lstat(self):
        job = self.job()
        db, original, root = self.seed_owner_fixture(job)
        original_lstat = Path.lstat
        for runner_uid in (0, 1001):
            replies = iter([original, root])
            with self.subTest(uid=runner_uid), patch.object(os, 'geteuid', return_value=runner_uid), \
                 patch.object(Path, 'lstat', autospec=True, side_effect=lambda p, *a, **k: next(replies) if p == db else original_lstat(p, *a, **k)), \
                 patch.object(os, 'chown') as chown, patch.object(job, 'call') as call:
                job.prepare_seed_owner(db)
            if runner_uid == 0:
                chown.assert_called_once_with(db, 0, 0, follow_symlinks=False)
                call.assert_not_called()
            else:
                self.assertEqual(call.call_args.args[0], ['/usr/bin/sudo', '-n', '--', '/usr/bin/chown', '--no-dereference', '0:0', str(db)])
                chown.assert_not_called()
            self.assertEqual(job.report['seed_file']['uid'], 0)
            self.assertEqual(job.report['seed_file']['gid'], 0)
            self.assertEqual(job.report['seed_file']['mode'], '0o600')
            self.assertTrue(job.report['seed_file']['regular_file'])
            self.assertEqual(d2.stat.S_IMODE(db.stat().st_mode), 0o600)

    def test_seed_owner_failure_or_wrong_postconditions_abort_without_copy(self):
        job = self.job()
        db, original, root = self.seed_owner_fixture(job)
        original_lstat = Path.lstat
        wrong = list(root)
        wrong[0] = (wrong[0] & ~0o777) | 0o644
        wrong = os.stat_result(wrong)
        for outcome, expected in [(RuntimeError('password=synthetic-private'), 'D2_SEED_OWNER_CHANGE_FAILED'),
                                   (wrong, 'D2_SEED_OWNER_VERIFICATION_FAILED')]:
            replies = iter([original, wrong])
            with patch.object(os, 'geteuid', return_value=1001), \
                 patch.object(Path, 'lstat', autospec=True, side_effect=lambda p, *a, **k: next(replies) if p == db else original_lstat(p, *a, **k)), \
                 patch.object(job, 'call', side_effect=outcome if isinstance(outcome, Exception) else None), \
                 patch.object(job, 'docker') as docker:
                with self.assertRaisesRegex(RuntimeError, '^' + expected + '$'):
                    job.prepare_seed_owner(db)
            docker.assert_not_called()

    def test_root_owned_private_seed_copy_uses_only_scoped_noninteractive_sudo(self):
        job = self.job()
        directory = job.work / 'seed'
        with patch.object(os, 'geteuid', return_value=1001), patch.object(job, 'call') as call, patch.object(job, 'docker') as docker:
            job.copy_seed(directory)
        self.assertEqual(call.call_args.args[0], ['/usr/bin/sudo', '-n', '--', '/usr/bin/docker',
            '--host=unix:///var/run/docker.sock', '--config=' + job.env['DOCKER_CONFIG'], 'cp', str(directory) + '/.', job.state['container'] + ':/data'])
        docker.assert_not_called()
        with patch.object(os, 'geteuid', return_value=1001), patch.object(job, 'call', side_effect=RuntimeError('synthetic-private')):
            with self.assertRaisesRegex(RuntimeError, '^D2_SEED_COPY_FAILED$'):
                job.copy_seed(directory)

    def preflight_fixture(self, job):
        job.state['probe'] = job.state['owner'] + '-probe'
        measured = job.storage_result(self.storage_fixture(), {'RW': True})
        return {'status': 'UNKNOWN', 'measurement': measured, 'database_size_matches_seed': True}

    def test_copied_seed_preflight_requires_measured_owner_mode_and_access(self):
        job = self.job()
        evidence = self.preflight_fixture(job)
        item = {'Config': {'Labels': {d2.LABEL: job.state['owner']}}, 'State': {'Running': False}}
        with patch.object(job, 'storage_diagnostics', return_value=evidence), patch.object(job, 'resource', return_value=item), \
             patch.object(job, 'docker') as docker:
            job.preflight_seed()
        self.assertEqual(job.report['seed_preflight']['status'], 'PASS')
        self.assertEqual(evidence['status'], 'UNKNOWN')  # unrelated SQLite/process evidence is not promoted
        self.assertEqual(evidence['measurement']['metadata']['DB']['mode'], '600')
        docker.assert_called_once_with('container', 'rm', job.state['probe'], timeout=d2.DIAGNOSTIC_TIMEOUT)

    def test_preflight_wrong_owner_unknown_or_denied_access_blocks_startup(self):
        for case, expected in [('host_owner_change', 'D2_SEED_OWNER_CHANGE_FAILED'),
                               ('owner', 'D2_SEED_PREFLIGHT_OWNER'), ('mode', 'D2_SEED_PREFLIGHT_FILE'),
                               ('read', 'D2_SEED_PREFLIGHT_ACCESS'), ('write', 'D2_SEED_PREFLIGHT_ACCESS'),
                               ('unknown', 'D2_SEED_PREFLIGHT_ACCESS'), ('missing', 'D2_SEED_PREFLIGHT_UNKNOWN'),
                               ('identity_unknown', 'D2_SEED_PREFLIGHT_IDENTITY'), ('size_unknown', 'D2_SEED_PREFLIGHT_SIZE')]:
            with self.subTest(case=case):
                work = self.root / case
                work.mkdir()
                job = d2.Acceptance(work, {})
                evidence = self.preflight_fixture(job)
                measured = evidence['measurement']
                if case == 'owner': measured['metadata']['DB']['uid'] = 1001
                if case == 'mode': measured['metadata']['DB']['mode'] = '644'
                if case in ('read', 'write', 'unknown'):
                    measured['checks']['READ_ACCESS' if case != 'write' else 'WRITE_ACCESS'] = 'UNKNOWN' if case == 'unknown' else 'FAIL'
                if case == 'missing': measured['metadata'].pop('DB')
                if case == 'identity_unknown': measured['UID'] = 'UNKNOWN'
                if case == 'size_unknown': evidence['database_size_matches_seed'] = 'UNKNOWN'
                calls = []
                def docker(*args, **kwargs):
                    calls.append(args)
                    if args[0] == 'info': return str(self.root)
                    if args[:2] == ('image', 'inspect'):
                        return json.dumps([{'RepoDigests': [d2.IMAGE], 'Id': d2.CONFIG, 'Size': 100, 'Os': 'linux', 'Architecture': 'amd64'}])
                    return ''
                owned = {'Labels': {d2.LABEL: job.state['owner']}, 'Driver': 'local', 'Options': None}
                with patch.object(job, 'docker', side_effect=docker), patch.object(job, 'resource', side_effect=[None, None, owned]), \
                     patch.object(job, 'prepare_seed_owner', side_effect=RuntimeError('D2_SEED_OWNER_CHANGE_FAILED') if case == 'host_owner_change' else None), \
                     patch.object(job, 'copy_seed') as copy, \
                     patch.object(job, 'storage_diagnostics', return_value=evidence), \
                     patch('subprocess.run', return_value=subprocess.CompletedProcess([], 0, '', '')), patch('shutil.disk_usage') as usage:
                    usage.return_value.free = d2.MIN_FREE + 1024
                    with self.assertRaisesRegex(RuntimeError, '^' + expected + '$'):
                        job.run()
                self.assertFalse(any(args[0] == 'start' for args in calls))
                if case == 'host_owner_change':
                    copy.assert_not_called()
                    self.assertNotIn('seed_preflight', job.report)
                else:
                    self.assertEqual(job.report['seed_preflight']['status'], 'FAIL')

    def test_preflight_failure_survives_diagnostic_failure_and_cleanup_runs(self):
        job = self.job()
        job.phase = 'seed'
        env = {'RUNNER_TEMP': str(self.root), 'GITHUB_RUN_ID': '1', 'GITHUB_RUN_ATTEMPT': '1',
               'GITHUB_STEP_SUMMARY': str(self.root / 'summary')}
        with patch.dict(os.environ, env), patch.object(d2, 'trusted'), patch.object(d2, 'Acceptance', return_value=job), \
             patch.object(sys, 'argv', ['acceptance.py']), patch.object(job, 'run', side_effect=RuntimeError('D2_SEED_PREFLIGHT_OWNER')), \
             patch.object(job, 'diagnose', side_effect=RuntimeError('synthetic-private')), patch.object(job, 'cleanup') as cleanup:
            with self.assertRaisesRegex(RuntimeError, '^D2_ACCEPTANCE_INCOMPLETE$'):
                d2.main()
        self.assertEqual(job.report['error_code'], 'D2_SEED_PREFLIGHT_OWNER')
        self.assertEqual(job.report['phases']['seed'], 'failure')
        self.assertEqual(job.report['phases']['startup'], 'not_run')
        cleanup.assert_called_once()


if __name__ == '__main__':
    unittest.main()
