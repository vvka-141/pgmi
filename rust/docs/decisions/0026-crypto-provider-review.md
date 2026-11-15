# 0026: Keep graviola, or move to aws-lc-rs or ring

Status: **deferred** (owner, 2026-11-15): 0.20 keeps graviola, with the
PGMI-1014 workaround; PGMI-1015 stays Low. Reopen this record if any of
these happens:

- graviola has another correctness bug that affects pgmi;
- graviola PR #248 (the RSA prime-order fix) is still unmerged when 1.0 is
  prepared;
- the Fable review (PGMI-892) flags the provider.

Since then (PGMI-1051, 2026-11-15): graviola also fails every signature
with RSA keys whose primes have unequal bit lengths in the same number of
words (1100 and 1140 bits), and with keys whose modulus is not a multiple
of 64 bits (1125-bit primes). pgmi now signs eight fixed messages with every
RSA key it loads and refuses the key (exit 10) when one fails, so no key
pgmi accepts fails later in a handshake. Standard 2048, 3072 and 4096-bit
keys pass. This is a workaround, not a fix; it counts toward the first
reopen condition above.

It reopened owner decision 1 of record 0021 (graviola, 2026-11-11) because
of PGMI-1013 and PGMI-1014. Measured on a throwaway worktree that was never
merged or pushed.

## Recommendation (deferred)

Move to **aws-lc-rs**, and narrow record 0021's rule from "no C anywhere" to
"no C except the crypto provider". Keep graviola until then, with the
PGMI-1014 workaround.

In plain words:

1. graviola is young. Its README says "very new, exercise due caution".
   In the three weeks before this record, outside reviewers found three
   correctness bugs in it: RSA signing fails for some keys (ours), P-256
   and P-384 keys were not range-checked, and a TLS 1.2 length check was
   missing. Two are still open. Nobody has
   published an audit of graviola, and the bugs sit in the parts that are
   not formally verified.
2. graviola refuses to run on CPUs older than about 2014, and on some
   virtual machines whose CPU hides features. To work around that, pgmi
   has its own CPU check and drops `prefer` connections to plain text
   (PGMI-979). Go 0.14 has no CPU floor, so this is a regression that
   graviola alone causes.
3. aws-lc-rs is the default provider of rustls, of the AWS SDK and of
   reqwest. It is the most used and most reviewed of the three. It runs on
   any CPU and offers a FIPS build. Its binary is about 1 MB bigger than
   graviola's and 2 MB bigger than ring's.
4. The cost is in CI, not for users. Every release job needs a C compiler
   (gcc with musl-tools on Linux, clang-cl on Windows arm64). Users still
   get one static file. About 200 lines of pgmi go away; none are added.
5. ring builds and runs about as well. But rustls moved its default away
   from ring, and ring has one maintainer (it was briefly declared
   unmaintained in 2025). It saves 1 MB, which is not a reason to prefer it.

If the owner keeps the "no C at all" rule, graviola is the only choice. It
is workable: keep the workaround in `tls::provider()`, drop it when
graviola merges its fix (PR #248), and keep the CPU check.

## Comparison

| | graviola 0.4.5 | aws-lc-rs 1.19 (aws-lc-sys 0.47) | ring 0.17.14 |
|---|---|---|---|
| Since | 2024-08, one maintainer (the rustls author) | AWS team; a fork of BoringSSL | 2015, one maintainer; derived from BoringSSL |
| Use | about 0.5 M downloads, 15 crates depend on it | the rustls, AWS SDK and reqwest default | the former rustls default; very widely used |
| Assurance | s2n-bignum assembly formally verified; no audit published; README says "very new" | FIPS 140-3 validated build available; parts formally verified (s2n-bignum, also used here) | BoringSSL heritage; no FIPS |
| Recent bugs | RSA CRT with p < q (PR #248, open); #235 P-256/P-384 accepted out-of-range points (fixed); #240 TLS 1.2 ChaCha20 length (open); #241 ML-KEM leaves AVX state dirty, a speed issue (open); #222 panics without ADX (open) | aws-lc-sys and aws-lc-fips-sys: RUSTSEC-2026-0042 to 0048 (X.509 name constraints, CRL, PKCS7, AES-CCM timing), all fixed in 0.38/0.39; none in code rustls uses (it checks certificates with webpki and offers no AES-CCM suites) | RUSTSEC-2025-0009 (AES panic with overflow checks, fixed in 0.17.12); RUSTSEC-2025-0007 ("unmaintained", withdrawn) |
| CPU | x86-64 needs AES-NI, PCLMULQDQ, SSSE3, AVX, AVX2, BMI1, BMI2 and ADX; arm64 needs NEON, AES, PMULL and SHA2; panics otherwise | any CPU, with runtime dispatch | any CPU, with runtime dispatch |
| Build needs | rustc only | C compiler on every target; no cmake, Go or bindgen (non-FIPS); NASM on Windows x86-64 unless `prebuilt-nasm`; clang-cl on Windows arm64 | C compiler on every target; no cmake; no NASM for the crates.io package; clang on Windows arm64 |
| Linux musl, as built today | builds, no C compiler | fails: `failed to find tool "x86_64-linux-musl-gcc"`; builds with `musl-tools` | the same as aws-lc-rs |
| arm64 Linux cross-build from x86-64 | builds | fails without an aarch64 musl gcc; use the native arm64 runner instead | the same as aws-lc-rs |
| Release binary, x86-64 musl | 22.5 MB | 23.4 MB | 21.3 MB |
| Clean release build, x86-64 musl, 8 jobs | 71 s | 79 s | 71 s |
| Static binary (x86-64 musl) | yes, static-pie | yes, static-pie | yes, static-pie |
| Licence | Apache-2.0 OR ISC OR MIT-0; passes `cargo deny` | ISC, Apache-2.0, MIT, BSD-3-Clause combinations; all on our allow list | Apache-2.0 AND ISC; on our allow list |
| `cargo deny` / `check-no-c.sh` | pass | the bans on `aws-lc-rs`, `aws-lc-sys` and `cc` must go; `check-no-c.sh` must allow it | the bans on `ring` and `cc` must go; the same |
| ML-KEM (post-quantum key exchange) | yes | yes | no |

The build times and sizes come from one run each, in the `pgmi-rust-dev`
image (gcc, no clang, cmake or NASM; `musl-tools` added for ring and
aws-lc-rs), release profile, all dependencies built from scratch, on a
lightly loaded machine. Treat differences under about 10 s as noise.
The Windows and macOS rows come from each project's build documentation, not
from a build.

## What a move to aws-lc-rs changes in pgmi

- `Cargo.toml`: rustls gets the `aws_lc_rs` feature; `rustls-graviola`
  goes. `tls::provider()` and `tls::install()` become
  `rustls::crypto::aws_lc_rs::default_provider()`.
- Removed: the larger-prime-first key provider (PGMI-1013, PGMI-1014); the
  CPU check (`CPU_FEATURES`, `require_cpu`, `missing_cpu`, `cpu_error`);
  the plain-text fallback for `allow` and `prefer` in `dial.rs` and its
  warning (PGMI-979); and the tests for both. Black-box cases are not
  affected: none fakes a CPU.
- Build: `deny.toml` loses three bans; `rust/check-no-c.sh` allows the
  provider's crates (or goes); the Linux release jobs install `musl-tools`,
  and arm64 Linux builds on the `ubuntu-24.04-arm` runner the release
  workflow already uses for its run test. The Windows arm64 job needs
  clang-cl. The reproducible-build job must then also prove the C part
  reproducible.
- Docs: the CPU line leaves `rust/docs/release-notes-0.20.md` and the
  install pages; records 0006, 0018 and 0021 follow.

## Not proven

- The Windows and macOS builds with aws-lc-rs or ring were not run. Only
  a CI run proves them, in particular whether the GitHub Windows arm64
  image already has clang-cl.
- Whether `musl-tools` gives byte-for-byte reproducible C objects. The
  reproducible job would show it.
- graviola's bug count is a count, not a rate. aws-lc and ring have more
  code and far more reviewers, so their advisories are not comparable one
  for one.
