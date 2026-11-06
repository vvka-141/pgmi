package manager

// DatabaseSettings are the CREATE DATABASE properties that must survive a
// drop-and-recreate.
//
// --overwrite is documented as "drop and recreate the target database", and a
// bare CREATE DATABASE inherits the server defaults — so overwriting a
// LATIN1/C database silently produced a UTF8/en_US.utf8 one. Encoding changes
// how bytes are stored; collation changes index ordering and every comparison,
// which is why pgmi's own plan view pins COLLATE "C". Neither is something the
// user asked to change by recreating a database.
type DatabaseSettings struct {
	Encoding string // pg_encoding_to_char(pg_database.encoding)
	Collate  string // pg_database.datcollate
	CType    string // pg_database.datctype

	// LocaleProvider is pg_database.datlocprovider: "c" libc, "i" icu,
	// "b" builtin. Locale and ICURules are the provider's locale and ICU rules.
	LocaleProvider string
	Locale         string
	ICURules       string

	// Owner is always restorable: DROP DATABASE already requires ownership or
	// superuser, so whoever got far enough to drop it can name the owner again.
	Owner string

	// ConnectionLimit is pg_database.datconnlimit; -1 means unlimited.
	ConnectionLimit int

	// Options are the database-level GUCs from ALTER DATABASE ... SET, as
	// "name=value". Losing a statement_timeout set here removes a safeguard
	// without saying so.
	Options []string

	// Comment is shobj_description, absent when the database has none.
	Comment *string

	// CustomACL is set when pg_database.datacl is non-NULL, i.e. someone ran
	// GRANT or REVOKE on the database. Grants then lists every non-owner entry;
	// recreating revokes PUBLIC's defaults and re-grants exactly these.
	CustomACL bool
	Grants    []DatabaseGrant

	// RoleOptions are the ALTER ROLE ... IN DATABASE ... SET settings.
	RoleOptions []DatabaseRoleOptions

	// PreserveLocale is set when the encoding or locale differs from the server
	// default, so recreating has to name them — which in turn forces TEMPLATE
	// template0. When false a plain CREATE DATABASE reproduces the database and
	// keeps inheriting whatever a site installed in template1, so naming them
	// anyway would quietly discard it.
	PreserveLocale bool
}

// DatabaseRoleOptions are one role's settings in one database, as "name=value".
type DatabaseRoleOptions struct {
	Role    string
	Options []string
}

// DatabaseGrant is one entry of a database ACL. Grantee "" is PUBLIC.
type DatabaseGrant struct {
	Grantee   string
	Privilege string
	Grantable bool
}
