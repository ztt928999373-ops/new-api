"""Fixture tests only; these do not exercise New API or prove protocol compatibility."""
import json
import threading
import unittest
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer
from mock import Mock


class FixtureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(('127.0.0.1', 0), Mock)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.url = 'http://127.0.0.1:' + str(cls.server.server_port)

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()

    def post(self, path, **value):
        req = urllib.request.Request(self.url + path, json.dumps(value).encode(), {'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(req, timeout=2) as response:
                return response.status, response.read().decode()
        except urllib.error.HTTPError as error:
            return error.code, error.read().decode()

    def test_chat_usage(self):
        status, body = self.post('/ok/v1/chat/completions', model='mock-text')
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body)['usage']['total_tokens'], 15)

    def test_tool_arguments(self):
        _, body = self.post('/ok/v1/chat/completions', model='mock-tool')
        call = json.loads(body)['choices'][0]['message']['tool_calls'][0]
        self.assertEqual(json.loads(call['function']['arguments']), {'city': 'Paris'})

    def test_stream_usage_and_terminal(self):
        _, body = self.post('/ok/v1/chat/completions', stream=True)
        self.assertTrue(body.endswith('data: [DONE]\n\n'))
        self.assertIn('"total_tokens": 15', body)

    def test_responses(self):
        _, body = self.post('/ok/v1/responses', model='mock-text')
        self.assertEqual(json.loads(body)['usage']['input_tokens'], 10)

    def test_error_modes(self):
        for mode, expected in [('rate', 429), ('error', 500), ('primary', 500)]:
            with self.subTest(mode=mode):
                self.assertEqual(self.post('/'+mode+'/v1/chat/completions')[0], expected)

    def test_size_limit(self):
        self.assertEqual(self.post('/ok/v1/chat/completions', input='x'*65537)[0], 413)


class DatabaseFingerprintTests(unittest.TestCase):
    def test_case_filter_rejects_unknown_name(self):
        import pathlib
        import subprocess
        import sys
        script = str(pathlib.Path(__file__).with_name('verify.py'))
        for flags in [('--resume', '--case', 'unknown'), ('--case', 'chat-failover')]:
            result = subprocess.run([sys.executable, script, *flags], capture_output=True)
            self.assertEqual(result.returncode, 2)

    def test_integrity_fingerprints_and_redaction(self):
        import pathlib
        import sqlite3
        import subprocess
        import sys
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory, 'backup.db')
            connection = sqlite3.connect(path)
            for table in ['users', 'tokens', 'channels', 'logs']:
                connection.execute(f'CREATE TABLE {table} (id INTEGER, value TEXT)')
                connection.execute(f'INSERT INTO {table} VALUES (1, ?)', ('synthetic-secret-never-emit',))
            connection.commit()
            cmd = [sys.executable, str(pathlib.Path(__file__).with_name('db_check.py')), str(path)]
            first = subprocess.check_output(cmd, text=True)
            self.assertNotIn('synthetic-secret-never-emit', first)
            data = json.loads(first)
            self.assertEqual(data['integrity'], 'ok')
            self.assertEqual(data['tables']['tokens']['rows'], 1)
            connection.execute("UPDATE tokens SET value='changed'")
            connection.commit()
            second = json.loads(subprocess.check_output(cmd, text=True))
            self.assertNotEqual(data['tables']['tokens']['sha256'], second['tables']['tokens']['sha256'])
            self.assertEqual(data['tables']['users']['sha256'], second['tables']['users']['sha256'])
            connection.close()

    def test_corrupt_database_is_rejected(self):
        import pathlib
        import subprocess
        import sys
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory, 'bad.db')
            path.write_bytes(b'not a database')
            result = subprocess.run([sys.executable, str(pathlib.Path(__file__).with_name('db_check.py')), str(path)], capture_output=True)
            self.assertNotEqual(result.returncode, 0)


if __name__ == '__main__':
    unittest.main()
