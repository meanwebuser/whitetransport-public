#!/usr/bin/env python3
"""Exit-only HTTP target with nonce, backup, and streaming failure probes."""

from __future__ import annotations

import argparse
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--nonce", required=True)
    parser.add_argument("--backup-nonce", required=True)
    parser.add_argument("--stream-marker", required=True)
    args = parser.parse_args()

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:  # noqa: N802
            if self.path == "/nonce":
                self.send_body(args.nonce.encode())
                return
            if self.path == "/backup":
                self.send_body(args.backup_nonce.encode())
                return
            if self.path == "/stream":
                Path(args.stream_marker).write_text("started\n", encoding="utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.send_header("Content-Length", str(1 << 20))
                self.end_headers()
                for number in range(1, 241):
                    try:
                        self.wfile.write(f"chunk-{number:04d}\n".encode())
                        self.wfile.flush()
                    except (BrokenPipeError, ConnectionResetError):
                        return
                    time.sleep(0.25)
                return
            self.send_error(404)

        def send_body(self, body: bytes) -> None:
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, _format: str, *_args: object) -> None:
            return

    ThreadingHTTPServer(("127.0.0.1", args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
