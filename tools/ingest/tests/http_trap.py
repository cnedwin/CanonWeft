"""Test-only loopback listener, never imported by production extraction."""
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
from pathlib import Path
import sys

requests = []
target = Path(sys.argv[1])


class Trap(BaseHTTPRequestHandler):
    def do_GET(self):
        requests.append(self.path)
        target.write_text(json.dumps(requests), encoding="utf-8")
        self.send_response(200)
        self.end_headers()

    def log_message(self, *_args):
        pass


server = HTTPServer(("127.0.0.1", 0), Trap)
print(server.server_address[1], flush=True)
server.serve_forever()
