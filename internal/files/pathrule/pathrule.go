// Package pathrule holds the rules that classify a project path: SQL or not,
// test or not, and which __dunder__ directories a project may use.
package pathrule

import (
	"regexp"
	"strings"
)

// PgmiignoreFile is the gitignore-style file at the project root whose
// patterns add to the built-in exclusions.
const PgmiignoreFile = ".pgmiignore"

var dunderDirRegexp = regexp.MustCompile(`/__([^/]+)__/`)

// IsTestPath reports whether the path is inside a __test__ or __tests__ directory.
func IsTestPath(path string) bool {
	for _, m := range dunderDirRegexp.FindAllStringSubmatch(path, -1) {
		if m[1] == "test" || m[1] == "tests" {
			return true
		}
	}
	return false
}

// UnsupportedDunderDir returns the first __name__ directory in path other
// than __test__ and __tests__.
func UnsupportedDunderDir(path string) (string, bool) {
	for _, m := range dunderDirRegexp.FindAllStringSubmatch(path, -1) {
		if m[1] != "test" && m[1] != "tests" {
			return m[1], true
		}
	}
	return "", false
}

// sqlExtensions must stay consistent with pg_temp.pgmi_is_sql_file() in schema.sql.
var sqlExtensions = map[string]bool{
	".sql":     true,
	".ddl":     true,
	".dml":     true,
	".dql":     true,
	".dcl":     true,
	".psql":    true,
	".pgsql":   true,
	".plpgsql": true,
}

// IsSQLExtension reports whether the extension marks a SQL file.
func IsSQLExtension(ext string) bool {
	return sqlExtensions[strings.ToLower(ext)]
}
