#!/usr/bin/env python3
"""Serve one deterministic nonce from an isolated exit-node namespace."""

from __future__ import annotations

import argparse
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--nonce", required=True)
    parser.add_argument("--backup-nonce", default="")
    parser.add_argument("--hold-marker", default="")
    args = parser.parse_args()
    primary_body = args.nonce.encode()
    backup_body = args.backup_nonce.encode()

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:  # noqa: N802
            if self.path == "/hold":
                if args.hold_marker:
                    Path(args.hold_marker).write_text("started\n", encoding="utf-8")
                self.send_response(200)
                self.send_header("Content-Length", str(1 << 20))
                self.end_headers()
                self.wfile.write(b"h")
                self.wfile.flush()
                time.sleep(60)
                return
            if self.path == "/nonce":
                body = primary_body
            elif self.path == "/backup" and backup_body:
                body = backup_body
            else:
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, _format: str, *_args: object) -> None:
            return

    ThreadingHTTPServer(("127.0.0.1", args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
