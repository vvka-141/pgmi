package db

import (
	"cmp"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
)

var passfileWarning sync.Once

// IgnoreExposedPassfile drops a password pgx took from a password file other
// users can read, as libpq does: such a file is ignored, with a warning, and
// the connection proceeds as if it held no entry. ownPassword is the one pgmi
// was given; a password from PGPASSWORD is kept. passfile is the connection's
// passfile parameter; the file checked is the one pgx read, in libpq's order:
// passfile, then PGPASSFILE, then ~/.pgpass. Windows makes no such check.
func IgnoreExposedPassfile(cfg *pgconn.Config, ownPassword, passfile string, warn func(format string, args ...any)) {
	if runtime.GOOS == "windows" || cfg.Password == "" || ownPassword != "" || os.Getenv("PGPASSWORD") != "" {
		return
	}
	path := cmp.Or(passfile, os.Getenv("PGPASSFILE"))
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		path = filepath.Join(home, ".pgpass")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 == 0 {
		return
	}
	cfg.Password = ""
	passfileWarning.Do(func() {
		warn("WARNING: password file %q has group or world access; permissions should be u=rw (0600) or less", path)
	})
}
