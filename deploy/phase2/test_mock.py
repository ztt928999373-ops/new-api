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


if __name__ == '__main__':
    unittest.main()
