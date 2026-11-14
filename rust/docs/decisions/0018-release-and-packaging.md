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
- Gates added: `cargo deny check` (advisories, licences, bans of
  record 0021), the packaging check of record 0010, the
  black-box suite against the built binary.
- **After GoReleaser**, in the same workflow: `cargo publish` for
  `pgmi-shared`, then `pgmi`; then PyPI,
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

## Changed by PGMI-855 (pure Rust, record 0021)

zig is a C compiler, so the zig cross-builds go:

- Linux musl x86_64 and aarch64 link with rustc's `rust-lld`
  (`CARGO_TARGET_<T>_LINKER=rust-lld`), proven static on 2026-11-10 in a
  container with gcc, cc and ld removed. That works on the macOS runner
  too.
- Darwin links with Apple's linker on the macOS runner, as before.
- Windows: `windows-gnu` needs mingw's dlltool (a C toolchain), so it goes.
  Whether Windows links on a Windows runner with the MSVC linker, or with
  `rust-lld` and `windows-gnullvm` (its dependencies compile without C; the
  final link is unproven), waits for the owner's answer on platform linkers
  (record 0021). A Windows runner would mean a second build job feeding the
  GoReleaser run, which the OSS edition cannot merge; to be worked out with
  that answer.

## Consequences

- One release job on a macOS runner costs more runner minutes than Linux.
- npm platform packages are unscoped, `pgmi-cli-<os>-<arch>` (six names,
  reserved in PGMI-850); a scope would need an npm organisation.

## Serves

templates, version, ai, completion, help (`version` output); Template
deployments (the shipped binary embeds `shared/` complete).

## As built (PGMI-925, 2026-11-12): native runners, no GoReleaser for Rust

Record 0021 put each target on its own runner and linker (Apple's on macOS,
MSVC on Windows, `rust-lld` for static Linux). GoReleaser OSS cannot take
binaries built elsewhere, and no single runner links all six, so option 2
above does not hold. Without GoReleaser Pro (owner, 2026-11-10), the Rust
release is a plain workflow, `.github/workflows/release-rust.yml`:

1. `verify`: tag equals the Cargo version (`rust/release/check-version.sh`),
   commit on main and release notes (publish only), fmt, clippy (also with
   `testhooks`), `cargo deny`, the no-C check, the packaging check, and the
   whole test suite against PostgreSQL 18 and 17.
2. `build`: one job per target on its native runner, `PGMI_COMMIT` and
   `PGMI_BUILD_DATE` set, build paths remapped (the job fails if the binary
   still holds one), `--version` checked where the runner can run it.
   Six targets: Linux musl and macOS for x86_64 and aarch64, Windows MSVC
   for x86_64 and aarch64 (aarch64 Windows is new to the matrix and not yet
   proven on a runner).
3. `blackbox`: every case but those `rust/blackbox-skip.txt` names, each
   with its reason, against the static Linux binary that ships (PGMI-984).
4. `package`: `rust/release/package.sh` writes `pgmi_<version>_<os>_<arch>`
   archives (tar.gz, zip for Windows) with the Go release's names and
   layout, reproducibly (fixed mtimes and owners), and `checksums.txt`;
   nfpm (pinned image) builds the two `.deb` files from
   `rust/release/nfpm.yaml`, one is installed in Debian and run;
   `rust/release/cask.sh` writes the Homebrew cask.
5. `publish` (only with `publish: true`): GitHub release with the notes
   from `RELEASES.md`, attestation of `checksums.txt`, cask pushed to the
   tap for stable versions.

Until switch day the workflow runs only by hand; the v* tag still triggers
the Go release. Switch day (PGMI-835) moves the tag trigger here and drops
`release.yml`, `snapshot.yml` and `.goreleaser.yaml`. Lost against
GoReleaser: the "Quick Install" header and the commit-log section of the
release page (the notes come from `RELEASES.md` only). crates.io, PyPI and
npm publishing stay with PGMI-850.
