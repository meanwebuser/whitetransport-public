#!/usr/bin/env python3
"""Fail closed on high-severity credentials or private topology in public trees."""

from __future__ import annotations

import argparse
import os
import re
import sys
from pathlib import Path

EXCLUDED_DIRS = {".git", "node_modules", ".next", "dist", "build"}
RULES = {
    "private-key": re.compile(rb"-----BEGIN [A-Z ]*PRIVATE KEY-----"),
    "github-token": re.compile(rb"\b(?:ghp|github_pat|gho|ghs|ghr)_[A-Za-z0-9_]{30,}\b"),
    "jwt": re.compile(rb"\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b"),
    "vk-token": re.compile(rb"\bvk1\.a\.[A-Za-z0-9_-]{20,}\b"),
    "ok-token": re.compile(rb"\b-n-[A-Za-z0-9_-]{16,}:[A-Za-z0-9_-]{12,}\b"),
    "private-topology": re.compile(
        b"(?:"
        + b"bezrabotnyi"
        + rb"\.com|/home/"
        + b"roomhacker"
        + rb"(?:/|\b)|\b(?:server(?:88|100)|vpn"
        + b"2"
        + rb")\b)"
    ),
}


def permitted_fixture(path: Path, rule: str) -> bool:
    return rule == "vk-token" and (path.name.endswith("_test.go") or path.name == "README.md")


def iter_files(root: Path):
    for current, dirs, files in os.walk(root):
        dirs[:] = [item for item in dirs if item not in EXCLUDED_DIRS]
        for name in files:
            yield Path(current, name)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("root", nargs="?", default=".")
    args = parser.parse_args()
    root = Path(args.root).resolve()
    findings: list[str] = []
    scanned = 0
    for path in iter_files(root):
        try:
            data = path.read_bytes()
        except OSError:
            continue
        scanned += 1
        relative = path.relative_to(root)
        for name, pattern in RULES.items():
            if pattern.search(data) and not permitted_fixture(relative, name):
                findings.append(f"{name}: {relative}")
    print(f"public secret scan: files={scanned} findings={len(findings)}")
    if findings:
        print("\n".join(findings), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
