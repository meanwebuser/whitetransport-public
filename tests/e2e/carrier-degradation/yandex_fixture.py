#!/usr/bin/env python3
"""Deterministic local Yandex Disk API fixture for carrier process tests."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import urllib.parse
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Fixture:
    def __init__(self, root: pathlib.Path, public_base: str, failure_flag: pathlib.Path | None) -> None:
        self.root = root
        self.public_base = public_base.rstrip("/")
        self.failure_flag = failure_flag
        self.root.mkdir(parents=True, exist_ok=True)

    def file_for(self, remote_path: str) -> pathlib.Path:
        safe = urllib.parse.quote(remote_path, safe="")
        return self.root / safe

    def list_items(self, folder: str) -> list[dict[str, str]]:
        prefix = folder.rstrip("/") + "/"
        items: list[dict[str, str]] = []
        for candidate in sorted(self.root.iterdir()):
            remote_path = urllib.parse.unquote(candidate.name)
            if not remote_path.startswith(prefix):
                continue
            items.append(
                {
                    "name": remote_path.rsplit("/", 1)[-1],
                    "path": remote_path,
                    "modified": datetime.fromtimestamp(
                        candidate.stat().st_mtime, timezone.utc
                    ).isoformat().replace("+00:00", "Z"),
                    "type": "file",
                }
            )
        return items


class Handler(BaseHTTPRequestHandler):
    fixture: Fixture

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def write_json(self, status: int, payload: object) -> None:
        body = json.dumps(payload, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def parsed(self) -> tuple[str, dict[str, list[str]]]:
        parsed = urllib.parse.urlsplit(self.path)
        return parsed.path, urllib.parse.parse_qs(parsed.query)

    def fail_if_disabled(self) -> bool:
        if self.fixture.failure_flag is None or not self.fixture.failure_flag.exists():
            return False
        self.write_json(503, {"message": "fixture disabled"})
        return True

    def do_PUT(self) -> None:  # noqa: N802
        if self.fail_if_disabled():
            return
        path, query = self.parsed()
        if path == "/upload":
            remote_path = query.get("path", [""])[0]
            body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            self.fixture.file_for(remote_path).write_bytes(body)
            self.write_json(201, {})
            return
        if path == "/v1/disk/resources":
            self.write_json(201, {})
            return
        self.write_json(404, {"message": "not found"})

    def do_GET(self) -> None:  # noqa: N802
        path, query = self.parsed()
        remote_path = query.get("path", [""])[0]
        if path == "/health":
            self.write_json(200, {"ok": True})
            return
        if self.fail_if_disabled():
            return
        if path == "/v1/disk/resources/upload":
            href = f"{self.fixture.public_base}/upload?{urllib.parse.urlencode({'path': remote_path})}"
            self.write_json(200, {"href": href})
            return
        if path == "/v1/disk/resources/download":
            href = f"{self.fixture.public_base}/download?{urllib.parse.urlencode({'path': remote_path})}"
            self.write_json(200, {"href": href})
            return
        if path == "/download":
            candidate = self.fixture.file_for(remote_path)
            if not candidate.exists():
                self.write_json(404, {"message": "not found"})
                return
            body = candidate.read_bytes()
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if path == "/v1/disk/resources":
            self.write_json(
                200,
                {"_embedded": {"items": self.fixture.list_items(remote_path)}},
            )
            return
        self.write_json(404, {"message": "not found"})

    def do_DELETE(self) -> None:  # noqa: N802
        if self.fail_if_disabled():
            return
        path, query = self.parsed()
        if path != "/v1/disk/resources":
            self.write_json(404, {"message": "not found"})
            return
        candidate = self.fixture.file_for(query.get("path", [""])[0])
        if candidate.exists():
            candidate.unlink()
        self.send_response(204)
        self.end_headers()

    def do_POST(self) -> None:  # noqa: N802
        path, _query = self.parsed()
        if path != "/control/fail":
            self.write_json(404, {"message": "not found"})
            return
        if self.fixture.failure_flag is None:
            self.write_json(400, {"message": "failure flag not configured"})
            return
        self.fixture.failure_flag.parent.mkdir(parents=True, exist_ok=True)
        self.fixture.failure_flag.touch()
        self.write_json(200, {"failed": True})


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--listen", default="127.0.0.1")
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--state-dir", type=pathlib.Path, required=True)
    parser.add_argument("--failure-flag", type=pathlib.Path)
    args = parser.parse_args()

    public_base = f"http://{args.listen}:{args.port}"
    Handler.fixture = Fixture(args.state_dir, public_base, args.failure_flag)
    server = ThreadingHTTPServer((args.listen, args.port), Handler)
    server.serve_forever()


if __name__ == "__main__":
    main()
