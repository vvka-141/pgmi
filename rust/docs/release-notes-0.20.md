# 0.20 release notes: material collected before switch day

Switch day (PGMI-835) moves this into `RELEASES.md` and the README, then
deletes this file. Until then the README describes the Go binary, so the
Rust-only requirements live here.

## Requirements (README "Requirements" line)

- **CPU.** pgmi uses the graviola crypto library for TLS and cloud sign-in.
  It needs, on x86-64: AES-NI, PCLMULQDQ, SSSE3, AVX, AVX2, BMI1, BMI2 and
  ADX (Intel Broadwell, AMD Zen and later, about 2014 on); on arm64: NEON,
  AES, PMULL and SHA2 (Apple silicon, AWS Graviton, Raspberry Pi 5; not
  Raspberry Pi 4). On a CPU without them, a connection that uses TLS and
  any cloud sign-in stop with exit 10 and name the missing features;
  commands that need no encryption run as before.

## Behaviour changes

The full list, with old and new exit codes, is the section "Behaviour
changes in 0.20" of `docs/design/behaviour-contract.md`.
