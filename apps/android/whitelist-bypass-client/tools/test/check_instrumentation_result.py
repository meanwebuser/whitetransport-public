#!/usr/bin/env python3
"""Validate the complete textual result emitted by AndroidJUnitRunner."""

from __future__ import annotations

import argparse
import re
from pathlib import Path


SUCCESS_RE = re.compile(r"^OK \([1-9][0-9]* tests?\)$", re.MULTILINE)
FAILURE_COUNT_RE = re.compile(r"Tests run: [0-9]+,\s+Failures: ([0-9]+)")
RESULT_CODE_RE = re.compile(r"^INSTRUMENTATION_CODE:\s*(-?[0-9]+)\s*$", re.MULTILINE)


def validate_result(output: str) -> str:
    """Return the success summary or raise ValueError for an incomplete/failed run."""
    if "FAILURES!!!" in output or "INSTRUMENTATION_FAILED:" in output:
        raise ValueError("AndroidJUnitRunner reported a failure marker")

    failure_counts = [int(value) for value in FAILURE_COUNT_RE.findall(output)]
    if any(count > 0 for count in failure_counts):
        raise ValueError(f"AndroidJUnitRunner reported failure count {max(failure_counts)}")

    success = SUCCESS_RE.search(output)
    if success is None:
        raise ValueError("AndroidJUnitRunner success marker is missing")

    result_codes = [int(value) for value in RESULT_CODE_RE.findall(output)]
    if not result_codes:
        raise ValueError("instrumentation result code is missing")
    # AndroidJUnitRunner uses Activity.RESULT_OK (-1) for a normal completed run;
    # some platform wrappers normalize it to 0. The JUnit marker remains decisive.
    if result_codes[-1] not in (-1, 0):
        raise ValueError(f"unexpected instrumentation result code {result_codes[-1]}")

    return success.group(0)


def main() -> int:
    """Validate one captured instrumentation output file."""
    parser = argparse.ArgumentParser()
    parser.add_argument("result_file", type=Path)
    args = parser.parse_args()

    try:
        summary = validate_result(args.result_file.read_text(encoding="utf-8", errors="replace"))
    except (OSError, ValueError) as error:
        print(f"Instrumentation result invalid: {error}")
        return 1

    print(f"Instrumentation result valid: {summary}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
