#!/usr/bin/env python3
"""Exit-only HTTP target with source-address audit and an interruptible stream."""

from __future__ import annotations

import argparse
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--listen", required=True)
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--primary-nonce", required=True)
    parser.add_argument("--backup-nonce", required=True)
    parser.add_argument("--recovered-nonce", required=True)
    parser.add_argument("--stream-marker", type=Path, required=True)
    parser.add_argument("--event-log", type=Path, required=True)
    args = parser.parse_args()
    event_lock = threading.Lock()

    def record(path: str, source: str) -> None:
        with event_lock:
            with args.event_log.open("a", encoding="utf-8") as output:
                output.write(json.dumps({"path": path, "source": source}, separators=(",", ":")) + "\n")

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:  # noqa: N802
            record(self.path, self.client_address[0])
            if self.path == "/nonce":
                self.send_body(args.primary_nonce.encode())
                return
            if self.path == "/backup":
                self.send_body(args.backup_nonce.encode())
                return
            if self.path == "/recovered":
                self.send_body(args.recovered_nonce.encode())
                return
            if self.path == "/stream":
                args.stream_marker.write_text("started\n", encoding="ascii")
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.send_header("Content-Length", str(1 << 20))
                self.end_headers()
                self.wfile.write(args.primary_nonce.encode() + b"\n")
                self.wfile.flush()
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

    ThreadingHTTPServer((args.listen, args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
