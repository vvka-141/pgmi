# 0021: Pure Rust, no C or C++ in the build

Status: accepted in part, 2026-11-10. Ticket: PGMI-855 (owner requirement).
Two choices wait for the owner: the crypto provider and the platform linkers
(below). Checked on crates.io and by building, 2026-11-10.

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

## Waiting for the owner

1. **Crypto provider.** Both candidates passed every TLS black-box case the
   spike passes with ring (16 of 19; the same three known failures), with no
   C compiler:
   - **rustls-rustcrypto 0.1.0-pre.0** (RustCrypto organisation): Rust only,
     every target. Its README says "do not use this in production": not
     complete, slower than ring, not audited as a set.
   - **graviola 0.4.5 via rustls-graviola 0.4.0** (by the rustls author):
     Rust plus inline assembly taken from s2n-bignum (formally verified),
     built by rustc alone. Production-intended and fast, but only x86_64 and
     aarch64, and it needs `avx2`, `adx` and `bmi2` on x86_64 (Intel
     Broadwell, AMD Zen, 2014 on) and `aes`, `sha2`, `pmull` on aarch64.
     All five release targets are x86_64 or aarch64.
   - **ring 0.17.14**: C and assembly; an exception the requirement does not
     allow without the owner's approval.

   Until the owner chooses, the build uses graviola: no C compiler, made for
   production use. Changing provider is one line in `tls::provider()`.
2. **Platform linkers.** Linux needs none (rust-lld). macOS links with
   Apple's linker on a macOS runner (record 0018 already builds there),
   Windows with the MSVC linker on a Windows runner. Neither compiles C, but
   both come from a C toolchain. Record 0018's zig cross-builds are dropped:
   zig is a C compiler.

## Consequences

- Records 0006, 0007, 0008, 0009 and 0018 follow this record.
- A new dependency that brings C fails CI on the first push.
- The rsa crate (record 0009) only generates the Cloud SQL key; signing
  with it during the TLS handshake is the provider's job. The rsa crate's
  timing advisory (RUSTSEC-2023-0071) concerns its own private-key
  operations, which pgmi does not call.

## Serves

Distribution (one static file); TLS; Cloud sign-in.
