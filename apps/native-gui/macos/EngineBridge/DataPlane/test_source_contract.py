#!/usr/bin/env python3
"""Source contracts for the credential-free C-ABI engine smoke lane."""

from __future__ import annotations

import re
import unittest
from pathlib import Path


ROOT = Path(__file__).parent
CLIENT = (ROOT / "client" / "main.go").read_text()
FIXTURE = (ROOT / "local_fixture.py").read_text()
RUNNER = (ROOT / "runner.c").read_text()
SCRIPT = (ROOT / "test-c-abi-data-plane.sh").read_text()


class CABIEngineSmokeSourceContractTest(unittest.TestCase):
    """Guard the proof boundary and failure semantics without macOS APIs."""

    def test_lane_is_c_abi_engine_smoke_with_fixed_test_net_destinations(self) -> None:
        self.assertIn("C-ABI engine smoke", SCRIPT)
        self.assertIn("198.51.100.10", CLIENT)
        self.assertIn("2001:db8::10", CLIENT)
        self.assertNotIn("ipconfig getifaddr en0", SCRIPT)
        self.assertNotIn("ifconfig", SCRIPT)
        self.assertNotIn("--ipv4-host", SCRIPT)
        self.assertNotIn("--ipv6-host", SCRIPT)

    def test_fixture_has_no_target_listeners_or_external_dials(self) -> None:
        self.assertNotIn("socket.create_connection((host, port)", FIXTURE)
        self.assertNotRegex(FIXTURE, r"\.bind\(\(host,\s*0\)\)")
        self.assertIn("198.51.100.10", FIXTURE)
        self.assertIn("2001:db8::10", FIXTURE)
        self.assertIn("udp_echo", FIXTURE)
        self.assertIn("tcp_echo", FIXTURE)

    def test_runner_stops_engine_after_wait_even_when_child_fails(self) -> None:
        self.assertLess(RUNNER.index("waitpid"), RUNNER.index("WTStopTun2Socks"))
        self.assertIn("child_failure", RUNNER)
        self.assertIn("descriptor", RUNNER)
        self.assertIn("EBADF", RUNNER)

    def test_script_uses_detected_host_architecture_and_retains_failures(self) -> None:
        self.assertRegex(SCRIPT, r"GOARCH=\"\$HOST_ARCH\"")
        self.assertNotIn("GOARCH=arm64", SCRIPT)
        for name in ("fixture.log", "client.log", "runner.log", "manifest"):
            self.assertIn(name, SCRIPT)
        self.assertIn("artifact_dir=", SCRIPT)
        self.assertIn('if [[ "$trap_status" -eq 0 && "$RUNNER_EXIT" -eq 0 ]]; then', SCRIPT)
        self.assertIn('rm -rf "$ARTIFACT_DIR"', SCRIPT)
        self.assertIn("interpretation=", SCRIPT)

    def test_script_preserves_caller_owned_success_artifacts(self) -> None:
        self.assertIn('ARTIFACT_DIR_AUTO_CREATED=0', SCRIPT)
        self.assertIn('ARTIFACT_DIR_AUTO_CREATED=1', SCRIPT)
        self.assertRegex(
            SCRIPT,
            r'if \[\[ "\$ARTIFACT_DIR_AUTO_CREATED" -eq 1 \]\]; then\n'
            r'\s+printf .*retained=false.*\n'
            r'\s+rm -rf "\$ARTIFACT_DIR"\n'
            r'\s+else\n'
            r'\s+printf .*retained=true',
        )

    def test_script_prints_the_resolved_round_count(self) -> None:
        self.assertIn(
            "printf 'interpretation=pass proof_boundary=C-ABI-engine-smoke rounds=%s\\n' \"${WT_TEST_ROUNDS:-3}\"",
            SCRIPT,
        )
        self.assertNotIn("rounds=${WT_TEST_ROUNDS:-3}\\n'", SCRIPT)

    def test_cleanup_captures_runner_wait_status_immediately(self) -> None:
        self.assertRegex(
            SCRIPT,
            r'if \[\[ -n "\$RUNNER_PID" && "\$RUNNER_EXIT" == "not-started" \]\]; then\n'
            r'\s+local runner_wait_status=0\n'
            r'\s+wait "\$RUNNER_PID"\n'
            r'\s+runner_wait_status=\$\?\n'
            r'\s+RUNNER_EXIT="\$runner_wait_status"',
        )
        self.assertNotIn('[[ "$RUNNER_EXIT" != "not-started" ]] || RUNNER_EXIT=$?', SCRIPT)

    def test_markers_are_bounded_and_redacted(self) -> None:
        for marker in ("protocol=", "family=", "length=", 'probe_log("accept"', 'probe_log("request"', 'probe_log("reply"', 'probe_log("tcp_echo"', 'probe_log("udp_echo"', 'probe_log("error"'):
            self.assertIn(marker, CLIENT + FIXTURE + RUNNER)
        self.assertNotIn("host={host}", FIXTURE)
        self.assertNotIn("address.String()", CLIENT)


if __name__ == "__main__":
    unittest.main()
