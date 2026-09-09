#!/usr/bin/env python3
"""HTTP fixture server for the file_url scenarios (TEST-PLAN.md section 5.5).

Serves tests/testdata/ plus four synthetic routes that the SSRF and size
scenarios need and that a plain static server cannot produce:

    /health          200, for readiness checks
    /printDemo.pdf   200 + the 2.5 MB fixture (also every other file in testdata/)
    /redirect        302 -> /printDemo.pdf          GW-URL-11
    /notfound        404 with a recognisable body   GW-URL-12
    /lying-chunked   chunked, NO Content-Length, streams past any limit
                                                    GW-URL-15
    /slow            headers, then stalls           GW-URL-16, GW-S3-13

/lying-chunked is the one route that cannot be faked with a static file: it
proves the fetch guard's LimitReader catches an oversize body even when the
response never declared a size for the up-front Content-Length check to reject.
Python's http.server switches to chunked transfer automatically when no
Content-Length header is set on an HTTP/1.1 response, which is exactly the
shape needed.

Binds 127.0.0.1 only. Every scenario that uses it runs under a profile with
PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS=true, because under the strict guard this
server is — correctly — unreachable.
"""

import os
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TESTDATA = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "testdata")
TESTDATA = os.path.normpath(TESTDATA)

SLOW_SECONDS = 120
CHUNK = b"x" * 8192
LYING_CHUNKS = 40  # 320 KiB, comfortably past profile G's 64 KiB fetch limit


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "printgw-fixtures/1.0"

    def log_message(self, fmt, *args):  # quieter than the default stderr spam
        sys.stderr.write("fixtures %s %s\n" % (self.address_string(), fmt % args))

    # Both verbs are served: /slow doubles as the stalled S3 endpoint in
    # profile C-fault-slow, and the MinIO client probes with GET and HEAD.
    def do_HEAD(self):
        self._route(body=False)

    def do_GET(self):
        self._route(body=True)

    def _route(self, body):
        path = self.path.split("?", 1)[0]

        if path == "/health":
            return self._send(200, b"ok\n", "text/plain", body)

        if path == "/redirect":
            self.send_response(302)
            self.send_header("Location", "/printDemo.pdf")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return

        if path == "/notfound":
            return self._send(404, b"fixture-404-body\n", "text/plain", body)

        if path == "/slow":
            # Headers first, then silence. The point is a peer that completes
            # the TCP handshake and the response line — so nothing short of a
            # real read deadline can rescue the caller.
            self.send_response(200)
            self.send_header("Content-Type", "application/pdf")
            self.send_header("Content-Length", str(1024 * 1024))
            self.end_headers()
            try:
                time.sleep(SLOW_SECONDS)
            except Exception:
                pass
            return

        if path == "/lying-chunked":
            # No Content-Length at all => http.server uses chunked encoding.
            # Nothing up front for the size check to reject, so only the
            # LimitReader wrapped around the body can stop this.
            self.send_response(200)
            self.send_header("Content-Type", "application/pdf")
            self.end_headers()
            if body:
                try:
                    for _ in range(LYING_CHUNKS):
                        self.wfile.write(CHUNK)
                        self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    pass  # the gateway cut us off at its limit — the point of the test
            return

        # Anything else: a file from testdata/, path-traversal-proof.
        name = os.path.basename(path.lstrip("/"))
        full = os.path.join(TESTDATA, name)
        if not name or not os.path.isfile(full):
            return self._send(404, b"no such fixture\n", "text/plain", body)
        with open(full, "rb") as fh:
            data = fh.read()
        ctype = "application/pdf" if name.endswith(".pdf") else "application/octet-stream"
        return self._send(200, data, ctype, body)

    def _send(self, code, payload, ctype, body):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        if body:
            self.wfile.write(payload)


def main():
    host = os.environ.get("FIXTURE_HOST", "127.0.0.1")
    port = int(os.environ.get("FIXTURE_PORT", "8099"))
    srv = ThreadingHTTPServer((host, port), Handler)
    srv.daemon_threads = True
    sys.stderr.write("fixtures serving %s on %s:%d\n" % (TESTDATA, host, port))
    srv.serve_forever()


if __name__ == "__main__":
    main()
