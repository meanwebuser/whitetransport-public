# WhiteTransport bugs

## Ubuntu package daemon reports development build identity

- Symptom. `/v1/build` and `--version` report `dev` and `unknown` for
  the daemon in the Ubuntu client archive.
- Location. `build_ubuntu` in `ops/build/build-supported-matrix.sh`
  builds the Go command without the version linker flags used by
  `ops/build/build-embedded.sh`.
- Reproduction. Run `resources/whitetransportd --version` from the
  Ubuntu 0.5.3 r9 archive.
- Context. `go version -m` still reports the exact clean VCS revision.
  The package manifest and executable SHA-256 preserve candidate identity.

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

## VK history flood penalty is shorter than the provider penalty

- Symptom. A VK Documents tunnel receives the TLS server flight but then the
  stream ends before the request completes. The client log reports
  `vk docs messages.getHistory throttled after retries (VK code 9)` and the
  tunnel closes without a payload. The same failure appears on the
  VK Messages route as a TLS error.
- Location. `core/go/internal/carriers/vk_messages.go` `waitVKRetry` and
  `waitVKBackoff`, used by `core/go/internal/carriers/vk_docs.go` for
  `messages.getHistory`.
- Reproduction. Run two VK Documents acceptance passes back to back against
  the live VPN2 node with the installed Mac clients polling the same VK
  accounts. The first pass delivers the flight in about six seconds; the
  second one is refused with VK error code 9. A direct
  `messages.getHistory` call for the same chat and token answers in 0.2 s,
  so the provider itself is healthy.
- Context. VK error code 9 is a flood penalty. Without a `retry_after` field
  the carrier waits 1, 2 and 4 seconds and then gives up, while the provider
  penalty lasts longer, so the retry series never meets an unthrottled
  window. The carrier should honour the provider penalty window instead of
  a fixed three-attempt series.

## One VK conversation is polled faster than the provider allows

- Symptom. Every carrier that reads the carrier chat is refused with VK
  error code 9 as soon as a client session is active, even when a single
  acceptance run polls it alone. The failure is independent of the number of
  messages in the chat.
- Location. `core/go/internal/tunnel/physical_reader.go`
  `physicalBindingKey` and `boundedCarrierPollInterval`, and the carrier
  limits in `core/go/internal/carriers/standard.go`.
- Reproduction. Hold the installed Mac clients down, keep one acceptance
  client on the VPN2 node and watch the node poll the carrier chat: the
  `vk.egress` reader polls every 500 ms, the `vk.docs.egress` reader every
  333 ms and the control scope every 2 s, so the same conversation sees
  about five history reads per second. VK allows far fewer reads per
  conversation and answers with error code 9.
- Context. `physicalBindingKey` deliberately keeps one poller per provider
  adapter and address, so `vk.messages`, `vk.egress` and `vk.docs.256`
  read the same conversation through three separate pollers, and the
  advertised per-carrier poll budgets are counted separately although the
  provider applies one limit per conversation. The poll budget has to be
  shared per conversation, and the carrier has to recover from the flood
  penalty instead of giving up after a fixed number of attempts.


## A fresh bulk reader downloads every attachment in the shared chat window

- Symptom. A tunnel that starts on the VK Documents carrier waits for the
  first response while the carrier downloads unrelated attachments that other
  tunnels left in the same chat, so the upstream handshake can time out
  before the first response arrives.
- Location. `core/go/internal/carriers/vk_docs.go` `VKDocsCarrier.Read`.
  Every message in the history window with a document attachment is
  downloaded, although the bulk protocol carries the envelope ID in the
  message text.
- Reproduction. Fill the carrier chat with tunnel documents, then open a new
  tunnel on the `vk.docs.256` carrier and watch the read latency grow with
  the number of attachments in the window.
- Context. A reader that waits for one envelope can skip the attachments of
  other envelopes without downloading them. The envelope ID is available in
  the message text, so no protocol change is needed.

