# Android Runtime Proof and Platform-Gated Acceptance Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use test-driven-development for every behavior change and preserve the platform sequence gate.

**Goal:** Close the Go daemon local runtime proof, then produce a self-contained Android GUI artifact and installed runtime proof with redacted logs, test mode, payload transfer, and cleanup evidence.

**Architecture:** The first gate is the canonical `core/go/cmd/whitetransportd` exercised by `core/go/tests/test-local.sh` through two real daemons, isolated `file.mailbox` carriers, strict SOCKS payloads, failover, and encrypted-frame artifacts. Android uses the shared UI plus `CapacitorVpnCoordinator`, the embedded Go runtime, loopback SOCKS, and `VpnService`/tun2socks; credentialed builds must be generated from the encrypted TokenStore sources and verified before install.

**Tech Stack:** Go 1.24+, Bash, Python verifier, Gradle/Kotlin, Capacitor Android, adb, Samsung physical device acceptance harness.

## Global Constraints

- Do not advance to Ubuntu, Android, Windows, or macOS platform closure without the preceding stage's real runtime proof, structured logs, test mode, and cleanup evidence.
- Do not edit or overwrite unrelated dirty files; the current worktree contains concurrent Windows/native-GUI changes.
- Never put production credentials in source, generated public artifacts, or logs; use `secrets/generate-token-store.sh` and `secrets/android-build.sh` for credentialed builds.
- Android artifacts advertised as ready must contain the embedded Go runtime and complete client TokenStore config; provisioning-only builds are not acceptance artifacts.
- A passing unit/JVM suite is not Android runtime proof; the installed APK must reach authoritative `TUNNEL_ACTIVE`, transfer a real payload, and leave no active tunnel/process state.

---

### Task 1: Reconfirm the canonical Go local stage gate

**Files:**
- Read: `core/go/tests/test-local.sh`
- Read: `core/go/tests/test-local-contract.sh`
- Evidence: `trash/logs/go-daemon-blackbox/<immutable-run-id>/`
- Status: `PROJECT_STATUS.md`

**Interfaces:**
- Consumes: current repository commit and local daemon/config templates.
- Produces: structured `desktop-local-fast` JSON with daemon count, routes, nonce validity, failover evidence, frame artifacts, binary SHA-256, logs, and cleanup state.

- [ ] **Step 1: Run the existing black-box contract against the exact current tree**

Run:

```bash
WT_DEBUG=1 core/go/tests/test-local-contract.sh
```

Expected: exit 0; JSON reports `daemonCount=2`, `transport=file.mailbox`, `socksStrict=true`, both nonce checks true, primary failure observed, backup route used, and non-zero encrypted frame artifacts.

- [ ] **Step 2: Preserve the exact output and logs**

Copy only redacted run output into `trash/logs/go-daemon-blackbox/<timestamp>/` and record the exact commit, binary SHA-256, and dirty-tree state. Do not substitute a previous “latest” artifact.

- [ ] **Step 3: If the gate is red, add or update a focused failing regression before implementation**

The regression must reproduce the observed failure through the real local script/runtime boundary; run the narrow test and capture the expected failure before changing production code.

- [ ] **Step 4: Make the smallest fix and rerun the same contract**

Run:

```bash
WT_DEBUG=1 core/go/tests/test-local-contract.sh
```

Expected: exit 0 with the complete structured proof and cleanup evidence. Do not mark the stage closed if only unit tests pass.

- [ ] **Step 5: Commit the logical stage-1 fix or evidence-only status update**

Run the repository secret scan before committing:

```bash
./secrets/detect-secrets.sh fast high
```

Use a focused `fix(core-runtime): ...`, `test(core-runtime): ...`, or `docs(status): ...` commit as applicable.

### Task 2: Validate Android artifact contracts before device work

**Files:**
- Read/modify only if a failing regression proves a defect: `ops/build/verify-android-apk.py`, `ops/build/test_verify_android_apk.py`
- Read: `ops/build/build-mobile-runtime-android.sh`, `ops/build/android-dev-apk.sh`
- Read: `apps/android/whitelist-bypass-client/app/src/test/java/bypass/whitelist/CapacitorVpnCoordinatorTest.kt`

**Interfaces:**
- Consumes: Android source, generated client-only runtime config, Go runtime AAR, and required platform assets.
- Produces: a verifier-approved APK containing `assets/wt-runtime-config.json`, the Go JNI library, tun2socks/sing-box assets required by the selected egress, and no inline/user-session credentials.

- [ ] **Step 1: Run the verifier and Android JVM contract suite first**

Run:

```bash
python3 -m unittest ops/build/test_verify_android_apk.py -v
cd apps/android/whitelist-bypass-client
ALLOW_EMPTY_MOBILE_SECRETS=1 ALLOW_NO_AUTOUPDATE=1 ./gradlew test
```

Expected: verifier tests and JVM tests pass with no credential leakage.

- [ ] **Step 2: If a contract fails, write the smallest failing regression and run it red**

The test must assert the artifact/runtime contract at the boundary that failed, not merely inspect a helper implementation.

- [ ] **Step 3: Implement only the minimal contract fix, then rerun focused tests and the full Android JVM suite**

Expected: the new regression is green and all existing Android tests remain green.

- [ ] **Step 4: Generate a credentialed acceptance APK from encrypted sources**

Run the repository's credentialed Android build path with `WT_EMBED_RUNTIME_CONFIG=1`; verify the final APK rather than an intermediate artifact. Record the immutable APK path and SHA-256.

### Task 3: Run installed Android runtime acceptance on the physical device

**Files:**
- Read/execute: `apps/android/whitelist-bypass-client/tools/test/run_android_acceptance.sh`
- Read/execute: `apps/android/whitelist-bypass-client/tools/test/run_android_auto_debug.sh`
- Evidence: `trash/logs/android-acceptance/<immutable-run-id>/`
- Status: `PROJECT_STATUS.md`

**Interfaces:**
- Consumes: exact verifier-approved APK and a reachable, isolated test node/profile.
- Produces: redacted structured acceptance JSON containing package/APK identity, permission state, authoritative `TUNNEL_ACTIVE`, real SOCKS payload result, selected node/runtime status, logs, disconnect, and zero-state cleanup.

- [ ] **Step 1: Run device preflight and fail clearly on environmental blockers**

Confirm adb device identity, unlocked/keyguard state, package installability, and that the acceptance harness uses the exact APK SHA-256.

- [ ] **Step 2: Install and launch the exact APK through the acceptance harness**

Run:

```bash
apps/android/whitelist-bypass-client/tools/test/run_android_acceptance.sh
```

Expected: the harness reaches the real UI/runtime path, not a mocked status or a port-open-only check.

- [ ] **Step 3: Verify the runtime proof boundary**

Require all of: authoritative `TUNNEL_ACTIVE`, real payload bytes through the app VPN path, redacted runtime logs, node/session identity, disconnect completion, VPN permission cleanup, and no leaked daemon/tun2socks processes.

- [ ] **Step 4: If device acceptance fails, reproduce with a focused regression before changing code**

Record the exact immutable evidence path and classify the failure as app/runtime, package/config, provider/discovery, or device environment. Do not relabel a device/environment failure as Android readiness.

- [ ] **Step 5: Commit only verified Android fixes and update the single status source**

Run the secret scan, commit the fix, and update `PROJECT_STATUS.md` only with the evidence-backed Android status. Keep technical evidence in `trash/logs/` and `docs/` as appropriate; do not duplicate readiness claims elsewhere.

### Task 4: Hold the next platform gates

**Files:**
- Read: `PROJECT_STATUS.md`
- Read: `.agents/handoffs/platform-sequence-20260723.md`
- Modify: `.agents/worklog.md`

**Interfaces:**
- Consumes: immutable stage-1 and Android acceptance evidence.
- Produces: explicit handoff stating which stage is closed, which is partial, and the exact next action.

- [ ] **Step 1: Refuse platform advancement when any required proof is missing**

A build, unit suite, or GUI launch without real payload, logs, test mode, and cleanup remains partial.

- [ ] **Step 2: Append the final worklog entry with exact evidence paths**

Record red/green commands, commits, blockers, and next action in English as required by the repository.

- [ ] **Step 3: Leave the repository clean before removing the Android lock**

Run `git status --short`, commit tracked task changes, then remove the lock only after the commit succeeds.
