package cli

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/params"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// connectionFlags holds the common connection-related flag values.
type connectionFlags struct {
	connection     string
	host           string
	port           int
	username       string
	password       string // not a CLI flag; set programmatically (e.g., from wizard)
	database       string
	sslMode        string
	azure          bool
	azureTenantID  string
	azureClientID  string
	aws            bool
	awsRegion      string
	google         bool
	googleInstance string
	sslCert        string
	sslKey         string
	sslRootCert    string
}

func (f connectionFlags) granular() *db.GranularConnFlags {
	return &db.GranularConnFlags{
		Host:     f.host,
		Port:     f.port,
		Username: f.username,
		Database: f.database,
		SSLMode:  f.sslMode,
	}
}

func (f connectionFlags) certs() *db.CertFlags {
	return &db.CertFlags{SSLCert: f.sslCert, SSLKey: f.sslKey, SSLRootCert: f.sslRootCert}
}

// connectionReport is where the connection came from, and every pgmi.yaml
// value another source replaced. source is --connection, PGMI_CONNECTION_STRING
// or DATABASE_URL for a connection string; otherwise the layer that supplied
// the host: flags, PGHOST, pgmi.yaml or defaults.
type connectionReport struct {
	source    string
	overrides []db.Override
}

// announced: worth a line of its own. The user just typed flags and --connection.
func (r connectionReport) announced() bool {
	return r.source != "--connection" && r.source != "flags"
}

func (r connectionReport) describe() string {
	if r.source == "defaults" {
		return "defaults"
	}
	return "from " + r.source
}

func reportConnection(f connectionFlags, pc *config.ProjectConfig) connectionReport {
	env := db.LoadFromEnvironment()
	connStr, source := db.SelectConnectionString(f.connection, f.granular(), env)
	r := connectionReport{source: source, overrides: db.EnvOverrides(connStr, source, f.granular(), f.certs(), env, pc)}
	if source == "" {
		r.source = hostSource(f, env, pc)
	}
	return r
}

func hostSource(f connectionFlags, env *db.EnvVars, pc *config.ProjectConfig) string {
	switch {
	case f.host != "":
		return "flags"
	case env.PGHOST != "":
		return "PGHOST"
	case pc != nil && pc.Connection.Host != "":
		return "pgmi.yaml"
	}
	return "defaults"
}

// resolveConnectionFromFlags resolves connection configuration from flags and project config.
func resolveConnectionFromFlags(
	flags connectionFlags,
	projectCfg *config.ProjectConfig,
) (*db.ConnectionConfig, string, error) {
	granularFlags := flags.granular()

	azureFlags := &db.AzureFlags{
		Enabled:  flags.azure,
		TenantID: flags.azureTenantID,
		ClientID: flags.azureClientID,
	}

	awsFlags := &db.AWSFlags{
		Enabled: flags.aws,
		Region:  flags.awsRegion,
	}

	googleFlags := &db.GoogleFlags{
		Enabled:  flags.google,
		Instance: flags.googleInstance,
	}

	connConfig, maintenanceDB, err := db.ResolveConnectionParams(flags.connection, granularFlags, azureFlags, awsFlags, googleFlags, flags.certs(), db.LoadFromEnvironment(), projectCfg)
	if err != nil {
		return nil, "", err
	}

	if flags.password != "" && connConfig.Password == "" {
		connConfig.Password = flags.password
	}

	return connConfig, maintenanceDB, nil
}

// paramSources are the parameter inputs beyond pgmi.yaml. stdin is nil where
// it is not the user's to give (pgmi serve speaks MCP on it).
type paramSources struct {
	environ []string
	stdin   io.Reader
	files   []string
	pairs   []string
}

// loadMergedParameters loads and merges parameters from all sources.
// Priority (highest to lowest): --param > params files > PGMI_PARAM_* > pgmi.yaml
func loadMergedParameters(projectCfg *config.ProjectConfig, src paramSources, verbose bool) (map[string]string, error) {
	// Keys are merged lowercased: the session stores them lowercased, so
	// `Env` from pgmi.yaml and `env` from --param are one parameter and the
	// later source must win, not collide on the session table's primary key.
	parameters := make(map[string]string)
	put := func(src map[string]string) {
		for k, v := range src {
			parameters[strings.ToLower(k)] = v
		}
	}

	if projectCfg != nil {
		if err := loader.CheckCaseCollisions(projectCfg.Params); err != nil {
			return nil, fmt.Errorf("pgmi.yaml params: %w", err)
		}
		put(projectCfg.Params)
	}

	envParams, err := params.FromEnviron(src.environ)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", pgmi.ErrInvalidConfig, err)
	}
	if err := loader.CheckCaseCollisions(envParams); err != nil {
		return nil, fmt.Errorf("%s* environment variables: %w", params.EnvPrefix, err)
	}
	put(envParams)
	if verbose && len(envParams) > 0 {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Loaded %d parameter(s) from %s* environment variables\n", len(envParams), params.EnvPrefix)
	}

	if len(src.files) > 0 {
		fileParams, err := loadParamsFromFiles(filesystem.NewOSFileSystem(), src.stdin, src.files, verbose)
		if err != nil {
			return nil, err
		}
		put(fileParams)
	}

	cliParams := make(map[string]string, len(src.pairs))
	for _, pair := range src.pairs {
		kv, err := params.ParseKeyValuePairs([]string{pair})
		if err != nil {
			return nil, fmt.Errorf("%w: invalid parameter format: %w", pgmi.ErrInvalidConfig, err)
		}
		maps.Copy(cliParams, kv)
	}
	if err := loader.CheckCaseCollisions(cliParams); err != nil {
		return nil, err
	}
	put(cliParams)

	if verbose && len(src.pairs) > 0 {
		fmt.Fprintf(os.Stderr, "[VERBOSE] CLI parameters override %d value(s)\n", len(src.pairs))
	}

	return parameters, nil
}

// resolveEffectiveTimeout returns the effective timeout, preferring pgmi.yaml unless the caller set one explicitly.
func resolveEffectiveTimeout(
	explicit bool,
	projectCfg *config.ProjectConfig,
	flagTimeout time.Duration,
) (time.Duration, error) {
	if projectCfg != nil && projectCfg.Timeout != "" && !explicit {
		parsed, err := time.ParseDuration(projectCfg.Timeout)
		if err != nil {
			// ErrInvalidConfig so a bad value in pgmi.yaml exits 10, like every
			// other fault in that file — an unknown field, a port that is not a
			// number, a connection block that is a scalar. This one exited 1.
			return 0, fmt.Errorf("invalid timeout in pgmi.yaml: %w: %w", err, pgmi.ErrInvalidConfig)
		}
		return parsed, nil
	}
	return flagTimeout, nil
}

// dotenvReport names the .env that was applied and the keys it set: those not
// already in the real environment, which godotenv leaves untouched. Values are
// never kept: a .env is where PGPASSWORD lives.
type dotenvReport struct {
	path string
	keys []string
}

// fromDotenv reports whether the variable name came from the project .env.
func (d dotenvReport) fromDotenv(name string) bool {
	return slices.Contains(d.keys, name)
}

// loadDotenv applies the project's .env: sourcePath/.env, never the process
// CWD, so the resolved target and credentials match the project being deployed.
func loadDotenv(sourcePath string) dotenvReport {
	envPath := filepath.Join(sourcePath, ".env")
	report := dotenvReport{path: envPath}
	if !filepath.IsAbs(envPath) && !strings.HasPrefix(envPath, "..") {
		report.path = "./" + envPath
	}
	if values, err := godotenv.Read(envPath); err == nil {
		for key := range values {
			if _, set := os.LookupEnv(key); !set {
				report.keys = append(report.keys, key)
			}
		}
		slices.Sort(report.keys)
	}
	if err := godotenv.Load(envPath); err != nil {
		// A missing .env is normal and stays silent. A .env that exists but
		// does not parse is not: godotenv is all-or-nothing, so one stray line
		// discards the whole file and the PGPASSWORD or PGDATABASE someone put
		// there is simply absent. The deploy then fails with an unrelated
		// complaint — "database name is required" — and sends them looking for
		// a missing -d.
		//
		// This used to print only under --verbose, which is exactly when
		// nobody is looking: you reach for --verbose after the confusing
		// error, not before it.
		//
		// The underlying error is deliberately NOT included. godotenv reports
		// the text surrounding the parse failure ("... near \"<file
		// content>\""), and a .env is where PGPASSWORD lives — printing it
		// would put the credential on stderr and into CI logs. The path plus
		// the format rule is enough to act on.
		if _, statErr := os.Stat(envPath); statErr == nil {
			fmt.Fprintf(os.Stderr,
				"WARNING: %s could not be parsed, so none of its values were applied.\n"+
					"Every line must be KEY=VALUE, a # comment, or blank.\n",
				envPath)
		}
		report.keys = nil
	}
	return report
}

// loadProjectYAML loads sourcePath/pgmi.yaml without touching the process
// environment. Returns nil config if pgmi.yaml does not exist (not an error).
func checkProjectDir(sourcePath string) error {
	info, err := os.Stat(sourcePath)
	return scanner.ProjectDirError(sourcePath, info, err)
}

func loadProjectYAML(sourcePath string) (*config.ProjectConfig, error) {
	projectCfg, err := config.Load(sourcePath)
	if err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			return nil, nil // Config file not found is not an error
		}
		return nil, fmt.Errorf("%w: failed to load pgmi.yaml: %w", pgmi.ErrInvalidConfig, err)
	}
	return projectCfg, nil
}

// logConnectionVerbose logs connection details when verbose mode is enabled.
func logConnectionVerbose(connConfig *db.ConnectionConfig, maintenanceDB string, includeMaintenanceDB bool) {
	fmt.Fprintf(os.Stderr, "[VERBOSE] Connection resolved:\n")
	fmt.Fprintf(os.Stderr, "  Host: %s\n", connConfig.Host)
	fmt.Fprintf(os.Stderr, "  Port: %d\n", connConfig.Port)
	fmt.Fprintf(os.Stderr, "  User: %s\n", connConfig.Username)
	fmt.Fprintf(os.Stderr, "  Target Database: %s\n", connConfig.Database)
	if includeMaintenanceDB {
		fmt.Fprintf(os.Stderr, "  Maintenance Database: %s\n", maintenanceDB)
	}
	fmt.Fprintf(os.Stderr, "  SSL Mode: %s\n", connConfig.SSLMode)
	if connConfig.SSLCert != "" {
		fmt.Fprintf(os.Stderr, "  SSL Cert: %s\n", connConfig.SSLCert)
	}
	if connConfig.SSLKey != "" {
		fmt.Fprintf(os.Stderr, "  SSL Key: %s\n", connConfig.SSLKey)
	}
	if connConfig.SSLRootCert != "" {
		fmt.Fprintf(os.Stderr, "  SSL Root Cert: %s\n", connConfig.SSLRootCert)
	}
	fmt.Fprintf(os.Stderr, "  Auth Method: %s\n", connConfig.AuthMethod)
}

// saveConnectionToConfig saves connection config to pgmi.yaml, merging with any existing config.
func saveConnectionToConfig(sourcePath string, connConfig *db.ConnectionConfig, managementDB string) error {
	configPath := filepath.Join(sourcePath, "pgmi.yaml")

	conn := config.ConnectionConfig{
		Host:                connConfig.Host,
		Port:                connConfig.Port,
		Username:            connConfig.Username,
		Database:            connConfig.Database,
		MaintenanceDatabase: managementDB,
		SSLMode:             connConfig.SSLMode,
		SSLCert:             connConfig.SSLCert,
		SSLKey:              connConfig.SSLKey,
		SSLRootCert:         connConfig.SSLRootCert,
		AuthMethod:          authMethodToString(connConfig.AuthMethod),
		AzureTenantID:       connConfig.AzureTenantID,
		AzureClientID:       connConfig.AzureClientID,
		AWSRegion:           connConfig.AWSRegion,
		GoogleInstance:      connConfig.GoogleInstance,
	}

	var doc yaml.Node
	existing, err := os.ReadFile(configPath)
	if err != nil || yaml.Unmarshal(existing, &doc) != nil ||
		len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if err := setConnection(doc.Content[0], conn); err != nil {
		return err
	}

	var out strings.Builder
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("marshal pgmi.yaml: %w", err)
	}

	// 0600: pgmi.yaml may accrue sensitive params via user edits; treat as
	// private by default (same convention as .pgpass). Users who need a
	// checked-in config can widen the mode themselves.
	if err := os.WriteFile(configPath, []byte(out.String()), 0600); err != nil {
		return fmt.Errorf("write pgmi.yaml: %w", err)
	}
	return nil
}

// setConnection edits the connection mapping of a pgmi.yaml node tree in
// place, so every comment and key outside it survives a save: keys the new
// connection sets are updated or appended, keys it leaves empty are removed.
func setConnection(root *yaml.Node, conn config.ConnectionConfig) error {
	var want yaml.Node
	if err := want.Encode(conn); err != nil {
		return fmt.Errorf("marshal connection: %w", err)
	}
	set := map[string]*yaml.Node{}
	var order []string
	for i := 0; i+1 < len(want.Content); i += 2 {
		k, v := want.Content[i].Value, want.Content[i+1]
		if v.Value == "" || v.Value == "0" {
			continue
		}
		set[k] = v
		order = append(order, k)
	}

	var section *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "connection" {
			section = root.Content[i+1]
			break
		}
	}
	if section == nil {
		section = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append([]*yaml.Node{{Kind: yaml.ScalarNode, Value: "connection"}, section}, root.Content...)
	}
	if section.Kind != yaml.MappingNode {
		*section = yaml.Node{Kind: yaml.MappingNode, HeadComment: section.HeadComment}
	}

	kept := section.Content[:0]
	seen := map[string]bool{}
	for i := 0; i+1 < len(section.Content); i += 2 {
		k, v := section.Content[i], section.Content[i+1]
		nv, ok := set[k.Value]
		if !ok {
			continue
		}
		seen[k.Value] = true
		if v.Value != nv.Value || v.Kind != yaml.ScalarNode {
			*v = yaml.Node{Kind: yaml.ScalarNode, Tag: nv.Tag, Value: nv.Value, Style: nv.Style, HeadComment: v.HeadComment, FootComment: v.FootComment}
		}
		kept = append(kept, k, v)
	}
	for _, k := range order {
		if !seen[k] {
			kept = append(kept, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, set[k])
		}
	}
	section.Content = kept
	return nil
}

func authMethodToString(m pgmi.AuthMethod) string {
	switch m {
	case pgmi.AuthMethodAzureEntraID:
		return "azure"
	case pgmi.AuthMethodAWSIAM:
		return "aws"
	case pgmi.AuthMethodGoogleIAM:
		return "google"
	default:
		return ""
	}
}

// loadParamsFromFiles loads parameters from key=value files (--params-file) using the provided filesystem.
// "-" reads stdin, once. Later files override earlier ones.
func loadParamsFromFiles(fsProvider filesystem.FileSystemProvider, stdin io.Reader, paramsFiles []string, verbose bool) (map[string]string, error) {
	parameters := make(map[string]string)

	stdinUses := 0
	for _, f := range paramsFiles {
		if f == "-" {
			stdinUses++
		}
	}
	if stdinUses > 1 {
		return nil, fmt.Errorf("%w: --params-file - (stdin) given %d times; stdin can be read once", pgmi.ErrInvalidConfig, stdinUses)
	}
	for _, paramsFile := range paramsFiles {
		if verbose {
			fmt.Fprintf(os.Stderr, "[VERBOSE] Loading parameters from file: %s\n", paramsFile)
		}

		fileContent, err := readParamsFile(fsProvider, stdin, paramsFile)
		if err != nil {
			return nil, fmt.Errorf("%w: failed to read params file '%s': %w\n\nTip: Verify the path or use --param to set parameters directly:\n  pgmi deploy . --database mydb --param key=value", pgmi.ErrInvalidConfig, paramsFile, err)
		}

		fileParams, err := params.ParseEnvFile(fileContent)
		if err != nil {
			return nil, fmt.Errorf("%w: failed to parse params file '%s': %w\n\nTip: Verify the file format (KEY=VALUE)", pgmi.ErrInvalidConfig, paramsFile, err)
		}

		if err := loader.CheckCaseCollisions(fileParams); err != nil {
			return nil, fmt.Errorf("params file '%s': %w", paramsFile, err)
		}
		for k, v := range fileParams {
			parameters[strings.ToLower(k)] = v
		}

		if verbose {
			fmt.Fprintf(os.Stderr, "[VERBOSE] Loaded %d parameter(s) from file (total: %d)\n", len(fileParams), len(parameters))
		}
	}

	return parameters, nil
}

func readParamsFile(fsProvider filesystem.FileSystemProvider, stdin io.Reader, path string) ([]byte, error) {
	if path != "-" {
		return fsProvider.ReadFile(path)
	}
	if stdin == nil {
		return nil, errors.New("stdin is not available here")
	}
	return io.ReadAll(stdin)
}

func validateSSLMode(mode string) error {
	if mode == "" {
		return nil
	}
	if !slices.Contains(sslModes, mode) {
		return fmt.Errorf("invalid --sslmode %q (valid: %s): %w",
			mode, "disable, allow, prefer, require, verify-ca, verify-full",
			pgmi.ErrInvalidConfig)
	}
	return nil
}
