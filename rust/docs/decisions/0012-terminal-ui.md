# 0012: Terminal UI and terminal detection

Status: accepted, 2026-11-10 (owner delegated, see PGMI-829). Ticket: PGMI-829.
Checked on crates.io, 2026-11-10: ratatui 0.30.2 (2026-06-19, rust-version
1.88), crossterm 0.29.0 (2025-04-05; ratatui 0.30 builds on it), inquire
0.9.4 (2026-02-24), dialoguer 0.12.0 (2025-08-23), shell-words 1.1.1.

## Context

Three interactive pieces: the `init` wizard (choose a template with arrow
keys, then "connection now?"), the `config` wizard (a form: Tab between host,
port, database and password fields, Ctrl-U clears a field, a live connection
test, then "Save password to .pgpass"), and the `--overwrite` confirmation
(type the database name), with the `--force` danger banner and a countdown
that rewrites one line. The `terminal/*` cases type exactly these keys.
Interactivity also depends on `CI`, `PGMI_NON_INTERACTIVE`, `NO_COLOR`,
`TERM=dumb`, `PGMI_NO_BANNER` and the pager variables.

## Options

1. **ratatui + crossterm** with a hand-written update loop. Any layout,
   including the tabbed form; the update function can be tested without a
   terminal, as the Go wizards were.
2. **inquire** or **dialoguer**: one question at a time. Far less code, but
   no form with Tab between fields, so the `config-wizard` case (and the
   UX it pins) would change.

## Decision

Option 1 for the two wizards; plain line I/O for the rest.

- Each wizard is a state machine: `update(state, event) -> (state, effect)`
  plus a `view(state)`. Unit tests feed key events. ratatui draws in an
  inline viewport (not the alternate screen), so the final lines
  (`Wrote ... (basic template)`) stay in the scrollback and on stdout.
  Keys arrive through crossterm's `EventStream` inside the tokio runtime;
  the connection test runs as a task whose result is an event.
- The overwrite confirmation is a prompt and one line read from stdin. The
  countdown rewrites its line with `\r`. The banner is `shared/art/` text.
- `.pgpass` is written with mode 0600, `:` and `\` escaped.
- **Detection**, read from the environment snapshot (record 0016):
  - interactive = stdin and stdout are terminals (`std::io::IsTerminal`),
    `CI` empty or unset, `PGMI_NON_INTERACTIVE` not `1`;
  - colour = stderr is a terminal, `TERM` is not `dumb`, `NO_COLOR` empty or
    unset; without colour the summary uses `ok`/`FAILED`, no glyphs;
  - banner = interactive and `PGMI_NO_BANNER` empty or unset.
- **Pager** (`pgmi ai`, `ai contract`): only when stdout is a terminal;
  `PGMI_PAGER`, else `PAGER`; the value is split with shell-words and run
  directly (no shell), the text written to its stdin; `cat` prints directly.
  Neither set: print directly.

## Consequences

- The wizards are the largest UI code (the Go ones were about 1,100 lines
  plus tests); the cost is accepted to keep the cases as they are.
- Answered from Go (PGMI-830): the pager command is split on white space
  and run without a shell (no quoting, no `$` expansion); with neither
  variable set it is `less -R`; a pager that cannot start means the output
  is printed directly (`terminal/pager-command`).

## First build (PGMI-833)

- The init wizard draws on the alternate screen, as Go's does, not in an
  inline viewport: ratatui's inline viewport and `Terminal::clear` ask the
  terminal for the cursor position, and a terminal that never answers
  (the black-box pty) stalls them. A stage change clears the screen with
  crossterm and starts a fresh `Terminal`, so the next screen is written
  whole. The `Wrote ...` line and the tree are printed after the wizard
  leaves the alternate screen.
- A pty may deliver Enter as `\n` plus `\r`; Ctrl-J and other control
  characters are ignored by the select lists.
- The config wizard (`tui/connection.rs`) follows Go's screens: provider,
  authentication method, a form per method, and the connection test against
  the maintenance database (token methods are not tested, as in Go). The
  test runs on a thread with its own current-thread runtime and reports
  through a channel; the loop polls keys every 100 ms to turn the spinner.
  Each page change (stage, or the test finishing) clears the screen and
  starts a fresh `Terminal`, so a line such as `Connected to ...` is written
  whole, never as a diff against the previous page.
- Form fields edit with a cursor: Left/Right/Home/End, Backspace/Delete,
  Ctrl-U clears before the cursor, Ctrl-K after, Ctrl-A/Ctrl-E; other control
  characters are ignored.
- The `[Y/n]` prompts (overwrite settings, unparseable file, `.pgpass`) are a
  line read from stdin outside the wizard; end of input is No.
- `init`: Yes to "Set up the connection now?" runs the same wizard after the
  template screen and before scaffolding (cancelling it cancels init); the
  settings are written into the scaffolded `pgmi.yaml` and the next steps
  name the chosen database (`terminal/init-wizard-connection`).
- `.pgpass` is replaced through a temporary file and a rename, mode 0600, the
  new lines first (libpq takes the first match), lines with the same
  host:port:db:user dropped.

## Serves

Terminal behaviour; init; config; Target database, overwrite and approval;
Notices and the summary line (colour and glyphs).
