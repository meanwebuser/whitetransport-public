#!/usr/bin/env python3
"""File-backed deterministic OK Docs API fixture for two-namespace tests."""

from __future__ import annotations

import argparse
import json
import pathlib
import time
import urllib.parse
from email.parser import BytesParser
from email.policy import default
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Fixture:
    def __init__(self, root: pathlib.Path, public_base: str, initial_delay_ms: int) -> None:
        self.root = root
        self.public_base = public_base.rstrip("/")
        self.root.mkdir(parents=True, exist_ok=True)
        delay = self.root / "delay-ms"
        if not delay.exists():
            delay.write_text(str(initial_delay_ms), encoding="ascii")

    def delay(self) -> None:
        try:
            millis = int((self.root / "delay-ms").read_text(encoding="ascii"))
        except (FileNotFoundError, ValueError):
            millis = 0
        if millis > 0:
            time.sleep(millis / 1000)

    def new_id(self) -> str:
        return str(time.time_ns())


class Handler(BaseHTTPRequestHandler):
    fixture: Fixture

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def json_response(self, status: int, payload: object) -> None:
        body = json.dumps(payload, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def parsed(self) -> tuple[str, dict[str, list[str]]]:
        parsed = urllib.parse.urlsplit(self.path)
        return parsed.path, urllib.parse.parse_qs(parsed.query)

    def do_GET(self) -> None:  # noqa: N802
        path, query = self.parsed()
        if path == "/health":
            self.json_response(200, {"ok": True})
            return
        if path.startswith("/doc/"):
            candidate = self.fixture.root / ("doc-" + path.removeprefix("/doc/"))
            if not candidate.exists():
                self.json_response(404, {"error": "missing doc"})
                return
            body = candidate.read_bytes()
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if path != "/fb.do":
            self.json_response(404, {"error": "not found"})
            return
        self.fixture.delay()
        method = query.get("method", [""])[0]
        if method == "docs.getUploadUrl":
            upload_id = self.fixture.new_id()
            self.json_response(200, {"upload_url": f"{self.fixture.public_base}/upload/{upload_id}"})
            return
        if method == "docs.commit":
            upload_id = query.get("doc_id", [""])[0]
            source = self.fixture.root / ("upload-" + upload_id)
            committed_id = "committed-" + upload_id
            if not source.exists():
                self.json_response(200, {"error_code": 404, "error_msg": "upload missing"})
                return
            (self.fixture.root / ("doc-" + committed_id)).write_bytes(source.read_bytes())
            self.json_response(200, {"id": committed_id})
            return
        if method == "messages.send":
            try:
                attachment = json.loads(query.get("attachment", ["[]"])[0])
                doc_id = attachment[0]["id"]
            except (json.JSONDecodeError, IndexError, KeyError, TypeError):
                self.json_response(200, {"error_code": 400, "error_msg": "bad attachment"})
                return
            message_id = int(self.fixture.new_id())
            message = {
                "messageId": message_id,
                "attachment": {
                    "type": "doc",
                    "doc": {"url": f"{self.fixture.public_base}/doc/{doc_id}"},
                },
            }
            (self.fixture.root / f"message-{message_id}.json").write_text(json.dumps(message), encoding="utf-8")
            self.json_response(200, {"success": True})
            return
        if method == "messages.getHistory":
            messages = []
            for candidate in sorted(self.fixture.root.glob("message-*.json"), reverse=True):
                messages.append(json.loads(candidate.read_text(encoding="utf-8")))
            self.json_response(200, {"messages": messages})
            return
        self.json_response(200, {"error_code": 404, "error_msg": f"unknown method {method}"})

    def do_POST(self) -> None:  # noqa: N802
        path, query = self.parsed()
        if path == "/control/delay":
            millis = int(query.get("ms", ["0"])[0])
            (self.fixture.root / "delay-ms").write_text(str(millis), encoding="ascii")
            self.json_response(200, {"delay_ms": millis})
            return
        if not path.startswith("/upload/"):
            self.json_response(404, {"error": "not found"})
            return
        self.fixture.delay()
        upload_id = path.removeprefix("/upload/")
        raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        mime = BytesParser(policy=default).parsebytes(
            b"Content-Type: " + self.headers["Content-Type"].encode() + b"\r\nMIME-Version: 1.0\r\n\r\n" + raw
        )
        part = next((item for item in mime.iter_parts() if item.get_filename()), None)
        if part is None:
            self.json_response(400, {"error": "missing file"})
            return
        (self.fixture.root / ("upload-" + upload_id)).write_bytes(part.get_payload(decode=True))
        self.json_response(200, {"docs": [{"id": upload_id, "token": "fixture-doc-token"}]})


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--listen", default="127.0.0.1")
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--state-dir", type=pathlib.Path, required=True)
    parser.add_argument("--initial-delay-ms", type=int, default=0)
    args = parser.parse_args()
    public_base = f"http://{args.listen}:{args.port}"
    Handler.fixture = Fixture(args.state_dir, public_base, args.initial_delay_ms)
    ThreadingHTTPServer((args.listen, args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
