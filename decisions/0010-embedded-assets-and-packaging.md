# 0010: Embedding `shared/` and crates.io packaging

Status: accepted, 2026-11-10 (owner delegated, see PGMI-829). Ticket: PGMI-829.
Checked on crates.io, 2026-11-10: rust-embed 8.13.0 (2026-10-07),
include_dir 0.7.4 (last release 2024-06-17). `shared/` holds 352 files,
4.4 MB, including dotfiles (`.pgmiignore`, `.gitignore`, `.agents/`) and the
gateway manifests renamed `Cargo.toml.scaffold` and `go.mod.scaffold`.

## Context

Both binaries embed `shared/` unchanged: the session SQL, the contract SQL,
the `--overwrite` manager SQL, templates, skills, the AI overview and the
art. `cargo package` silently leaves out what it is not told to include, and
spike D found it otherwise takes every file in the folder. A crate on
crates.io can only contain files under its own directory.

## Options

1. **rust-embed.** A derive macro. In debug builds it reads files from disk
   unless `debug-embed` is on, so a debug binary and a release binary can
   differ; which hidden files it takes is implicit.
2. **include_dir.** A macro over a directory; no release in over two years.
3. **An own `build.rs`** in the shared crate that walks its directory and
   writes a sorted table of `(path, include_bytes!(...))`. About 40 lines,
   same output in every profile, every rule visible.

## Decision

Option 3, in a crate of its own, `pgmi-shared`, with `shared/Cargo.toml`.

- `build.rs` walks `ai/`, `art/`, `contract/`, `manager/`, `session/`,
  `skills/` and `templates/`, sorts by path (byte order), and
  emits `pub static FILES: &[(&str, &[u8])]` with `rerun-if-changed` per
  folder. `src/lib.rs` adds lookups (`file`, `dir`) and nothing else.
- `include = ["Cargo.toml", "build.rs", "src/**", "ai/**", "art/**",
  "contract/**", "manager/**", "session/**", "skills/**", "templates/**"]`.
  Licence `MPL-2.0 AND MIT` (`shared/templates/LICENSE` is MIT).
- **The check**, in CI and in the release gate: three lists of the seven
  folders must be equal: `git ls-files`, `cargo package -p pgmi-shared
  --list` (`rust/check-package.sh`), and the embedded `FILES` paths (a unit
  test in `pgmi-shared`). Template `*.go` files are template content and
  ship; only `shared.go` and `shared_test.go` at the folder's root stay out,
  since no folder holds them. The Go test that forbids a `Cargo.toml` under
  `shared/` exempts the crate's own manifest at the root. A missing dotfile, a stray
  untracked file or a nested `.gitignore` swallowing files fails it.
- `pgmi` depends on `pgmi-shared = "=<same version>"`; both are published
  together, shared first (record 0018). `pgmi`'s own manifest has an
  `include` list too (`src/**`, `build.rs`, `Cargo.toml`, `README.md`,
  `LICENSE`), checked the same way.
- `init` writes `<name>.scaffold` as `<name>`; nothing is renamed inside
  `shared/`.

## Consequences

- A developer's untracked file in `shared/` ends up in their local build,
  never in a release (the check runs on a clean checkout).
- Adding a new top-level folder to `shared/` means adding it to the walk list
  and to `include`; the check fails until both are done.

## Serves

Session API; Template deployments; init; templates, version, ai, completion,
help; Target database, overwrite and approval (manager SQL); Test macro
(the SQL that generates test runs).
