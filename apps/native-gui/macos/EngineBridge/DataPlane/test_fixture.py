#!/usr/bin/env python3
"""Black-box tests for the credential-free C-ABI SOCKS fixture."""

from __future__ import annotations

import json
import os
import socket
import struct
import subprocess
import sys
import time
import unittest
from pathlib import Path


FIXTURE = Path(__file__).with_name("local_fixture.py")
TEST_NET_V4 = "198.51.100.10"
TEST_NET_V6 = "2001:db8::10"
TCP_PORT = 443
UDP_PORT = 53


def recv_exact(connection: socket.socket, count: int) -> bytes:
    """Read exactly ``count`` bytes from a SOCKS control connection."""
    chunks: list[bytes] = []
    while sum(map(len, chunks)) < count:
        chunk = connection.recv(count - sum(map(len, chunks)))
        if not chunk:
            raise AssertionError("fixture closed the SOCKS control connection")
        chunks.append(chunk)
    return b"".join(chunks)


def encode_target(host: str, port: int) -> bytes:
    """Encode one numeric RFC 1928 address and port."""
    if ":" in host:
        return b"\x04" + socket.inet_pton(socket.AF_INET6, host) + struct.pack("!H", port)
    return b"\x01" + socket.inet_pton(socket.AF_INET, host) + struct.pack("!H", port)


def socks_request(connection: socket.socket, command: int, host: str, port: int) -> bytes:
    """Perform the no-auth SOCKS5 handshake and return the command reply."""
    connection.sendall(b"\x05\x01\x00")
    if recv_exact(connection, 2) != b"\x05\x00":
        raise AssertionError("fixture did not select no-auth SOCKS5")
    connection.sendall(b"\x05" + bytes((command,)) + b"\x00" + encode_target(host, port))
    header = recv_exact(connection, 4)
    address_type = header[3]
    address_length = 4 if address_type == 1 else 16 if address_type == 4 else 1 + recv_exact(connection, 1)[0]
    return bytes((header[1],)) + recv_exact(connection, address_length + 2)


class FixtureProcess:
    """Own one fixture subprocess and expose its loopback SOCKS port."""

    def __init__(self) -> None:
        self._stderr = ""
        self.process = subprocess.Popen(
            [sys.executable, str(FIXTURE)],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            env={**os.environ, "WT_TEST_ROUND": "1"},
        )
        assert self.process.stdout is not None
        deadline = time.monotonic() + 3
        line = ""
        while not line and time.monotonic() < deadline:
            line = self.process.stdout.readline()
            if not line:
                time.sleep(0.01)
        if not line:
            stderr = self.process.stderr.read() if self.process.stderr is not None else ""
            raise AssertionError(f"fixture did not publish its loopback port: {stderr}")
        self.metadata = json.loads(line)
        self.socks_port = int(self.metadata["socks"])

    def __enter__(self) -> "FixtureProcess":
        return self

    def __exit__(self, *_: object) -> None:
        self.process.terminate()
        self.process.wait(timeout=3)
        if self.process.stderr is not None:
            self._stderr = self.process.stderr.read()
        if self.process.stdout is not None:
            self.process.stdout.close()
        if self.process.stderr is not None:
            self.process.stderr.close()

    def logs(self) -> str:
        """Return fixture diagnostics after the process exits."""
        return self._stderr


class FixtureContractTest(unittest.TestCase):
    """Prove exact TEST-NET target validation and loopback-only echoing."""

    def test_tcp_connect_exact_destination_echoes_payload_and_rejects_wrong_target(self) -> None:
        with FixtureProcess() as fixture:
            with socket.create_connection(("127.0.0.1", fixture.socks_port), timeout=2) as connection:
                reply = socks_request(connection, 1, TEST_NET_V4, TCP_PORT)
                self.assertEqual(reply[0], 0)
                payload = b"c-abi-tcp-proof"
                connection.sendall(payload)
                self.assertEqual(connection.recv(len(payload)), payload)

            with socket.create_connection(("127.0.0.1", fixture.socks_port), timeout=2) as wrong:
                reply = socks_request(wrong, 1, TEST_NET_V4, TCP_PORT + 1)
                self.assertNotEqual(reply[0], 0)

    def test_udp_associate_echoes_framed_payload_and_rejects_wrong_target(self) -> None:
        with FixtureProcess() as fixture:
            with socket.create_connection(("127.0.0.1", fixture.socks_port), timeout=2) as control:
                reply = socks_request(control, 3, "0.0.0.0", 0)
                self.assertEqual(reply[0], 0)
                relay_port = struct.unpack("!H", reply[-2:])[0]
                payload = b"c-abi-udp-proof"
                with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as relay:
                    relay.settimeout(2)
                    for host in (TEST_NET_V4, TEST_NET_V6):
                        frame = b"\x00\x00\x00" + encode_target(host, UDP_PORT) + payload
                        relay.sendto(frame, ("127.0.0.1", relay_port))
                        echoed, _ = relay.recvfrom(2048)
                        self.assertEqual(echoed[-len(payload) :], payload)
                        self.assertEqual(echoed[: len(frame) - len(payload)], frame[: len(frame) - len(payload)])

                wrong = b"\x00\x00\x00" + encode_target(TEST_NET_V4, UDP_PORT + 1) + payload
                with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as relay:
                    relay.settimeout(0.5)
                    relay.sendto(wrong, ("127.0.0.1", relay_port))
                    with self.assertRaises(socket.timeout):
                        relay.recvfrom(2048)

    def test_ipv6_connect_destination_is_exact(self) -> None:
        with FixtureProcess() as fixture:
            with socket.create_connection(("127.0.0.1", fixture.socks_port), timeout=2) as connection:
                reply = socks_request(connection, 1, TEST_NET_V6, TCP_PORT)
                self.assertEqual(reply[0], 0)
                payload = b"c-abi-ipv6-proof"
                connection.sendall(payload)
                self.assertEqual(connection.recv(len(payload)), payload)

    def test_fixture_markers_are_bounded_and_redacted(self) -> None:
        fixture = FixtureProcess()
        try:
            with socket.create_connection(("127.0.0.1", fixture.socks_port), timeout=2) as connection:
                self.assertEqual(socks_request(connection, 1, TEST_NET_V4, TCP_PORT)[0], 0)
                connection.sendall(b"marker-proof")
                self.assertEqual(connection.recv(len(b"marker-proof")), b"marker-proof")
        finally:
            fixture.__exit__(None, None, None)
        logs = fixture.logs()
        for marker in ("fixture=accept", "fixture=request", "fixture=reply", "fixture=tcp_echo"):
            self.assertIn(marker, logs)
        self.assertNotIn(TEST_NET_V4, logs)
        self.assertNotIn(TEST_NET_V6, logs)


if __name__ == "__main__":
    unittest.main()
