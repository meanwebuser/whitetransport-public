# Public mirror export

The canonical private-to-public route is `git-private2public` configured by
`.gitpublic/`. The denylist removes all of `secrets/`, `ops/`, broad `config/`,
`third_party/`, `.github/`, and generated runtime products before the public
tree is scanned. The only exceptions are the reviewed entries in
`.gitpublic/public-allowlist`; the exporter copies them into the public overlay.

From a clean private checkout, the one command that refreshes, scans, and
publishes the reproducible public mirror is:

```bash
bash .gitpublic/export-public.sh publish
```

Use `bash .gitpublic/export-public.sh verify` to perform the same overlay
refresh and sanitized-tree scan without pushing. Run
`bash .gitpublic/test-public-export-contract.sh` first; it rejects stale
allowlist copies, missing release automation, or an unsafe workflow contract.

The publisher inherits `GIT_SSH_COMMAND`; publication must use the dedicated
`meanwebuser` identity and a verified GitHub host key. The export has no
credential input and fails the built-in plus project public-secret scans before
a public ref can be pushed.

To build or repair assets for an existing immutable public tag without a local
GitHub API token, put that tag in
`.gitpublic/public/.github/public-release-request` and publish normally. The
public `main` push validates the source, checks out that exact tag, builds and
audits the provisioning-only daemon and GUI archive, then uploads them with the
workflow's short-lived `GITHUB_TOKEN`. Remove the request after anonymous
download-back verification so later source-only exports do not rebuild it.
