#!/usr/bin/env python3
"""Loopback-only relay fixture for the cross-host SSH egress smoke."""

import argparse
import hashlib
import http.server
import json
import os
import tempfile
import urllib.parse


class RelayState:
    """Persist request evidence so the shell harness can inspect it remotely."""

    def __init__(self, path: str, client_token: str, node_token: str, envelope_id: str) -> None:
        self.path = path
        self.tokens = {"client": client_token, "node": node_token}
        self.envelope_id = envelope_id
        self.payload = ""
        self.data = {
            "postCount": 0,
            "getCount": 0,
            "ackCount": 0,
            "authValid": True,
            "metadataValid": True,
            "ackValid": True,
            "payloadSha256": None,
        }
        self.save()

    def save(self) -> None:
        directory = os.path.dirname(self.path) or "."
        fd, temporary = tempfile.mkstemp(prefix="relay-state-", dir=directory)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(self.data, handle, separators=(",", ":"))
        os.replace(temporary, self.path)


class RelayHandler(http.server.BaseHTTPRequestHandler):
    """Implement the exact message and ACK subset used by adminrelay.Carrier."""

    state: RelayState

    def authenticated(self, principal: str) -> bool:
        valid = self.headers.get("Authorization") == f"Bearer {self.state.tokens[principal]}"
        if not valid:
            self.state.data["authValid"] = False
            self.state.save()
            self.send_error(http.HTTPStatus.UNAUTHORIZED)
        return valid

    def read_json(self) -> dict:
        length = int(self.headers.get("Content-Length", "0"))
        return json.loads(self.rfile.read(length))

    def send_json(self, status: int, value: dict) -> None:
        encoded = json.dumps(value, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_POST(self) -> None:
        if self.path == "/api/relay/messages":
            if not self.authenticated("client"):
                return
            body = self.read_json()
            self.state.data["postCount"] += 1
            valid = (
                body.get("channel") == "control"
                and body.get("sender") == "client"
                and body.get("recipient") == "node"
                and body.get("message_key") == self.state.envelope_id
                and isinstance(body.get("payload"), str)
            )
            self.state.data["metadataValid"] = self.state.data["metadataValid"] and valid
            if not valid:
                self.state.save()
                self.send_error(http.HTTPStatus.BAD_REQUEST)
                return
            self.state.payload = body["payload"]
            self.state.data["payloadSha256"] = hashlib.sha256(body["payload"].encode()).hexdigest()
            self.state.save()
            self.send_json(http.HTTPStatus.CREATED, {"ok": True, "id": "relay-message-1"})
            return
        if self.path == "/api/relay/acks":
            if not self.authenticated("node"):
                return
            body = self.read_json()
            self.state.data["ackCount"] += 1
            valid = (
                body.get("channel") == "control"
                and body.get("consumer") == "node"
                and body.get("message_id") == "relay-message-1"
            )
            self.state.data["ackValid"] = self.state.data["ackValid"] and valid
            self.state.save()
            if not valid:
                self.send_error(http.HTTPStatus.BAD_REQUEST)
                return
            self.send_json(http.HTTPStatus.OK, {"ok": True, "advanced": True})
            return
        self.send_error(http.HTTPStatus.NOT_FOUND)

    def do_GET(self) -> None:
        if not self.authenticated("node"):
            return
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path != "/api/relay/messages":
            self.send_error(http.HTTPStatus.NOT_FOUND)
            return
        query = urllib.parse.parse_qs(parsed.query)
        self.state.data["getCount"] += 1
        valid = query.get("channel") == ["control"] and query.get("recipient") == ["node"] and query.get("limit") == ["50"]
        self.state.data["metadataValid"] = self.state.data["metadataValid"] and valid
        self.state.save()
        if not valid or not self.state.payload:
            self.send_error(http.HTTPStatus.BAD_REQUEST)
            return
        self.send_json(http.HTTPStatus.OK, {
            "ok": True,
            "messages": [{
                "id": "relay-message-1",
                "sender": "client",
                "recipient": "node",
                "payload": self.state.payload,
            }],
        })

    def log_message(self, _format: str, *_args: object) -> None:
        return


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--state", required=True)
    parser.add_argument("--client-token-file", required=True)
    parser.add_argument("--node-token-file", required=True)
    parser.add_argument("--envelope-id", required=True)
    args = parser.parse_args()
    with open(args.client_token_file, encoding="utf-8") as handle:
        client_token = handle.read().strip()
    with open(args.node_token_file, encoding="utf-8") as handle:
        node_token = handle.read().strip()
    if client_token == node_token:
        raise SystemExit("client and node tokens must be distinct")
    RelayHandler.state = RelayState(args.state, client_token, node_token, args.envelope_id)
    http.server.HTTPServer(("127.0.0.1", args.port), RelayHandler).serve_forever()


if __name__ == "__main__":
    main()
