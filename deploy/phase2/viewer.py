"""Fixed upstream localhost viewer. Never accepts arbitrary upstream URLs."""
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HOP = {'connection', 'transfer-encoding', 'keep-alive', 'proxy-authenticate',
       'proxy-authorization', 'te', 'trailer', 'upgrade'}


class Proxy(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *_):
        pass

    def forward(self):
        if not self.path.startswith('/') or self.path.startswith('//'):
            self.send_error(400)
            return
        size = int(self.headers.get('Content-Length', '0'))
        if size > 1048576 or self.headers.get('Transfer-Encoding'):
            self.send_error(413)
            return
        body = self.rfile.read(size) if size else None
        headers = {k: v for k, v in self.headers.items() if k.lower() not in HOP}
        conn = http.client.HTTPConnection('new-api', 3000, timeout=30)
        try:
            conn.request(self.command, self.path, body, headers)
            response = conn.getresponse()
            self.send_response(response.status)
            for k, v in response.getheaders():
                if k.lower() not in HOP:
                    self.send_header(k, v)
            self.send_header('Connection', 'close')
            self.end_headers()
            self.close_connection = True
            while data := response.read1(65536):
                self.wfile.write(data)
                self.wfile.flush()
        except (OSError, http.client.HTTPException):
            self.close_connection = True
        finally:
            conn.close()

    do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = do_OPTIONS = do_HEAD = forward


def serve():
    ThreadingHTTPServer(('0.0.0.0', 3000), Proxy).serve_forever()


if __name__ == '__main__':
    serve()
