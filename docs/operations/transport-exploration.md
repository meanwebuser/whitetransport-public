# Transport capability and exploration

WhiteTransport keeps transport selection provider-neutral. Every adapter must
implement the common Carrier contract; stream adapters may additionally
implement StreamDialer. A descriptor's Metrics are prior catalog knowledge.
An adapter can attach bounded posterior observations through its evidence state;
policy scores the effective metrics without knowing the adapter name.

## Hysteria 2

Configure an authenticated hysteria2 carrier with the existing carrier
configuration mechanism. The adapter uses the managed sing-box sidecar and
accepts hysteria2:// and hy2:// imports. The password remains in the
TokenStore or encrypted session profile. The sidecar JSON is generated in the
runtime directory and is not a release or receipt artifact.

## Tailscale

Configure tailscale only when the host already has an authenticated tailnet
and the named peer exposes a WhiteTransport SOCKS endpoint. The adapter checks
the local Tailscale backend state and the configured peer before probing the
proxy. It never changes Tailscale login/exit-node state and never falls back to
direct Internet dialing. Set udp_probe_address only when the peer runs an
explicit UDP echo service; this measures the overlay and does not advertise
consumer UDP packet routing by itself.

## Background exploration

Exploration is off unless the runtime config contains the following block:

    {
      "exploration": {
        "enabled": true,
        "interval_seconds": 60,
        "max_concurrency": 2,
        "tcp_probe_address": "example.com:443",
        "probe_timeout_seconds": 5,
        "persist_path": ".tmp/transport-exploration.json"
      }
    }

max_concurrency is capped at 8. The receipt exposes carrier and endpoint IDs,
prior values, observed values, probe source, timestamps, TCP/UDP result and a
bounded failure stage. It does not contain target addresses, cookies,
passwords or payloads. The receipt is written to persist_path; when that field
is omitted, state_file plus .exploration.json is used, or .tmp/transport-
exploration.json for a runtime without state_file. Exploration does not switch
the active route; the normal generic scorer consumes posterior evidence on
subsequent selection. A successful health probe does not claim throughput:
catalog bandwidth, cost and quota remain prior values unless a carrier supplies
a separate measured hook.

At daemon startup, the newest observed value from the receipt is promoted to
the next startup prior before egress candidates are sorted. This restore also
happens when background exploration is disabled, so enabled=false stops new
probes without discarding learned evidence. A later probe becomes the current
posterior and supersedes the startup prior for the measured fields. Failed
probes are persisted as fixed redacted failure codes rather than raw errors.
A live server-44 enablement still requires a separate config-only rollout and
an installed/consumer receipt.

The local API status includes TransportExploration when the daemon is running.
Use the existing headless whitetransportd --serve --config <path> and query
the local status endpoint; a healthy listening API alone is not payload proof.
