# 0021: Pure Rust, no C or C++ in the build

Status: accepted, 2026-11-10; the crypto provider and the platform linkers
decided by the owner on 2026-11-11 (below). Ticket: PGMI-855 (owner
requirement). Checked on crates.io and by building, 2026-11-10.

## Context

The owner requires a binary built from Rust only: no dependency may compile
or link C or C++ (OpenSSL, libpq, aws-lc-sys, ring's C and assembly parts if
avoidable), one self-contained executable, static on Linux.

## Audit

A scratch crate with every dependency the records name (tokio,
tokio-postgres, rustls, reqwest 0.13, azure_identity 1.0, aws-config 1.13,
aws-sigv4, google-cloud-auth 1.18, rsa, x509-cert, the RustCrypto hashes and
ciphers, clap, ratatui, crossterm, serde-saphyr, yaml-edit, roxmltree, jiff)
was resolved with default features first. C entered by one route only:
`aws-lc-sys` (C, built with cmake), pulled in by

- google-cloud-auth's default features (`default-idtoken-backend`,
  `default-rustls-provider`);
- aws-config's default HTTPS client (aws-smithy-http-client with rustls on
  aws-lc-rs);
- azure_core's default `reqwest_rustls`, which turns on reqwest's `rustls`
  feature and with it rustls' aws-lc-rs provider.

openssl-sys, bindgen and libpq never appeared. ring appeared only through
record 0006's own choice.

With the features below, `cargo check` succeeds with `CC`, `CXX` and `AR`
set to `/bin/false` for x86_64 and aarch64 Linux musl, x86_64 Windows MSVC
and aarch64 macOS: no build script compiles C. Static musl binaries for
x86_64 and aarch64 link with rustc's own `rust-lld` in a container with gcc,
cc and ld removed.

## Decision

- **Features that keep C out:**
  - `rustls` with `default-features = false` (`std`, `tls12`, `logging`),
    no provider feature;
  - `reqwest` with `rustls-no-provider`;
  - `azure_core` and `azure_identity` with `default-features = false`,
    features `reqwest` and `tokio`;
  - `google-cloud-auth` with `default-features = false`;
  - `aws-config` with `default-features = false` (`rt-tokio`,
    `credentials-process`, `sso`) and pgmi's own `HttpClient` for it, built
    on the shared reqwest client (aws-smithy-runtime-api's public trait,
    about 80 lines). aws-sigv4 with `sign-http` only.
- **One crypto provider for the whole process**, installed first thing in
  `main` and passed explicitly to every rustls config. It is chosen in one
  function, so changing it is one line.
- **The CI gate** (in the `rust` job of PGMI-831):
  1. `cargo deny check bans` denies `cc`, `cmake`, `bindgen`, `pkg-config`,
     `vcpkg`, `openssl-sys`, `openssl`, `native-tls`, `aws-lc-sys`,
     `aws-lc-rs`, `ring` and `libz-sys`;
  2. `cargo check` for every release target with `CC`, `CXX` and `AR` set to
     `/bin/false`, so a build script that compiles C fails even if its crate
     is not on the list;
  3. the Linux musl release builds link with `rust-lld`, in a job without a
     C compiler.
- **Allowed:** crates that only declare bindings to the operating system and
  compile no C: `libc`, `windows-sys`/`windows-link`, and on macOS
  `security-framework`/`core-foundation` (reqwest's platform verifier reads
  the system trust store through them). Go's binary uses the same operating
  system interfaces.

## Owner decisions (2026-11-11, PGMI-855)

1. **Crypto provider: graviola** (0.4.5 via rustls-graviola 0.4.0, by the
   rustls author). Rust plus inline assembly taken from s2n-bignum
   (formally verified), built by rustc alone; assembly written inside Rust
   is acceptable, a C compiler or C/C++ source is not.
   - rustls-rustcrypto is rejected: its README says "do not use this in
     production".
   - ring (C and assembly) stays excluded.
   - **CPU requirement.** x86-64: `aes`, `pclmulqdq`, `ssse3`, `avx`,
     `avx2`, `bmi1`, `bmi2`, `adx` (Intel Broadwell, AMD Zen and later,
     about 2014 on). arm64: `neon`, `aes`, `pmull`, `sha2` (Apple silicon,
     Graviton, Raspberry Pi 5; not Raspberry Pi 4). graviola itself panics
     on a CPU without them, so pgmi checks first (`tls::require_cpu`)
     before any TLS connection and any cloud sign-in, and refuses with
     exit 10 naming the missing features. Work that needs no cryptography
     (`init`, `templates`, `metadata`, a deploy with `sslmode=disable`)
     runs on any CPU.
   - **Fallback:** if graviola goes stale, switching provider is one line
     in `tls::provider()` and `tls::install()` (rustls-rustcrypto once it is
     production-ready, or another pure-Rust provider); the CPU check then
     goes with it.
2. **Platform linkers accepted.** Linux needs none (rust-lld). macOS links
   with Apple's linker on a macOS runner, Windows with the MSVC linker on a
   Windows runner. They only link Rust output; no C/C++ source is
   compiled. The CI gate still fails if any crate compiles C/C++. Record
   0018's zig cross-builds are dropped: zig is a C compiler.

## Consequences

- Records 0006, 0007, 0008, 0009 and 0018 follow this record.
- A new dependency that brings C fails CI on the first push.
- The fuzz targets in `rust/pgmi/fuzz` link libFuzzer, which is C++. They
  sit outside the workspace, run in the CI job `fuzz` (PGMI-861) and are
  linted in the job `Rust` (PGMI-870), and never reach a release binary; the
  gates above check the workspace.
- The rsa crate (record 0009) only generates the Cloud SQL key; signing
  with it during the TLS handshake is the provider's job. The rsa crate's
  timing advisory (RUSTSEC-2023-0071) concerns its own private-key
  operations, which pgmi does not call.

## Serves

Distribution (one static file); TLS; Cloud sign-in.
