# 0027: The repository's native Rust layout

Status: Agent decision; the owner may revisit (owner, 2026-11-12).
Ticket: PGMI-835. Builds on 0001 (crate layout), 0010 (embedded assets),
0018 (release), 0022 (library layout). Supersedes the folder list in 0001.
Checked on b83c1a2f, 2026-11-17. 0.20.0 then shipped through
`release-rust.yml` before any move, so that workflow keeps its name.

## Context

The Go implementation is gone. The layout still shows the side-by-side
years: the binary sits in `rust/pgmi`, release scripts in `rust/release`,
records in `rust/docs`, the content crate in a folder named `shared/`
(shared by two binaries that are now one), and workflows named
`release-rust.yml` and `rust-build.yml`. A first-time reader should see a
Rust project that was always a Rust project.

What the layout must hold: a Cargo workspace for one CLI that will be
published on crates.io; the embedded content crate `pgmi-shared` (SQL,
templates, skills, and the template gateways in Go, C#, Python, Rust and
TypeScript, which ship in the scaffold); the black-box suite (Python); the
gateway e2e harness; future PyPI and npm wrappers (PGMI-850); the docs site;
examples; fuzz targets; release scripts; design records; CI.

Fixed points:

- `scripts/install.sh` and `scripts/install.ps1` are fetched by URL from
  `main` (docs/QUICKSTART.md, docs/CICD.md). They never move.
- `website/hugo.toml` mounts `../docs`. `docs/` never moves.
- The black-box harness finds the repo root as `parents[2]` of
  `blackbox/pgmi_blackbox/`, and the crate's tests reach it as
  `CARGO_MANIFEST_DIR/../../blackbox`. `blackbox/` stays at the root and the
  binary crate stays two levels deep.
- `cargo package -p pgmi-shared` must list every embedded file. Its
  `include=` patterns are relative to the package, so they survive a move
  of the whole folder unchanged.
- The release workflow is dispatched on `main`, and its attestation names
  the workflow file. Docs and install scripts quote that path.

## What well-known Rust CLIs do

- **uv, ruff:** virtual workspace at the root, every crate in
  `crates/<crate-name>/`, `fuzz/` at the root (ruff), `scripts/` for
  tooling, `docs/` for the user site, `ci.yml` and `release.yml`.
- **ripgrep:** root package plus `crates/` for libraries, `fuzz/` at the
  root, `ci/` for scripts, `pkg/` for packaging, `ci.yml` and `release.yml`.
- **rust-lang/rust:** `RELEASES.md` for release notes.

## Decision

Follow uv and ruff: a virtual workspace with every crate in
`crates/<crate-name>/`.

```
Cargo.toml  Cargo.lock        virtual workspace, members = ["crates/*"]
rust-toolchain.toml rustfmt.toml clippy.toml deny.toml   tool config, where the tools look
Makefile                      the few commands contributors and CI share
README.md CONTRIBUTING.md RELEASES.md SECURITY.md SUPPORT.md CODE_OF_CONDUCT.md LICENSE
crates/
  pgmi/                       the CLI and its library target; published as `pgmi`
  pgmi-shared/                everything the binary embeds; published as `pgmi-shared`
fuzz/                         cargo-fuzz targets for the pgmi library
blackbox/                     the executable contract: the binary from outside, Python and YAML
tests/gateways/               e2e harness for the template gateways against a live database
examples/                     small runnable pgmi projects that CI deploys
docs/                         user documentation; the site mounts it
website/                      the Hugo site that publishes docs/ and the articles
decisions/                    design records and the evidence behind them
packaging/                    turns built binaries into archives, .deb, the cask, notices;
                              later pypi/ and npm/ (PGMI-850)
scripts/                      install scripts and repo checks
.github/                      workflows: ci, build, release, docs, examples,
                              gateway-audit, verify-install
```

Why each top-level folder:

- `crates/`: one place for all Rust packages, named after the crate, as in
  uv and ruff. A reader finds the code without knowing the history.
- `crates/pgmi-shared/`: the folder takes the crate's name. "shared" meant
  shared by the Go and Rust binaries; with one binary it explains nothing.
  The crate name `pgmi-shared` stays (owner's name).
- `fuzz/`: cargo-fuzz runs from the root this way, as in ruff and ripgrep.
- `blackbox/`: the contract every build must keep; not Rust code, so not in
  `crates/`.
- `tests/gateways/`: tests the template gateways, not the binary, so it is
  not black-box. The root is a virtual manifest, so cargo never reads
  `tests/`.
- `examples/`, `docs/`, `website/`: unchanged.
- `decisions/`: contributor history, full of Go comparisons. Under `docs/`
  it would be published and fail the public-docs check.
- `packaging/`: what ripgrep calls `pkg/`. pgmi builds wheels and npm
  packages from the release binaries (record 0018), not with maturin from
  source, so there is no root `pyproject.toml` and no `python/` folder as in
  ruff and uv; the wrappers live here.
- `scripts/`: as in uv. Repo checks join the install scripts.

Choices inside that:

- **Makefile, not justfile.** uv, ruff and ripgrep have neither, but CI and
  CONTRIBUTING already call make, and make needs no install.
- **RELEASES.md stays.** uv, ruff and ripgrep use CHANGELOG.md; Rust itself
  uses RELEASES.md, and two tests and `scripts/release-notes.sh` read it.
- **`about.toml` and `about.hbs` go to `packaging/`.** Only the license step
  of a release reads them; `licenses.sh` passes the config path.
- **Workflows.** `ci.yml` keeps its name. `rust-build.yml` becomes
  `build.yml`. `release-rust.yml` keeps its name: the attestations of
  0.20.0-rc.1 and 0.20.0 name it, and the documented `gh attestation verify
  --signer-workflow` command quotes it, so a rename would make every verify
  command know two names.
- **Spikes are deleted.** `rust/spikes/connection` answered its question;
  `spike-a-connection.md` holds the verdict. Tag the commit
  `archive/spike-connection`, then `git rm`.
- **Switch-day staging is consumed, not moved.** `rust/docs/switch-day/`
  and `rust/docs/release-notes-0.20.md` are applied to `docs/`, `README.md`
  and `RELEASES.md` by PGMI-835's content work and deleted before the moves.
- **What stays Go on purpose.** The template gateways and their audit;
  `website/go.mod`, which Hugo modules require; and the differential CI job
  with `.github/scripts/go-reference.sh` and
  `blackbox/differential-allow.yaml`, which compare against the released Go
  0.14.1 binary. Retiring that comparison is a separate owner decision.

## Moves

Each group is one commit with `git mv` (or `git rm`) only, so rename
detection and `git log --follow` keep history. The next commit makes the
content edits listed under it. Push each pair together: the move commit
alone does not build. After each pair: `make check`, the black-box suite,
`scripts/check-package.sh`, the docs check, and a dispatch of `build.yml`.

**1. Crates and fuzz**

```
git mv rust/pgmi/fuzz fuzz
git mv rust/pgmi crates/pgmi
git mv shared crates/pgmi-shared
```

Then edit:

- `Cargo.toml`: `members = ["crates/*"]`; `exclude` to
  `crates/pgmi-shared/templates/advanced/gateways/rust` and `fuzz`;
  `pgmi-shared = { path = "crates/pgmi-shared", ... }`.
- `fuzz/Cargo.toml`: `pgmi = { path = "../crates/pgmi", ... }`.
- `crates/pgmi-shared/Cargo.toml`: nothing; `include=` is package-relative.
- `rust/check-package.sh`: `cd crates/pgmi-shared`.
- `rust/release/licenses.sh`: `-m crates/pgmi/Cargo.toml`.
- Workflows: `ci.yml` (fuzz manifest path, working directory, artifact
  path), `rust-build.yml` (paths filter), `release-rust.yml` (cyclonedx
  manifest and the SBOM `mv`), `docs.yml` (`shared/ai/**`),
  `gateway-audit.yml` (`GATEWAYS`).
- Scripts: `scripts/sync-ai-content.sh` and `.ps1`, `scripts/check-docs-rust.sh`
  (its file list names `shared/ai`).
- Content tests: path labels in `crates/pgmi/src/content_tests/docs.rs` and
  `templates.rs` (`"shared/..."`, `format!("shared/{path}")`). The
  `CARGO_MANIFEST_DIR/../..` roots need no edit: the depth is the same.
- Comments naming `rust/pgmi/...`: `crates/pgmi/src/invariants.rs`,
  `crates/pgmi-shared/contract/api-v1.sql`, four template test files under
  `crates/pgmi-shared/templates/advanced/lib/__test__/`.
- `LICENSE` (the MIT scope names `shared/templates/`), `README.md` (license
  line), `.gitignore` (comment), `CONTRIBUTING.md`, `blackbox/COVERAGE.md`,
  `docs/CONFIGURATION.md`, `docs/advanced/TRANSACTION-POLICY.md`,
  `docs/design/behaviour-contract.md` and every other `shared/` link in
  `docs/` and `website/content/`.
- Seven published links point at `blob/main/shared/...`. The in-repo ones
  are edited; copies of articles already posted elsewhere break. Accepted:
  the alternative is keeping a Go-era folder name forever.
- Overlay (pgmi-dev): `CLAUDE.md` and the contributor skills.

**2. Packaging and checks**

```
git mv rust/release packaging
git mv about.toml about.hbs packaging/
git mv rust/check-no-c.sh rust/check-package.sh scripts/
git mv rust/blackbox-skip.txt blackbox/skip.txt
git mv scripts/check-docs-rust.sh scripts/check-docs.sh
git mv scripts/check-docs-rust.allow scripts/check-docs.allow
```

Then edit:

- `packaging/package.sh`, `licenses.sh`, `check-version.sh`: the root is
  now `$here/..`, not `$here/../..`. `licenses.sh` passes
  `-c packaging/about.toml` and `packaging/about.hbs`.
- `packaging/placeholders-test.sh`: the fake tree `$work/rust/release`
  becomes `$work/packaging`.
- `scripts/check-no-c.sh`: reads `packaging/targets.txt`.
- `scripts/check-docs.sh`: its `allow=` path.
- `ci.yml` (skip list, check-no-c, check-package, placeholders test, musl
  notice path, docs check step), `release-rust.yml` (paths filter,
  `check-version.sh`, `check-no-c.sh`, `check-package.sh`, skip list,
  `licenses.sh`, `package.sh`, `nfpm.yaml`, `cask.sh`), `Makefile` (deny
  target, skip list), `CONTRIBUTING.md`, `blackbox/COVERAGE.md`.

**3. Workflows**

```
git mv .github/workflows/rust-build.yml .github/workflows/build.yml
```

Then edit:

- `build.yml`: its own path in `paths:`; `name: Build`.
- `ci.yml` job names: "Rust" becomes "Build and lint", "Fuzz (Rust)"
  becomes "Fuzz", "Docs describe the Rust binary" becomes "Docs".

**4. Decision records**

```
git mv rust/docs/decisions decisions
git mv rust/docs/rust-vs-go.md rust/docs/test-parity-shared.md rust/docs/test-parity-units.md decisions/
```

Then edit:

- Relative links in the records: `../../../blackbox/...` becomes
  `../blackbox/...`; links to `rust/pgmi` code become `crates/pgmi`;
  record 0022's links to the parity files. Record prose that describes
  the layout of its day stays as history.
- Links into the records: `CONTRIBUTING.md`, `blackbox/COVERAGE.md`,
  `docs/design/behaviour-contract.md`.
- `decisions/README.md`: add this record; drop "Rust re-implementation"
  from its title.

**5. Spikes**

```
git tag archive/spike-connection
git rm -r rust/spikes
```

Then edit `Cargo.toml` (`exclude` loses `rust/spikes`) and the records that
cite the spike's code (0005, 0006, 0018) to name the tag. `rust/` is now
gone.

## Consequences

- The root reads as a Rust CLI: `Cargo.toml`, `crates/`, `fuzz/`,
  `docs/`, `scripts/`, `ci.yml`, `release.yml`.
- `crates/pgmi` is publishable as is; its README for crates.io is a
  content change for the publishing ticket, not part of these moves.
- PGMI-850 adds `packaging/pypi/` and `packaging/npm/`; nothing else moves.
- Template paths in docs get longer
  (`crates/pgmi-shared/templates/...`).
