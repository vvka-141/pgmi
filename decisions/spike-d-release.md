# Spike D: the release toolchain for the Rust binary

Date: 2026-11-10. Ticket: PGMI-848. Verdict: **GoReleaser with its Rust builder**.

## Question

Today's release (`.goreleaser.yaml`, `.github/workflows/release.yml`) carries
gates earned over several releases and channels that scripts and checks depend
on. Replacing it could drop them silently. Which tool builds and publishes the
Rust binary while keeping them: GoReleaser's Rust builder (v2.20, built on
cargo-zigbuild) or dist (v0.34, formerly cargo-dist)?

## What was run

A small crate named `pgmi` that links rustls with the ring backend (the C and
assembly that make cross-compiling hard), branch
`feature/pgmi-848-release-spike` (archived), and a throwaway workflow on it.

| Check | Result |
|---|---|
| GoReleaser snapshot, all six targets from one Linux job (`x86_64`/`aarch64` linux-musl, `x86_64`/`aarch64` apple-darwin, `x86_64-pc-windows-gnu`, `aarch64-pc-windows-gnullvm`) | ✓ 1 min 42 s locally, the same in CI |
| Archive names | ✓ today's: `pgmi_<version>_linux_amd64.tar.gz`, `..._darwin_arm64.tar.gz`, `..._windows_amd64.zip` |
| `.deb` for amd64 and arm64 (nfpm), `checksums.txt` | ✓ |
| Binaries run on native runners: `ubuntu-latest`, `ubuntu-24.04-arm`, `macos-15`, `macos-15-intel`, `windows-latest`, `windows-11-arm` | ✓ all six |
| macOS arm64 signature | ✓ `flags=0x20002(adhoc,linker-signed)`, as Go's linker does |
| Linux binaries | ✓ static, stripped, about 0.5 MB compressed |
| PyPI: maturin `bindings = "bin"` wheel named `pgmi-cli`, `twine check`, install, run `pgmi` | ✓ |
| npm: platform package with the binary + `pgmi-cli` launcher package, `npm install` from the tarballs, `npx pgmi` and `npx pgmi-cli` | ✓ |
| `cargo package --list`, `cargo publish --dry-run` | ✓ (but see the `include` note) |

dist was run locally (`dist init`, `dist generate`, `dist plan`) for the same
six targets; its CI was not run, since it builds each target on its native
runner by construction.

## Comparison

| | GoReleaser Rust builder | dist 0.34 |
|---|---|---|
| Current workflow and gates (tag on main, lint, tests, vulnerability check, release notes, walkthrough, examples) | kept: only the build commands inside the jobs change | replaced by its generated workflow; every gate re-added through its hooks |
| Archive names (`install.sh`, `install.ps1`, Verify Install hard-code them) | unchanged | `pgmi-<target-triple>.tar.xz` |
| Homebrew | cask, unchanged (with the quarantine hook) | formula only |
| `.deb` / APT (PGMI-834) | nfpm, unchanged | none |
| Checksums | one `checksums.txt` | one `.sha256` per file |
| Attestations | the existing `actions/attest` step | built in (`actions/attest@v4`) |
| Windows | GNU toolchain (zig); the x64 zip is 1.3 MB against 0.5 MB for the others | MSVC on a Windows runner |
| Builds | one Linux job cross-builds everything | one native runner per target |
| npm | not built in; the platform-package pattern above | an installer that downloads the binary from GitHub at install time (the distribution research advises against it) |
| Maturity | Rust builder documented as limited to tested targets; all six worked here | mature for Rust |

## Decision

Keep GoReleaser and switch its build to `builder: rust`. It keeps every gate,
archive name, the cask, the `.deb` and the checksum file the install scripts and
Verify Install rely on; dist would change most of them for gains pgmi does not
need. Accept the GNU toolchain on Windows; revisit only if a Windows user hits
an MSVC-specific problem.

Add to the release workflow, outside GoReleaser: the maturin wheels and the npm
packages (PGMI-850), `cargo publish`, and Verify Install channels for each.

## Findings for PGMI-834 and PGMI-850

- **Pre-release versions must parse as PEP 440.** maturin refused
  `0.20.0-spike.1`; `0.20.0-alpha.1` became `0.20.0a1`. Use only `-alpha.N`,
  `-beta.N`, `-rc.N`.
- **Wheels built on a current runner need a new glibc** (`manylinux_2_34`).
  Build them in a manylinux2014 image or with `maturin --zig`, or ship musllinux
  wheels of the static binary.
- **`cargo package` takes every file in the folder**, here npm and PyPI files
  too. The crate needs an `include` list, checked against git as PGMI-842's test
  does for embedded files.
- **The root `.gitignore` ignores every `bin/`**, which is npm's usual layout.
  Keep launchers out of `bin/` or add a narrow negation.
- **Local `archive/*` tags confuse a local GoReleaser snapshot**: it took
  `archive/feature/...` as the current tag. CI never has those tags; for local
  runs set `git.ignore_tags` or `GORELEASER_CURRENT_TAG`.
- Publishing to all registries was not exercised (no real publish). Order to
  implement in PGMI-834: GitHub release and attestations, then crates.io, PyPI,
  npm, Homebrew, Cloudsmith, each followed by its Verify Install channel, since
  the publishes are not atomic.
