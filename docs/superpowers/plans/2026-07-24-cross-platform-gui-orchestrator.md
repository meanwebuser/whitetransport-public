# Cross-platform GUI acceptance orchestrator

## Goal

Add one fail-closed entry point that runs the existing Android, macOS, and
Windows GUI acceptance lanes sequentially, stores each lane's logs and JSON
result under one run directory, and emits one aggregate JSON result. Ubuntu is
excluded from the default run to avoid competing with other local work; it
remains available only when explicitly listed (or from a container). The first
implementation is sequential; the result schema and runner boundaries
must make a later `--parallel` mode possible without changing individual
platform harnesses.

## Acceptance contract

The orchestrator is `ops/tools/cross-platform-gui-acceptance.sh`.

```text
cross-platform-gui-acceptance.sh
  --platforms android,macos,windows
  --result-dir output/cross-platform-gui/<run-id>
  [--dry-run]
```

For each platform it creates `<result-dir>/<platform>/`, sets these variables
for the platform runner, and waits for the runner to exit before starting the
next one:

```text
WT_GUI_ACCEPTANCE_PLATFORM
WT_GUI_ACCEPTANCE_RESULT_DIR
WT_GUI_ACCEPTANCE_RESULT_JSON
WT_GUI_ACCEPTANCE_LOG
```

Each runner must write a JSON result to `WT_GUI_ACCEPTANCE_RESULT_JSON` (or the
orchestrator's built-in adapter must copy the legacy result there). A passing
lane must prove all of:

```json
{
  "schemaVersion": 1,
  "platform": "ubuntu",
  "passed": true,
  "guiPassed": true,
  "payloadPassed": true,
  "cleanupPassed": true,
  "directIp": "198.51.100.10",
  "proxyIp": "203.0.113.20",
  "ipChanged": true
}
```

`directIp` and `proxyIp` must be non-empty valid IP strings and must differ.
The orchestrator never converts a process/port-only result into a pass. A
platform without a configured runner or without real IP fields is a recorded
failed lane, not a skipped success.

## Files

- Create `ops/tools/cross-platform-gui-acceptance.sh`.
- Create `ops/tools/test-cross-platform-gui-acceptance-contract.sh`.
- Create `docs/superpowers/plans/2026-07-24-cross-platform-gui-orchestrator.md`.
- Update `.agents/worklog.md` after implementation and verification.

## Execution steps

### Step 1: Red contract

Write the contract test before the production script. It creates four fake
runner scripts that append their platform name to a shared order file and
write canonical JSON results. Run the orchestrator with `--platforms
ubuntu,android,macos,windows`; assert:

1. each runner ran exactly once;
2. the order file is exactly Ubuntu, Android, macOS, Windows;
3. the aggregate JSON has four passing lanes and `executionMode: sequential`;
4. the aggregate fails when one fake runner emits equal IPs;
5. `--dry-run` emits the same order without executing runners.

Run:

```bash
bash ops/tools/test-cross-platform-gui-acceptance-contract.sh
```

Expected red failure before implementation: the orchestrator path does not
exist.

### Step 2: Minimal orchestrator

Implement strict Bash argument parsing, per-run directories, platform order,
runner invocation, result validation, and aggregate JSON generation with
Python's standard library. Runner commands come from
`WT_GUI_<PLATFORM>_RUNNER`; the script must reject an unset/non-executable
runner. Use `bash "$runner"` so platform wrappers may be shell scripts. Capture
stdout/stderr in the platform log while preserving the runner exit code.

The validator accepts only `schemaVersion == 1`, matching platform, boolean
`guiPassed`, `payloadPassed`, and `cleanupPassed`, valid `directIp`/`proxyIp`,
and `directIp != proxyIp`. It records `runnerExitCode`, `startedAt`,
`completedAt`, `resultPath`, and a sanitized error string. It must write the
aggregate result atomically even when a lane fails, then continue sequential
execution through every configured lane and return non-zero if any lane fails.

### Step 3: Green contract and refactor

Run the contract test again and verify the exact order and fail-closed equal-IP
case. Refactor only after green: keep the validator in the same script until a
real platform adapter needs extraction. Run shell syntax checks and `shellcheck`
when available.

### Step 4: Wire existing runtime lanes

Add documented built-in adapters without changing their internals:

- Explicit Ubuntu runs invoke `ops/build/test-native-gui-managed-payload.sh` and map its
  `directIp`/`socksIp`, `passed`, and cleanup evidence.
- Android invokes `apps/android/whitelist-bypass-client/tools/test/android_acceptance.sh`;
  it must expose real direct/proxy IP fields through its result before this
  lane can pass the cross-platform gate. Existing payload-only output is a
  recorded proof gap, never promoted to `ipChanged`.
- macOS invokes the configured exact bundle through
  `apps/native-gui/scripts/mac-acceptance.sh`; its system-route result must be
  paired with an explicit direct baseline and expected exit IP.
- Windows invokes the configured off-host runner; the existing verifier is
  applied to the resulting acceptance artifact and the canonical adapter
  requires direct/proxy IP fields.

These adapters are configuration-level wrappers. They do not silently invent
credentials, node IDs, bundles, remote hosts, or expected exit IPs.

### Step 5: Real runtime proof

Run the orchestrator sequentially with real configured runners. Archive the
aggregate JSON and each platform log under `output/cross-platform-gui/<run-id>`.
For each platform report direct IP, proxy/system-route IP, selected node,
payload proof, cleanup proof, and exact result path. If a platform is not
available, leave its failed artifact and stop; do not claim a full pass.

## Self-review

- No lane passes from an open port or process existence alone.
- A runner failure does not skip later lanes: every configured platform is
  attempted sequentially, and the aggregate fails if any lane fails.
- A future parallel mode can reuse the per-platform result directories and
  validator, but is intentionally not implemented in this change.
- The contract test uses fake runners only for orchestration behavior; the
  real runtime proof remains the configured platform harnesses.
