"""Accessibility probes shared by the Android auto-debug harness and tests."""

from __future__ import annotations

import re
import sys


_NODE_RE = re.compile(r"<node\b[^>]*>")
_BOUNDS_RE = re.compile(r'bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"')
_CONNECT_LABELS = {'content-desc="Connect WhiteTransport"', 'text="Подключиться"', 'text="Connect"'}
_DISCONNECT_LABELS = {
    'content-desc="Disconnect WhiteTransport"',
    'text="Отключиться"',
    'text="Отключить"',
    'text="Disconnect"',
}
_CONNECTED_LABELS = {'text="Connected"', 'text="Подключено"'}
_DISCONNECTED_LABELS = {'text="Disconnected"', 'text="Отключено"'}


def _has_label(xml: str, labels: set[str]) -> bool:
    return any(any(label in node for label in labels) for node in _NODE_RE.findall(xml))


def _center(xml: str, labels: set[str]) -> tuple[int, int] | None:
    for node in _NODE_RE.findall(xml):
        if not any(label in node for label in labels):
            continue
        match = _BOUNDS_RE.search(node)
        if match is None:
            continue
        left, top, right, bottom = (int(value) for value in match.groups())
        return ((left + right) // 2, (top + bottom) // 2)
    return None


def connect_center(xml: str) -> tuple[int, int] | None:
    """Return the tap center for a disconnected localized or English control."""

    return _center(xml, _CONNECT_LABELS)


def disconnect_center(xml: str) -> tuple[int, int] | None:
    """Return the tap center for a connected localized or English control."""

    return _center(xml, _DISCONNECT_LABELS)


def has_connect_control(xml: str) -> bool:
    return connect_center(xml) is not None


def is_disconnected(xml: str) -> bool:
    return _has_label(xml, _DISCONNECTED_LABELS) or has_connect_control(xml)


def is_connected(xml: str) -> bool:
    return _has_label(xml, _CONNECTED_LABELS) and disconnect_center(xml) is not None


def main() -> int:
    if len(sys.argv) != 2 or sys.argv[1] not in {"connect", "disconnect", "has-connect", "connected", "disconnected"}:
        raise SystemExit("usage: android_auto_debug_ui.py {connect|disconnect|has-connect|connected|disconnected}")
    xml = sys.stdin.read()
    operation = sys.argv[1]
    if operation == "connect":
        center = connect_center(xml)
        if center is None:
            return 1
        print(*center)
        return 0
    if operation == "disconnect":
        center = disconnect_center(xml)
        if center is None:
            return 1
        print(*center)
        return 0
    result = {
        "has-connect": has_connect_control,
        "connected": is_connected,
        "disconnected": is_disconnected,
    }[operation](xml)
    return 0 if result else 1


if __name__ == "__main__":
    raise SystemExit(main())
