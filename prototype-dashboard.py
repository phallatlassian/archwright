#!/usr/bin/env python3
"""PROTOTYPE, throwaway: serves the dashboard's UI files from disk and proxies the API to the running Works.

    python3 prototype-dashboard.py [port]      # then open http://127.0.0.1:8765/#token=x

Edit internal/works/ui/*, reload the browser; no rebuild or Works restart. The Works' port and token are read
from ~/.local/state/archwright/works.pid on every request, so a Works restart is picked up. The token in the
URL fragment only has to be non-empty: the proxy sends the real one.
"""
import http.client
import http.server
import json
import mimetypes
import os
import socketserver
import sys

ROOT = os.path.dirname(os.path.abspath(__file__))
UI = os.path.join(ROOT, "internal", "works", "ui")
PID = os.path.expanduser("~/.local/state/archwright/works.pid")
HOP = {"connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade", "proxy-authorization", "content-length"}


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass

    def static(self, rel):
        path = os.path.normpath(os.path.join(UI, rel))
        if not path.startswith(UI) or not os.path.isfile(path):
            return False
        data = open(path, "rb").read()
        self.send_response(200)
        self.send_header("Content-Type", mimetypes.guess_type(path)[0] or "application/octet-stream")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(data)
        return True

    def proxy(self):
        try:
            w = json.load(open(PID))
        except Exception as e:
            self.send_error(502, "no works.pid: %s" % e)
            return
        body = None
        n = int(self.headers.get("Content-Length") or 0)
        if n:
            body = self.rfile.read(n)
        headers = {k: v for k, v in self.headers.items() if k.lower() not in HOP | {"host", "authorization"}}
        headers["Authorization"] = "Bearer " + w["token"]
        try:
            c = http.client.HTTPConnection("127.0.0.1", w["port"], timeout=None)
            c.request(self.command, self.path, body, headers)
            r = c.getresponse()
        except Exception as e:
            self.send_error(502, "works unreachable: %s" % e)
            return
        self.send_response(r.status)
        for k, v in r.getheaders():
            if k.lower() not in HOP:
                self.send_header(k, v)
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        try:
            while True:
                chunk = r.read1(65536) if hasattr(r, "read1") else r.read(65536)
                if not chunk:
                    break
                self.wfile.write(b"%x\r\n%s\r\n" % (len(chunk), chunk))
                self.wfile.flush()
            self.wfile.write(b"0\r\n\r\n")
        except (BrokenPipeError, ConnectionResetError):
            pass
        finally:
            c.close()
        self.close_connection = True

    def route(self):
        p = self.path.split("?")[0]
        if self.command == "GET" and p in ("/", "/index.html") and self.static("index.html"):
            return
        if self.command == "GET" and p.startswith("/ui/") and self.static(p[4:]):
            return
        self.proxy()

    do_GET = do_POST = do_PUT = do_DELETE = route


class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    allow_reuse_address = True


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8765
    print("PROTOTYPE dashboard: http://127.0.0.1:%d/#token=x" % port, flush=True)
    Server(("127.0.0.1", port), Handler).serve_forever()
