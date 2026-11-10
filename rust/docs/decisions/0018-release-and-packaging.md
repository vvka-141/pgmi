# 0018: Release pipeline, Python and npm packages

Status: accepted by the owner, 2026-11-10. Ticket: PGMI-829. Builds on spikes D and B.
Checked 2026-11-10: GoReleaser v2.20.0 (2026-11-04); the repository runs the
OSS edition (`distribution: goreleaser`, v2.19.0 in `release.yml`);
goreleaser.com says the `prebuilt` builder and cargo publishing are
GoReleaser Pro only. maturin 1.16.0 (2026-10-29), cargo-deny 0.20.2. Names:
crates.io `pgmi` (owner's placeholder), PyPI `pgmi-cli` and npm `pgmi-cli`
(owner's placeholders, command `pgmi`); `pgmi` on PyPI and npm unused.

## Context

Spike D chose GoReleaser's Rust builder: one Linux job cross-builds six
targets with zig, keeping archive names, the cask, the `.deb`, the checksum
file and every gate. Spike B found that any crate using reqwest 0.13 with
rustls (google-cloud-auth, and also azure_core, record 0007) links Apple's
frameworks, which zig does not ship, so macOS must build on macOS. Spike B
proposed handing the macOS binaries to GoReleaser as prebuilt artefacts.
**That needs GoReleaser Pro**, which this repository does not use.

## Options

1. **GoReleaser Pro** with prebuilt macOS binaries from a macOS job. Works
   as spike B said; a paid licence.
2. **Run the whole GoReleaser job on a macOS runner.** Darwin targets build
   with plain `cargo` against the Xcode SDK (both architectures), the four
   others with cargo-zigbuild from the same machine. One job, OSS, nothing
   merged by hand. Not yet proven.
3. **Two GoReleaser runs** (Linux and macOS) merged by a script. Fragile:
   checksums, the cask and the release notes would be stitched together.
4. **dist** instead. Rejected by spike D.

## Decision

Option 2, proven in the snapshot workflow before the switch. No GoReleaser
Pro (owner, 2026-11-10): if option 2 fails, report to the owner; the likely
fallback is dropping the affected macOS build, not paying.

- `.goreleaser.yaml`: two builds with `builder: rust`, one for the two
  `apple-darwin` targets (`tool: cargo`), one for `x86_64`/`aarch64`
  `unknown-linux-musl`, `x86_64-pc-windows-gnu` and
  `aarch64-pc-windows-gnullvm` (`tool: cargo-zigbuild`). Archives, cask,
  `.deb`, `checksums.txt`, attestations unchanged.
- The release gate checks that the version in `rust/pgmi/Cargo.toml` equals
  the tag, rather than rewriting it. Only `-alpha.N`, `-beta.N`, `-rc.N`
  pre-releases (PEP 440 can parse them). `build.rs` takes the commit from an
  environment variable GoReleaser sets, else `unknown`.
- Gates added: `cargo deny check` (advisories, licences, bans on
  `aws-lc-rs` and `openssl-sys`), the packaging check of record 0010, the
  black-box suite against the built binary.
- **After GoReleaser**, in the same workflow: `cargo publish` for
  `pgmi-shared`, `pgmi-tokio-postgres` (when changed), `pgmi`; then PyPI,
  then npm; each followed by its Verify Install channel.
- **PyPI**: wheels assembled by a small script from the release archives,
  not rebuilt by maturin, so every channel ships the same bytes and the
  glibc problem spike D found does not arise: the static musl binary is
  tagged `manylinux2014` and `musllinux_1_2`; macOS and Windows wheels get
  their usual tags. `twine check` and an install test gate the upload.
  maturin `--zig` stays the fallback.
- **npm**: `pgmi-cli` (launcher) with one optional dependency per platform
  carrying the binary and `os`/`cpu` fields, as spike D tested. Launcher
  files outside any `bin/` folder (the root `.gitignore` ignores `bin/`).

## Consequences

- One release job on a macOS runner costs more runner minutes than Linux.
- npm platform packages are unscoped, `pgmi-cli-<os>-<arch>` (six names,
  reserved in PGMI-850); a scope would need an npm organisation.

## Serves

templates, version, ai, completion, help (`version` output); Template
deployments (the shipped binary embeds `shared/` complete).
