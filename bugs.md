# WhiteTransport bugs

## Managed runtime test can discover a real checkout daemon

- Symptom. `TestManagedModeRequiresDaemonBinaryAndConfig` can report only
  `daemon-config` missing when the test expects both the daemon and its config.
- Location. `apps/native-gui/internal/runtime/resources_test.go` and the
  repository fallback candidates in `apps/native-gui/internal/runtime/resources.go`.
- Reproduction. Set `TMPDIR` to an absolute directory under the checkout's
  `.tmp/` and run
  `/usr/local/go/bin/go test ./internal/runtime -run TestManagedModeRequiresDaemonBinaryAndConfig`
  from `apps/native-gui` in a checkout with a daemon in a resolver fallback path.
- Context. The test's temporary directory has the real checkout as an ancestor,
  so the resolver can find an existing binary. The focused managed carrier
  projection tests pass in the same checkout.
