#!/usr/bin/env python3
"""Run a loopback-only, credential-free SOCKS5 C-ABI engine fixture."""

from __future__ import annotations

import argparse
import ipaddress
import json
import os
import signal
import socket
import struct
import sys
import threading
from typing import Final


BUFFER_SIZE: Final = 65_535
TEST_NET_V4: Final = "198.51.100.10"
TEST_NET_V6: Final = "2001:db8::10"
TCP_PORT: Final = 443
UDP_PORT: Final = 53
ASSOCIATE_V4: Final = "0.0.0.0"
ASSOCIATE_V6: Final = "::"
ROUND_FILE: Final = os.environ.get("WT_C_ABI_ROUND_FILE", "")


def current_round() -> str:
    """Read the runner's current round without exposing the marker-file path."""
    if not ROUND_FILE:
        return os.environ.get("WT_TEST_ROUND", "0")
    try:
        with open(ROUND_FILE, encoding="ascii") as marker:
            return str(int(marker.read().strip()))
    except (OSError, ValueError):
        return "0"


def probe_log(
    event: str,
    *,
    command: str,
    family: str,
    port: int,
    protocol: str = "control",
    length: int = 0,
    result: str = "",
    error: str = "",
) -> None:
    """Emit bounded fixture markers without endpoint addresses or credentials."""
    fields = [
        f"fixture={event}",
        f"round={current_round()}",
        f"command={command}",
        f"family={family}",
        f"protocol={protocol}",
        f"port={port}",
        f"length={length}",
    ]
    if result:
        fields.append(f"result={result}")
    if error:
        fields.append(f"error={error}")
    print(" ".join(fields), file=sys.stderr, flush=True)


def read_exact(connection: socket.socket, count: int) -> bytes:
    """Read exactly ``count`` bytes or raise ``EOFError``."""
    chunks = bytearray()
    while len(chunks) < count:
        chunk = connection.recv(count - len(chunks))
        if not chunk:
            raise EOFError("SOCKS connection closed")
        chunks.extend(chunk)
    return bytes(chunks)


def read_address(connection: socket.socket, address_type: int) -> tuple[str, int]:
    """Read one RFC 1928 address and port from a SOCKS control request."""
    if address_type == 1:
        host = socket.inet_ntop(socket.AF_INET, read_exact(connection, 4))
    elif address_type == 4:
        host = socket.inet_ntop(socket.AF_INET6, read_exact(connection, 16))
    elif address_type == 3:
        length = read_exact(connection, 1)[0]
        host = read_exact(connection, length).decode("ascii")
    else:
        raise ValueError(f"unsupported SOCKS address type {address_type}")
    return host, struct.unpack("!H", read_exact(connection, 2))[0]


def encode_address(host: str, port: int) -> bytes:
    """Encode one numeric RFC 1928 address and port."""
    parsed = socket.inet_pton(socket.AF_INET6 if ":" in host else socket.AF_INET, host)
    address_type = 4 if len(parsed) == 16 else 1
    return bytes((address_type,)) + parsed + struct.pack("!H", port)


def parse_udp_request(data: bytes) -> tuple[str, int, bytes]:
    """Decode one unfragmented SOCKS5 UDP request."""
    if len(data) < 4 or data[:3] != b"\x00\x00\x00":
        raise ValueError("invalid SOCKS UDP prefix")
    address_type = data[3]
    cursor = 4
    if address_type == 1:
        host = socket.inet_ntop(socket.AF_INET, data[cursor : cursor + 4])
        cursor += 4
    elif address_type == 4:
        host = socket.inet_ntop(socket.AF_INET6, data[cursor : cursor + 16])
        cursor += 16
    elif address_type == 3:
        length = data[cursor]
        cursor += 1
        host = data[cursor : cursor + length].decode("ascii")
        cursor += length
    else:
        raise ValueError(f"unsupported SOCKS UDP address type {address_type}")
    if len(data) < cursor + 2:
        raise ValueError("truncated SOCKS UDP destination")
    port = struct.unpack("!H", data[cursor : cursor + 2])[0]
    return host, port, data[cursor + 2 :]


def family_for(host: str) -> str:
    """Return a stable family marker for a numeric or domain destination."""
    try:
        return "ipv6" if ipaddress.ip_address(host).version == 6 else "ipv4"
    except ValueError:
        return "domain"


def expected_target(command: str, host: str, port: int) -> tuple[str, str] | None:
    """Return the expected family and marker for one exact destination tuple."""
    if command == "connect":
        expected = {TEST_NET_V4: ("ipv4", TCP_PORT), TEST_NET_V6: ("ipv6", TCP_PORT)}
    elif command == "udp":
        expected = {TEST_NET_V4: ("ipv4", UDP_PORT), TEST_NET_V6: ("ipv6", UDP_PORT)}
    elif command == "udp_associate":
        expected = {ASSOCIATE_V4: ("ipv4", 0), ASSOCIATE_V6: ("ipv6", 0)}
    else:
        return None
    family, expected_port = expected.get(host, ("", -1))
    return (family, host) if family and port == expected_port else None


def reply(connection: socket.socket, code: int, host: str = ASSOCIATE_V4, port: int = 0) -> None:
    """Write one SOCKS5 reply using only loopback bind data."""
    connection.sendall(b"\x05" + bytes((code,)) + b"\x00" + encode_address(host, port))


def echo_stream(connection: socket.socket, family: str, port: int) -> None:
    """Echo one bounded TCP payload directly on the SOCKS control stream."""
    payload = connection.recv(BUFFER_SIZE)
    if not payload:
        probe_log("tcp_echo", command="connect", family=family, port=port, protocol="tcp", result="empty")
        return
    connection.sendall(payload)
    probe_log("tcp_echo", command="connect", family=family, port=port, protocol="tcp", length=len(payload), result="success")


def relay_udp(relay: socket.socket, control: socket.socket) -> None:
    """Echo framed UDP payloads while their loopback control association lives."""
    relay.settimeout(0.2)
    control.settimeout(0.0)
    while True:
        try:
            if control.recv(1, socket.MSG_PEEK) == b"":
                return
        except BlockingIOError:
            pass
        try:
            request, client = relay.recvfrom(BUFFER_SIZE)
        except socket.timeout:
            continue
        try:
            host, port, payload = parse_udp_request(request)
            family = family_for(host)
            if expected_target("udp", host, port) is None:
                probe_log("error", command="udp", family=family, port=port, protocol="udp", length=len(payload), result="rejected")
                continue
            relay.sendto(b"\x00\x00\x00" + encode_address(host, port) + payload, client)
            probe_log("udp_echo", command="udp", family=family, port=port, protocol="udp", length=len(payload), result="success")
        except (OSError, ValueError, struct.error) as error:
            probe_log("error", command="udp", family="unknown", port=0, protocol="udp", result="failure", error=type(error).__name__)


def _handle_socks(connection: socket.socket) -> None:
    """Handle one SOCKS5 CONNECT or UDP ASSOCIATE request."""
    command_name = "unknown"
    family = "unknown"
    port = 0
    with connection:
        version, method_count = read_exact(connection, 2)
        methods = read_exact(connection, method_count)
        if version != 5 or 0 not in methods:
            probe_log("error", command=command_name, family=family, port=port, result="rejected")
            return
        connection.sendall(b"\x05\x00")
        version, command, reserved, address_type = read_exact(connection, 4)
        command_name = {1: "connect", 3: "udp_associate"}.get(command, "unsupported")
        family = {1: "ipv4", 4: "ipv6", 3: "domain"}.get(address_type, "unknown")
        if version != 5 or reserved != 0:
            probe_log("error", command=command_name, family=family, port=port, result="rejected")
            return
        host, port = read_address(connection, address_type)
        probe_log("request", command=command_name, family=family, port=port, result="received")
        if command == 1:
            if expected_target("connect", host, port) is None:
                reply(connection, 2)
                probe_log("error", command=command_name, family=family, port=port, result="rejected")
                return
            reply(connection, 0)
            probe_log("reply", command=command_name, family=family, port=port, result="success")
            echo_stream(connection, family, port)
            return
        if command == 3:
            if expected_target("udp_associate", host, port) is None:
                reply(connection, 2)
                probe_log("error", command=command_name, family=family, port=port, result="rejected")
                return
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as relay:
                relay.bind(("127.0.0.1", 0))
                relay_port = relay.getsockname()[1]
                reply(connection, 0, port=relay_port)
                probe_log("reply", command=command_name, family=family, port=port, result="success")
                relay_udp(relay, connection)
            return
        reply(connection, 7)
        probe_log("error", command=command_name, family=family, port=port, result="rejected")


def handle_socks(connection: socket.socket) -> None:
    """Handle a SOCKS request and convert all failures into bounded markers."""
    try:
        _handle_socks(connection)
    except (EOFError, OSError, ValueError, struct.error) as error:
        probe_log("error", command="unknown", family="unknown", port=0, result="failure", error=type(error).__name__)


def socks_accept(listener: socket.socket) -> None:
    """Accept SOCKS fixture connections on loopback only."""
    while True:
        connection, _ = listener.accept()
        probe_log("accept", command="control", family="unknown", port=0, result="success")
        threading.Thread(target=handle_socks, args=(connection,), daemon=True).start()


def main() -> None:
    """Start the loopback SOCKS fixture and publish its control port once."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--socks-port", type=int, default=0)
    arguments = parser.parse_args()
    socks = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    socks.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    socks.bind(("127.0.0.1", arguments.socks_port))
    socks.listen()
    print(json.dumps({"socks": socks.getsockname()[1]}), flush=True)
    threading.Thread(target=socks_accept, args=(socks,), daemon=True).start()
    signal.pause()


if __name__ == "__main__":
    main()
