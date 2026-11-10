# Spike B: Google Cloud SQL from Rust

Date: 2026-11-10. Ticket: PGMI-847. Verdict: **write pgmi's own Cloud SQL
dialer; take credentials from Google's `google-cloud-auth`; build the macOS
binaries natively**.

No real Cloud SQL instance was used: by the owner's decision of 2026-11-10,
Google sign-in is implemented but not tested against a real cloud. Everything
below comes from the crates' source and from builds.

## Question

Google ships Cloud SQL connectors for Go, Python, Java and Node, not Rust. The
Go binary uses `cloud.google.com/go/cloudsqlconn` with IAM database
authentication, connecting to the instance's primary IP. Use the community crate
`cloud-sql-connector`, or write our own?

## The community crate (`cloud-sql-connector` 0.8.0)

MIT, one maintainer, released 2026-10-11, about 58 000 recent downloads, built
on Google's official `google-cloud-auth` and `google-cloud-sql-v1`. It does what
pgmi needs today (ephemeral certificate, TLS 1.3, IAM login token, primary IP).
Three findings rule it out as it stands:

1. **It does not compile for Windows.** It imports `tokio::net::UnixListener`
   unconditionally (its Unix-socket proxy). The Go binary supports `--google`
   on Windows.
2. **It cannot be pointed at a fake Admin API.** Both constructors build the
   Admin API client with defaults and expose no endpoint, so no black-box case
   could exercise it.
3. **It ties the build to `aws-lc-rs`** for RSA key generation, where spike A
   chose `ring`.

## The macOS finding (applies to any choice)

Cross-building with GoReleaser and Zig (spike D), the Linux and Windows targets
build, but **macOS fails to link**: `unable to find framework 'Security'` and
`'CoreFoundation'`. The cause is not the crate: `google-cloud-auth` enables
`reqwest` with `rustls-platform-verifier` unconditionally, and that verifier
links Apple's frameworks, which Zig does not ship. `google-cloud-auth` alone
builds for `x86_64-pc-windows-gnu`, `aarch64-pc-windows-gnullvm` and
`x86_64-unknown-linux-musl`.

So any use of Google's Rust SDK means building the macOS binaries on a macOS
runner. Other cloud SDKs may bring the same dependency; PGMI-834 should check
`azure_identity` and the AWS SDK the same way.

## Decision

- **Own dialer, about 500 lines:** the two Admin API calls (`connectSettings`,
  `generateEphemeralCert`) over pgmi's own HTTPS client (rustls with ring), an
  RSA 2048 key from the pure-Rust `rsa` crate, TLS 1.3 to the instance with the
  server CA and the ephemeral client certificate, the IAM login token as the
  password, handed to the connection layer through `connect_raw` as spike A
  does. Portable to every target, and its Admin API base URL can be overridden
  for a black-box fake.
- **Credentials from `google-cloud-auth`:** Application Default Credentials in
  full (service account keys, gcloud user credentials, the metadata server,
  impersonation, workload identity federation) stay Google's code, which is
  where the breadth and the security fixes are.
- **Release consequence for PGMI-834:** build the two macOS targets natively on
  macOS runners and give GoReleaser the binaries as prebuilt artefacts; the other
  four keep the single Linux cross-build. The alternative, writing our own ADC
  subset to stay Zig-only, is rejected: workload identity federation alone is
  more code than the dialer.
- **Tests:** a fake Admin API and TLS server in the black-box runner (the
  `fakes` server kind) once the dialer exists; the real path stays untested by
  decision.
