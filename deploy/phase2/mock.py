"""Offline protocol fixtures. No upstream credentials or outbound HTTP clients."""
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

EVENTS = []
LOCK = threading.Lock()


class Mock(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, status, value):
        data = json.dumps(value).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        with LOCK:
            self.reply(200, list(EVENTS))

    def do_POST(self):
        length = int(self.headers.get('Content-Length', '0'))
        if length > 65536:
            self.reply(413, {'error': {'message': 'fixture size exceeded'}})
            return
        value = json.loads(self.rfile.read(length) or b'{}')
        if self.path == '/reset':
            with LOCK:
                EVENTS.clear()
            self.reply(200, {'ok': True})
            return
        mode = self.path.split('/')[1]
        with LOCK:
            EVENTS.append({'mode': mode, 'path': self.path})
        try:
            if mode in ('primary', 'rate', 'error'):
                code = 429 if mode == 'rate' else 500
                self.reply(code, {'error': {'message': 'synthetic failure', 'type': 'mock_error'}})
                return
            if mode == 'timeout':
                time.sleep(6)
            model = value.get('model', 'mock-text')
            tool = 'tool' in model
            usage = {'prompt_tokens': 10, 'completion_tokens': 5, 'total_tokens': 15}
            call = {'id': 'call_fixture', 'type': 'function', 'function': {
                'name': 'get_weather', 'arguments': '{"city":"Paris"}'}}
            if self.path.endswith('/responses'):
                output = [{'type': 'function_call', 'id': 'fc_fixture', 'call_id': 'call_fixture',
                           'name': 'get_weather', 'arguments': '{"city":"Paris"}', 'status': 'completed'}] if tool else [{
                    'id': 'msg_fixture', 'type': 'message', 'status': 'completed', 'role': 'assistant',
                    'content': [{'type': 'output_text', 'text': 'mock-ok', 'annotations': []}]}]
                response = {'id': 'resp_fixture', 'object': 'response', 'created_at': 1700000000,
                            'model': model, 'status': 'completed', 'output': output,
                            'usage': {'input_tokens': 10, 'output_tokens': 5, 'total_tokens': 15}}
                if value.get('stream'):
                    self.sse([('response.created', {'type': 'response.created', 'response': {
                        **response, 'status': 'in_progress', 'output': []}}),
                        ('response.output_text.delta', {'type': 'response.output_text.delta',
                         'item_id': 'msg_fixture', 'output_index': 0, 'content_index': 0, 'delta': 'mock-ok'}),
                        ('response.completed', {'type': 'response.completed', 'response': response})])
                else:
                    self.reply(200, response)
                return
            message = {'role': 'assistant', 'content': None if tool else 'mock-ok'}
            if tool:
                message['tool_calls'] = [call]
            base = {'id': 'chatcmpl_fixture', 'created': 1700000000, 'model': model}
            if value.get('stream'):
                delta = {'role': 'assistant', 'content': 'mock-ok'}
                if tool:
                    delta = {'role': 'assistant', 'tool_calls': [{'index': 0, **call}]}
                self.sse([(None, {**base, 'object': 'chat.completion.chunk', 'choices': [{
                    'index': 0, 'delta': delta, 'finish_reason': None}]}),
                    (None, {**base, 'object': 'chat.completion.chunk', 'choices': [{
                        'index': 0, 'delta': {}, 'finish_reason': 'tool_calls' if tool else 'stop'}]}),
                    (None, {**base, 'object': 'chat.completion.chunk', 'choices': [], 'usage': usage}),
                    (None, '[DONE]')])
            else:
                self.reply(200, {**base, 'object': 'chat.completion', 'choices': [{
                    'index': 0, 'message': message, 'finish_reason': 'tool_calls' if tool else 'stop'}], 'usage': usage})
        except (BrokenPipeError, ConnectionResetError):
            pass

    def sse(self, events):
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.send_header('Connection', 'close')
        self.end_headers()
        self.close_connection = True
        for name, value in events:
            payload = value if isinstance(value, str) else json.dumps(value)
            self.wfile.write(((f'event: {name}\n' if name else '') + f'data: {payload}\n\n').encode())
            self.wfile.flush()


if __name__ == '__main__':
    import viewer
    threading.Thread(target=viewer.serve, daemon=True).start()
    ThreadingHTTPServer(('0.0.0.0', 8000), Mock).serve_forever()
