#!/usr/bin/env python3
"""Independent implicit-TLS SMTP+IMAP fixture for local carrier canaries."""

from __future__ import annotations

import argparse
import base64
import fcntl
import json
import pathlib
import re
import shlex
import signal
import socketserver
import ssl
import threading
import time


class MailState:
    def __init__(self, root: pathlib.Path, failure_flag: pathlib.Path | None) -> None:
        self.root = root
        self.failure_flag = failure_flag
        self.root.mkdir(parents=True, exist_ok=True)
        (self.root / "uidvalidity").write_text("424242\n", encoding="ascii")

    def failed(self) -> bool:
        return self.failure_flag is not None and self.failure_flag.exists()

    def event(self, event: str, **fields: object) -> None:
        record = {"event": event, "at_ns": time.time_ns(), **fields}
        with (self.root / "events.lock").open("a+b") as lock:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
            with (self.root / "events.jsonl").open("a", encoding="utf-8") as output:
                output.write(json.dumps(record, separators=(",", ":")) + "\n")

    def store(self, raw: bytes) -> int:
        with (self.root / "mailbox.lock").open("a+b") as lock:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
            uid_file = self.root / "next_uid"
            uid = int(uid_file.read_text(encoding="ascii")) if uid_file.exists() else 1
            temporary = self.root / f".{uid}.eml.tmp"
            final = self.root / f"{uid}.eml"
            temporary.write_bytes(raw)
            temporary.replace(final)
            uid_file.write_text(f"{uid + 1}\n", encoding="ascii")
        self.event("smtp_store", uid=uid, bytes=len(raw))
        return uid

    def uids(self) -> list[int]:
        result: list[int] = []
        for candidate in self.root.glob("*.eml"):
            try:
                result.append(int(candidate.stem))
            except ValueError:
                continue
        return sorted(result)

    def uid_next(self) -> int:
        candidate = self.root / "next_uid"
        if candidate.exists():
            return int(candidate.read_text(encoding="ascii"))
        return 1

    def message(self, uid: int) -> bytes | None:
        candidate = self.root / f"{uid}.eml"
        return candidate.read_bytes() if candidate.exists() else None


class TLSFixtureServer(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self, address: tuple[str, int], handler: type[socketserver.StreamRequestHandler], context: ssl.SSLContext) -> None:
        super().__init__(address, handler)
        self.socket = context.wrap_socket(self.socket, server_side=True)


class SMTPHandler(socketserver.StreamRequestHandler):
    state: MailState
    username: str
    password: str

    def send_line(self, line: bytes) -> None:
        self.wfile.write(line + b"\r\n")
        self.wfile.flush()

    def handle(self) -> None:
        if self.state.failed():
            return
        self.state.event("smtp_connect", source=self.client_address[0])
        self.send_line(b"220 mail.fixture.test ESMTP ready")
        authenticated = False
        data_mode = False
        message = bytearray()
        while True:
            if self.state.failed():
                return
            line = self.rfile.readline(1 << 20)
            if not line:
                return
            line = line.rstrip(b"\r\n")
            if data_mode:
                if line == b".":
                    self.state.store(bytes(message))
                    self.send_line(b"250 2.0.0 queued")
                    data_mode = False
                    message.clear()
                    continue
                if line.startswith(b".."):
                    line = line[1:]
                message.extend(line + b"\r\n")
                continue

            command, _, argument = line.partition(b" ")
            command = command.upper()
            if command in (b"EHLO", b"HELO"):
                self.state.event("smtp_hello")
                self.wfile.write(b"250-mail.fixture.test\r\n250-AUTH PLAIN\r\n250 SIZE 1048576\r\n")
                self.wfile.flush()
            elif command == b"AUTH":
                fields = argument.split(b" ", 1)
                if len(fields) != 2 or fields[0].upper() != b"PLAIN":
                    self.send_line(b"504 5.5.4 AUTH PLAIN required")
                    continue
                try:
                    decoded = base64.b64decode(fields[1], validate=True).split(b"\x00")
                    valid = len(decoded) == 3 and decoded[1].decode() == self.username and decoded[2].decode() == self.password
                except (ValueError, UnicodeDecodeError):
                    valid = False
                if not valid:
                    self.state.event("smtp_auth_failed")
                    self.send_line(b"535 5.7.8 authentication failed")
                    continue
                authenticated = True
                self.state.event("smtp_auth")
                self.send_line(b"235 2.7.0 authenticated")
            elif command in (b"MAIL", b"RCPT"):
                self.send_line(b"250 2.1.0 ok" if authenticated else b"530 5.7.0 authenticate first")
            elif command == b"DATA":
                if not authenticated:
                    self.send_line(b"530 5.7.0 authenticate first")
                    continue
                data_mode = True
                message.clear()
                self.send_line(b"354 end with <CRLF>.<CRLF>")
            elif command == b"RSET":
                data_mode = False
                message.clear()
                self.send_line(b"250 2.0.0 reset")
            elif command == b"NOOP":
                self.send_line(b"250 2.0.0 ok")
            elif command == b"QUIT":
                self.send_line(b"221 2.0.0 bye")
                return
            else:
                self.send_line(b"502 5.5.2 unsupported")


class IMAPHandler(socketserver.StreamRequestHandler):
    state: MailState
    username: str
    password: str

    def send(self, payload: bytes, fragmented: bool = False) -> None:
        if fragmented and len(payload) > 4:
            cuts = (1, min(7, len(payload)), min(19, len(payload)))
            start = 0
            for end in cuts:
                if end > start:
                    self.request.sendall(payload[start:end])
                start = end
            self.request.sendall(payload[start:])
        else:
            self.request.sendall(payload)

    def handle(self) -> None:
        if self.state.failed():
            return
        self.send(b"* OK mail.fixture.test IMAP4rev1 ready\r\n", fragmented=True)
        authenticated = False
        while True:
            if self.state.failed():
                return
            raw = self.rfile.readline(1 << 20)
            if not raw:
                return
            line = raw.decode("utf-8", errors="replace").rstrip("\r\n")
            fields = line.split(" ", 2)
            if len(fields) < 2:
                return
            tag, command = fields[0], fields[1].upper()
            argument = fields[2] if len(fields) == 3 else ""
            self.state.event("imap_command", command=command)
            if command == "CAPABILITY":
                self.send(f"* CAPABILITY IMAP4rev1 UIDPLUS\r\n{tag} OK CAPABILITY complete\r\n".encode(), fragmented=True)
            elif command == "LOGIN":
                try:
                    credentials = shlex.split(argument)
                except ValueError:
                    credentials = []
                valid = len(credentials) == 2 and credentials[0] == self.username and credentials[1] == self.password
                if not valid:
                    self.send(f"{tag} NO authentication failed\r\n".encode())
                    continue
                authenticated = True
                self.state.event("imap_auth", source=self.client_address[0])
                self.send(f"{tag} OK LOGIN complete\r\n".encode())
            elif command == "EXAMINE":
                if not authenticated:
                    self.send(f"{tag} NO authenticate first\r\n".encode())
                    continue
                count = len(self.state.uids())
                payload = (
                    f"* {count} EXISTS\r\n* 0 RECENT\r\n"
                    "* OK [UIDVALIDITY 424242] stable fixture epoch\r\n"
                    f"* OK [UIDNEXT {self.state.uid_next()}] next fixture uid\r\n"
                    f"{tag} OK [READ-ONLY] EXAMINE complete\r\n"
                ).encode()
                self.send(payload, fragmented=True)
            elif command == "UID" and argument.upper().startswith("SEARCH UID "):
                start_text = argument.rsplit(" ", 1)[-1].split(":", 1)[0]
                try:
                    start = int(start_text)
                except ValueError:
                    self.send(f"{tag} BAD invalid UID set\r\n".encode())
                    continue
                # Deliberately include older UIDs and reverse order. The carrier
                # must enforce its own strict > cursor filter and numeric sort.
                uids = list(reversed(self.state.uids()))
                rendered = " ".join(str(uid) for uid in uids)
                self.send(f"* OK fixture unsolicited status\r\n* SEARCH {rendered}\r\n{tag} OK SEARCH complete\r\n".encode(), fragmented=True)
            elif command == "UID" and argument.upper().startswith("FETCH "):
                match = re.match(r"FETCH ([0-9]+) ", argument, flags=re.IGNORECASE)
                uid = int(match.group(1)) if match else 0
                message = self.state.message(uid)
                if message is None:
                    self.send(f"{tag} NO UID not found\r\n".encode())
                    continue
                header = f"* 1 FETCH (UID {uid} BODY[] {{{len(message)}}}\r\n".encode()
                self.send(header, fragmented=True)
                self.send(message, fragmented=True)
                self.send(f")\r\n{tag} OK FETCH complete\r\n".encode(), fragmented=True)
                self.state.event("imap_fetch", uid=uid, bytes=len(message))
            elif command == "LOGOUT":
                self.send(f"* BYE logging out\r\n{tag} OK LOGOUT complete\r\n".encode())
                return
            else:
                self.send(f"{tag} BAD unsupported\r\n".encode())


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--listen", default="127.0.0.1")
    parser.add_argument("--smtp-port", type=int, required=True)
    parser.add_argument("--imap-port", type=int, required=True)
    parser.add_argument("--state-dir", type=pathlib.Path, required=True)
    parser.add_argument("--cert", required=True)
    parser.add_argument("--key", required=True)
    parser.add_argument("--smtp-username", required=True)
    parser.add_argument("--smtp-password", required=True)
    parser.add_argument("--imap-username", required=True)
    parser.add_argument("--imap-password", required=True)
    parser.add_argument("--failure-flag", type=pathlib.Path)
    parser.add_argument("--ready-file", type=pathlib.Path)
    args = parser.parse_args()

    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.load_cert_chain(args.cert, args.key)
    state = MailState(args.state_dir, args.failure_flag)
    SMTPHandler.state, SMTPHandler.username, SMTPHandler.password = state, args.smtp_username, args.smtp_password
    IMAPHandler.state, IMAPHandler.username, IMAPHandler.password = state, args.imap_username, args.imap_password
    smtp = TLSFixtureServer((args.listen, args.smtp_port), SMTPHandler, context)
    imap = TLSFixtureServer((args.listen, args.imap_port), IMAPHandler, context)
    threads = [threading.Thread(target=server.serve_forever, daemon=True) for server in (smtp, imap)]
    for thread in threads:
        thread.start()
    ready = args.ready_file or (args.state_dir / f"ready-{args.smtp_port}-{args.imap_port}")
    ready.parent.mkdir(parents=True, exist_ok=True)
    ready.write_text("ready\n", encoding="ascii")

    stopped = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_args: stopped.set())
    signal.signal(signal.SIGINT, lambda *_args: stopped.set())
    stopped.wait()
    for server in (smtp, imap):
        server.shutdown()
        server.server_close()


if __name__ == "__main__":
    main()
