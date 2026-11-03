# Exit 10: invalid configuration

pgmi refused the run, and the message names the cause. Except for the server
version check, every case is caught before pgmi connects, so no database was
created, dropped or changed.

## The project files

| Message names | Cause | Fix |
|---|---|---|
| a file that `contains a NUL byte` or `is not valid UTF-8` | A binary file in the project path. pgmi loads every file as text | Exclude it in `.pgmiignore`, or move it out of the project |
| a `symlink`, or a file or folder that `cannot` be read | pgmi follows no symlinks and loads every file it finds | Exclude it in `.pgmiignore`, replace the link with the file, or fix its permissions |
| a file that `exceeds` a byte limit | A file over the size cap (10 MiB by default) | Exclude it, or set `PGMI_MAX_FILE_SIZE` to a byte count |
| `deploy.sql` | It cannot be read, or is not valid UTF-8. A missing `deploy.sql` is exit 14 | Fix its permissions, or save it as UTF-8 |
| `metadata error` or `invalid PGMI metadata` | A broken pgmi-meta block | See the pgmi-metadata skill. `pgmi metadata validate .` reproduces it |
| `duplicate <pgmi-meta id>` | Two files share an id, usually a copied file | Give one of them a new UUID |
| `unsupported directory "__x__"` | A directory named with double underscores other than `__test__` or `__tests__` | Rename it |
| `.pgmiignore` | A malformed pattern, or a pattern that hides the root `deploy.sql` | Fix the pattern; after a broad one such as `*.sql`, add `!deploy.sql` |
| the project path | It does not exist, or is a file | Pass the project directory |

A Python virtual environment inside the project is the usual binary file. A
Windows `venv/` holds `.exe` and `.dll` files and fails with a NUL byte, exit
10. A Linux or macOS `venv/` holds symlinks, which pgmi refuses to read, and
fails with `refusing to read symlink`, also exit 10. Either way, add `venv/` to
`.pgmiignore`, or name the folder `.venv`: pgmi never loads a path that
starts with a dot.

## The invocation

| Cause | Fix |
|---|---|
| No database name | `-d`, the connection string, `pgmi.yaml`, or `PGDATABASE` |
| `--param` without `=` | `--param key=value` |
| A parameter key that is not a PostgreSQL identifier: it must start with a letter or underscore and hold only letters, digits and underscores | Rename the key |
| Two parameters whose keys differ only in case | Keys are stored lowercased; pass one |
| A parameter value with a NUL byte or invalid UTF-8 | Fix the value |
| `--params-file` missing or not `KEY=VALUE` lines, or `-` given twice | Fix the path or the file; read stdin once |
| A `PGMI_PARAM_*` variable set but empty (a CI secret that did not map) | Fix the secret mapping, or unset the variable |
| `pgmi.yaml` does not parse, or has an invalid value | Fix the field the message names |
| `--compat` names a version this binary does not ship | Use a supported version, or omit the flag |
| More than one of `--azure`, `--aws`, `--google`, or a cloud flag missing what it needs | Choose one method and supply its required flags |
| Invalid `--sslmode`, `--port`, `PGPORT`, or connection string | Fix the value |
| `--overwrite` aimed at the maintenance database or a template database | Choose another target with `-d` |

## After connecting

| Cause | Fix |
|---|---|
| The server is older than the oldest PostgreSQL pgmi supports; the message names both versions | Deploy to a newer server |

A version floor that a project sets for itself is checked by its own
`deploy.sql`. It raises an exception, so it is exit 13, not 10.
