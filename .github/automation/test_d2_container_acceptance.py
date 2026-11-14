"""Offline guards only: these tests do not pull or start an image."""
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

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
        self.assertEqual(diagnostic['status'], 'success')

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


if __name__ == '__main__':
    unittest.main()
