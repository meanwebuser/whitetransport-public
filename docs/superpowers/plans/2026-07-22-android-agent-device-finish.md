# Android agent-device Finish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the connected Android client reproducibly discover an endpoint, connect real VPN traffic, and disconnect cleanly while using agent-device for UI actions and evidence.

**Architecture:** Android is the only active acceptance target for this milestone. `agent-device` drives the installed APK through accessibility refs and stores screenshots/logs; the existing Android auto-debug harness remains the structured end-to-end runner. Runtime and discovery fixes stay in the existing Go/Capacitor boundaries.

**Tech Stack:** Node.js 22.12+, `agent-device` 0.20.x, ADB, Android APK, Go runtime, existing Android test harness.

## Global Constraints

- Use USB serial `R5CR702SRFP` explicitly; do not select the duplicate Wi-Fi ADB target.
- Do not print, copy, or commit credentials, cookies, or `/tmp/sudopass`.
- Do not weaken credential guards or replace real discovery with a mock.
- Every runtime defect follows red reproduction, one minimal fix, then green local and device proof.
- Evidence paths must be explicit under `output/android-agent-device/20260722/`.

### Task 1: Document the Android agent-device workflow

**Files:**
- Modify: `AGENTS.md` in the Android testing section
- Create: `docs/superpowers/plans/2026-07-22-android-agent-device-finish.md`

- [ ] **Step 1: Add the exact prerequisites and commands**

Document Node/ADB requirements, `npm install -g agent-device@latest`, `agent-device doctor`, USB selection with `ANDROID_SERIAL=R5CR702SRFP`, and the open/snapshot/press/screenshot/logs/close loop.

- [ ] **Step 2: Add evidence and cleanup rules**

Require screenshots, accessibility snapshots, result JSON, log path, and explicit session close; state that `agent-device` supplements rather than replaces the structured auto-debug harness.

- [ ] **Step 3: Run markdown and secret checks**

Run `git diff --check` and `./secrets/detect-secrets.sh fast high`.

### Task 2: Reproduce Android discovery failure with agent-device evidence

**Files:**
- Create: `output/android-agent-device/20260722/repro.json`
- Create: `output/android-agent-device/20260722/connect-snapshot.txt`

- [ ] **Step 1: Open the exact installed APK**

Run:

```bash
ANDROID_SERIAL=R5CR702SRFP agent-device open bypass.whitelist --platform android --relaunch --no-record
ANDROID_SERIAL=R5CR702SRFP agent-device snapshot -i --platform android
```

- [ ] **Step 2: Tap the current connect ref and capture the settled diff**

Use the current ref from the latest snapshot:

```bash
ANDROID_SERIAL=R5CR702SRFP agent-device press <current-connect-ref> --platform android --settle
ANDROID_SERIAL=R5CR702SRFP agent-device screenshot output/android-agent-device/20260722/connect.png --platform android
```

- [ ] **Step 3: Preserve the structured failure**

Run the existing exact APK harness with `DEVICE=R5CR702SRFP`, save its result JSON under the evidence directory, and close the agent-device session.

### Task 3: Fix the first failing discovery boundary

**Files:**
- Modify: the exact runtime/discovery file identified by Task 2 and the audit
- Test: the lowest existing Go/Android fixture that reproduces the missing endpoint or invalid runtime config

- [ ] **Step 1: Add a red regression test**

The test must fail against the current code and assert the concrete boundary (for example, missing embedded token-store binding, carrier initialization, or endpoint advertisement parsing).

- [ ] **Step 2: Implement one minimal fix**

Keep transport semantics unchanged; fix only the source of the missing discovery input.

- [ ] **Step 3: Run focused local tests and the Android build**

Run the regression test, the existing Android local verification, and build the exact APK used for device acceptance.

### Task 4: Prove connect, split behavior, and cleanup on Samsung

**Files:**
- Create: `output/android-agent-device/20260722/connect-result.json`
- Create: `output/android-agent-device/20260722/disconnect-result.json`

- [ ] **Step 1: Install and launch the exact APK**

Use the existing harness with the immutable APK path and USB serial.

- [ ] **Step 2: Verify real connect**

Use agent-device accessibility evidence plus the harness result to show an endpoint, connected transport, and VPN state.

- [ ] **Step 3: Verify split traffic**

Probe one destination routed through VPN and one explicit bypass/control destination; record both paths in structured JSON.

- [ ] **Step 4: Disconnect and verify restoration**

Confirm VPN is disconnected, the original route is restored, child processes are gone, and logs/result JSON are saved.

### Task 5: Commit and publish the instruction/runtime changes

- [ ] **Step 1: Run the final scoped checks**

Run the focused tests, Android build contract, `git diff --check`, and secret scan.

- [ ] **Step 2: Commit only the intended files**

Use `docs(android): standardize agent-device acceptance` for instruction-only changes and a separate `fix(android): ...` commit for runtime changes.

- [ ] **Step 3: Push `main` and report exact evidence paths**

Do not stage unrelated pre-existing dirty files.
