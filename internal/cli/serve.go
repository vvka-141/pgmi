package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/vvka-141/pgmi/internal/ai"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/db/manager"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/logging"
	"github.com/vvka-141/pgmi/internal/mcp"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/services"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

const mcpDeployDefaultTimeout = 3 * time.Minute

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run pgmi as an MCP (Model Context Protocol) server over stdio",
	Long: `Expose pgmi's commands as MCP tools over stdio (JSON-RPC 2.0).

MCP-capable assistants (Claude Code, OpenCode) can use pgmi natively instead of
spawning a subprocess and parsing text. The tools map 1:1 to existing CLI
commands — no new deployment semantics. deploy resolves its connection,
database, timeout and parameters like pgmi deploy: tool arguments, then the
server's environment, then the project's pgmi.yaml. The project .env is not
read. Nothing from one call is stored in server state.

Add it to Claude Code with:

  claude mcp add pgmi -- pgmi serve

Tools: deploy, init, metadata_plan, metadata_validate, templates_list,
ai_overview, ai_contract.

The server reads JSON-RPC from stdin and writes responses to stdout; all
diagnostics go to stderr. It exits cleanly on EOF or SIGINT.

A failing tool answers with isError and the session continues. A malformed
message gets a -32700 parse error with a null id and the session continues.
Tool calls run concurrently: ping is answered during a deploy, and
notifications/cancelled stops it. One deploy runs at a time.`,
	Args:          usageArgs(cobra.NoArgs),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
}

func runServe(_ *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); _ = os.Stdin.Close() }()
	return buildMCPServer().Serve(ctx, os.Stdin, os.Stdout)
}

// buildMCPServer registers every pgmi tool on a fresh MCP server. The version
// comes from resolveVersionInfo, not the raw `version` var, which stays "dev"
// on a `go install` build until debug.ReadBuildInfo fills it in — the handshake
// is all an MCP client gets to identify the binary it is driving.
var readOnlyTool = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}

func buildMCPServer() *mcp.Server {
	v, _, _ := resolveVersionInfo()
	s := mcp.NewServer("pgmi", v)

	s.Register(mcp.Tool{
		Name:        "ai_overview",
		Annotations: readOnlyTool,
		Description: "Return the pgmi overview for AI assistants (what pgmi is, core model, CLI reference).",
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return ai.GetOverview()
		},
	})

	s.Register(mcp.Tool{
		Name:        "ai_contract",
		Annotations: readOnlyTool,
		Description: "Return the pgmi session API contract (views, functions, exit codes, test macros) as JSON.",
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return ai.GetContractJSON()
		},
	})

	s.Register(mcp.Tool{
		Name:         "templates_list",
		Annotations:  readOnlyTool,
		Description:  "List the available pgmi project templates (basic, advanced) with descriptions.",
		OutputSchema: templatesOutputSchema(),
		Handler: func(context.Context, json.RawMessage) (any, error) {
			names, err := scaffold.ListTemplates()
			if err != nil {
				return nil, err
			}
			descs := getTemplateDescriptions()
			templates := make([]map[string]string, 0, len(names))
			for _, name := range names {
				templates = append(templates, map[string]string{
					"name":        name,
					"description": descs[name].Short,
					"bestFor":     descs[name].BestFor,
				})
			}
			return map[string]any{"templates": templates}, nil
		},
	})

	s.Register(mcp.Tool{
		Name:        "metadata_plan",
		Annotations: readOnlyTool,
		Description: "Return, without a database, the rows pgmi_plan_view will hold at deploy time: each file once per sort key, in execution order.",
		InputSchema: objectSchema(map[string]any{
			"path": stringProp("Path to the pgmi project directory"),
		}, "path"),
		OutputSchema: metadataPlanOutputSchema(),
		Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
			a, err := decodeArgs[struct {
				Path string `json:"path"`
			}](raw)
			if err != nil {
				return nil, err
			}
			if a.Path == "" {
				return nil, errors.New("path is required")
			}
			return planProject(a.Path)
		},
	})

	s.Register(mcp.Tool{
		Name:        "metadata_validate",
		Annotations: readOnlyTool,
		Description: "Validate a project's <pgmi-meta> blocks (XML validity and duplicate-id check). Filesystem-only, no database.",
		InputSchema: objectSchema(map[string]any{
			"path": stringProp("Path to the pgmi project directory"),
		}, "path"),
		OutputSchema: metadataValidateOutputSchema(),
		Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
			a, err := decodeArgs[struct {
				Path string `json:"path"`
			}](raw)
			if err != nil {
				return nil, err
			}
			if a.Path == "" {
				return nil, errors.New("path is required")
			}
			return validateProject(a.Path)
		},
	})

	s.Register(mcp.Tool{
		Name:        "init",
		Annotations: &mcp.ToolAnnotations{},
		Description: "Scaffold a new pgmi project from a template into an empty or new directory.",
		InputSchema: objectSchema(map[string]any{
			"path":     stringProp("Target directory for the new project"),
			"template": stringProp("Template name: basic (default) or advanced"),
			"name":     stringProp("Project name (defaults to the directory name)"),
		}, "path"),
		OutputSchema: initOutputSchema(),
		Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
			a, err := decodeArgs[struct {
				Path     string `json:"path"`
				Template string `json:"template"`
				Name     string `json:"name"`
			}](raw)
			if err != nil {
				return nil, err
			}
			if a.Path == "" {
				return nil, errors.New("path is required")
			}
			tmpl := a.Template
			if tmpl == "" {
				tmpl = "basic"
			}
			if !scaffold.IsValidTemplate(tmpl) {
				return nil, fmt.Errorf("%w: unknown template: %s", pgmi.ErrUsage, tmpl)
			}
			name := a.Name
			if name == "" {
				name = defaultProjectName(a.Path)
			}
			if err := scaffold.NewScaffolder(false).WithVersion(scaffoldVersion()).CreateProject(name, tmpl, a.Path); err != nil {
				return nil, err
			}
			return map[string]any{"created": true, "path": a.Path, "template": tmpl}, nil
		},
	})

	s.Register(mcp.Tool{
		Name:        "deploy",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: true, OpenWorldHint: true},
		Summary:     deploySummary,
		Description: "Run a pgmi deployment against a database and return the structured result. " +
			"Resolves like `pgmi deploy`: arguments override the server's environment (PGMI_CONNECTION_STRING, DATABASE_URL, PG*), " +
			"which overrides the project's pgmi.yaml (connection, params, timeout). Keep secrets in the environment (PGMI_PARAM_<NAME> becomes parameter <name>, below a params file) or a params file, not in arguments. " +
			"overwrite=true DROPS the target database: it additionally requires confirmDatabaseName to equal the resolved database.",
		InputSchema: objectSchema(map[string]any{
			"path":       stringProp("Path to the pgmi project directory (contains deploy.sql)"),
			"connection": stringProp("PostgreSQL connection string: URI, libpq keyword/value (host=... dbname=...), or ADO.NET. Default: environment, then pgmi.yaml"),
			"database":   stringProp("Target database name. Default: the connection's database, then pgmi.yaml"),
			"overwrite":  boolProp("Drop and recreate the target database before deploying (destructive)"),
			"confirmDatabaseName": stringProp(
				"Required when overwrite=true: repeat the target database name exactly. " +
					"A mismatch aborts before any connection is made."),
			"timeout":             stringProp("Catastrophic-failure timeout, e.g. \"3m\" (default: pgmi.yaml, then 3m). Use \"0\" for no limit"),
			"maintenanceDatabase": stringProp("Database used for CREATE/DROP DATABASE (default: resolved like pgmi deploy, usually \"postgres\")"),
			"verbose":             boolProp("Like pgmi deploy -v: RAISE DEBUG from deploy.sql and tests reaches notices"),
			"paramsFile": map[string]any{
				"description": "Params file path (KEY=VALUE lines), or an array of them; later files win, like --params-file. Stdin (-) is not accepted",
				"anyOf": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
			},
			"params": map[string]any{
				"type":                 "object",
				"description":          "Deployment parameters, available as current_setting('pgmi.key'); override paramsFile and pgmi.yaml, like --param",
				"additionalProperties": map[string]any{"type": "string"},
			},
		}, "path"),
		OutputSchema: deployOutputSchema(),
		Handler:      mcpDeployHandler,
	})

	return s
}

type notice struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// noticeBuffer captures the notice stream of a deploy for the MCP tool result
// while still forwarding to stderr for the human operator. Bounded: it keeps
// the first head and last tail notices, and up to maxWarnings WARNINGs in
// between, because a long deploy's warnings (a defaulted password, a skipped
// step) sit in the middle and are what the agent most needs to see.
type noticeBuffer struct {
	mu          sync.Mutex
	head        int
	tail        int
	maxWarnings int
	first       []notice
	warnings    []notice
	last        []notice
	total       int
}

func newNoticeBuffer() *noticeBuffer {
	return &noticeBuffer{head: 50, tail: 150, maxWarnings: 200}
}

func (b *noticeBuffer) add(severity, message, detail, hint string) {
	b.mu.Lock()
	b.total++
	n := notice{Severity: severity, Message: message}
	switch {
	case len(b.first) < b.head:
		b.first = append(b.first, n)
	default:
		b.last = append(b.last, n)
		if len(b.last) > b.tail {
			if b.last[0].Severity == "WARNING" && len(b.warnings) < b.maxWarnings {
				b.warnings = append(b.warnings, b.last[0])
			}
			b.last = b.last[1:]
		}
	}
	b.mu.Unlock()
	db.DefaultNoticeHandler(severity, message, detail, hint)
}

func (b *noticeBuffer) fields() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := slices.Concat(b.first, b.warnings, b.last)
	if kept == nil {
		kept = []notice{}
	}
	f := map[string]any{"notices": kept}
	if truncated := b.total - len(kept); truncated > 0 {
		f["noticesTruncated"] = truncated
	}
	return f
}

// stringList accepts a JSON string or an array of strings.
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*l = stringList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("must be a string or an array of strings")
	}
	*l = many
	return nil
}

type mcpDeployArgs struct {
	Path                string            `json:"path"`
	Connection          string            `json:"connection"`
	Database            string            `json:"database"`
	Overwrite           bool              `json:"overwrite"`
	ConfirmDatabaseName string            `json:"confirmDatabaseName"`
	Timeout             string            `json:"timeout"`
	MaintenanceDatabase string            `json:"maintenanceDatabase"`
	ParamsFile          stringList        `json:"paramsFile"`
	Params              map[string]string `json:"params"`
	Verbose             bool              `json:"verbose"`
}

// mcpDeploymentConfig resolves the deploy tool's arguments exactly as
// `pgmi deploy` resolves its flags. The project .env is not loaded: serve is
// long-lived and godotenv writes the process environment, so one project's
// credentials would leak into the next project's deploy.
func mcpDeploymentConfig(a mcpDeployArgs) (pgmi.DeploymentConfig, error) {
	if a.Path == "" {
		return pgmi.DeploymentConfig{}, fmt.Errorf("%w: path is required", pgmi.ErrInvalidConfig)
	}
	projectCfg, err := loadProjectYAML(a.Path)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}

	req := deployRequest{
		conn:                connectionFlags{connection: a.Connection, database: a.Database},
		overwrite:           a.Overwrite,
		force:               a.Overwrite,
		paramsFiles:         a.ParamsFile,
		environ:             os.Environ(),
		timeout:             mcpDeployDefaultTimeout,
		maintenanceDatabase: a.MaintenanceDatabase,
		announceEnvSource:   true,
		verbose:             a.Verbose,
	}
	if a.Timeout != "" {
		if req.timeout, err = time.ParseDuration(a.Timeout); err != nil {
			return pgmi.DeploymentConfig{}, fmt.Errorf("%w: invalid timeout: %w", pgmi.ErrInvalidConfig, err)
		}
		req.timeoutSet = true
	}
	if err := loader.ValidateParameters(a.Params); err != nil {
		return pgmi.DeploymentConfig{}, err
	}
	for _, k := range slices.Sorted(maps.Keys(a.Params)) {
		req.params = append(req.params, k+"="+a.Params[k])
	}

	cfg, err := resolveDeploymentConfig(a.Path, projectCfg, req)
	if err != nil {
		return pgmi.DeploymentConfig{}, err
	}
	return cfg, cfg.Validate()
}

func mcpDeployHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	a, err := decodeArgs[mcpDeployArgs](raw)
	if err != nil {
		return nil, err
	}

	cfg, err := mcpDeploymentConfig(a)
	if err != nil {
		return nil, err
	}

	// overwrite drops the database and this path auto-approves (no TTY to prompt).
	// The echo-back is the only friction between a hallucinated database name and
	// a destroyed database, so it is checked before anything connects.
	if err := confirmOverwrite(a.Overwrite, cfg.DatabaseName, a.ConfirmDatabaseName); err != nil {
		return nil, err
	}

	// The notice handler is package-level, so one deploy at a time; tool calls
	// otherwise run concurrently, which is what lets ping and cancellation
	// reach the server while a deploy runs.
	if !mcpDeployMu.TryLock() {
		return nil, fmt.Errorf("%w: another deploy is already running in this pgmi serve session; wait for its result, or cancel it", pgmi.ErrConcurrentDeploy)
	}
	defer mcpDeployMu.Unlock()

	notices := newNoticeBuffer()
	origHandler := db.NoticeHandler
	db.NoticeHandler = notices.add
	defer func() { db.NoticeHandler = origHandler }()

	prov := deployProvenance{startedAt: time.Now(), compat: cfg.Compat, target: targetOf(cfg)}
	result, err := runMCPDeploy(ctx, cfg)
	out := deployResultFields(result, err, prov)
	maps.Copy(out, notices.fields())
	if err != nil {
		return nil, &mcp.FieldsError{Err: err, Fields: out}
	}
	return out, nil
}

var mcpDeployMu sync.Mutex

// deploySummary is the text of a successful deploy: one line, the whole record
// is in structuredContent.
func deploySummary(result any) string {
	out, _ := result.(map[string]any)
	warnings := 0
	notices, _ := out["notices"].([]notice)
	for _, n := range notices {
		if n.Severity == "WARNING" {
			warnings++
		}
	}
	return fmt.Sprintf("deployed %v: %v files loaded, %v paths ignored, %d notices (%d warnings) in %vms; the full record is in structuredContent",
		out["database"], out["filesLoaded"], out["pathsIgnored"], len(notices)+toInt(out["noticesTruncated"]), warnings, out["durationMs"])
}

func toInt(v any) int {
	n, _ := v.(int)
	return n
}

// confirmOverwrite gates the destructive path on an exact echo-back of the target
// database name. An agent that hallucinated the name cannot also hallucinate the
// same wrong name twice by accident; a human reviewing the tool call sees the
// database it is about to lose written out in the arguments.
func confirmOverwrite(overwrite bool, database, confirm string) error {
	if !overwrite {
		return nil
	}
	if confirm == "" {
		return fmt.Errorf(
			"overwrite=true drops database %q: pass confirmDatabaseName=%q to confirm",
			database, database)
	}
	if confirm != database {
		return fmt.Errorf(
			"confirmDatabaseName %q does not match database %q; nothing was deployed and no database was dropped",
			confirm, database)
	}
	return nil
}

// runMCPDeploy wires a one-shot deployment service with a non-interactive
// approver and stderr logging (stdout is the JSON-RPC channel).
func runMCPDeploy(ctx context.Context, cfg pgmi.DeploymentConfig) (*services.DeployResult, error) {
	logger := logging.NewConsoleLogger(cfg.Verbose)
	fileScanner := scanner.NewScanner(checksum.New())
	fileLoader := loader.NewLoader()
	dbManager := manager.New()
	pgmiVersion, _, _ := resolveVersionInfo()
	sessionManager := services.NewSessionManager(db.NewConnector, fileScanner, fileLoader, logger, pgmiVersion)
	deployer := services.NewDeploymentService(db.NewConnector, autoApprover{}, logger, sessionManager, dbManager)

	ctx, cancel := deadlineContext(ctx, cfg.Timeout)
	defer cancel()

	err := timeoutExceeded(ctx, deployer.Deploy(ctx, cfg), cfg.Timeout)
	return deployer.LastResult(), err
}

// autoApprover approves destructive operations without prompting. The MCP
// client made the request with overwrite=true explicitly; there is no TTY to
// prompt and stdout carries JSON-RPC, so the interactive/countdown approvers
// do not apply.
type autoApprover struct{}

func (autoApprover) RequestApproval(context.Context, string) (bool, error) { return true, nil }

func decodeArgs[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func boolProp(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func intProp(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func arrayOf(items map[string]any, description string) map[string]any {
	return map[string]any{"type": "array", "description": description, "items": items}
}

// Output schemas. Declared only for tools whose result is a structured value —
// ai_overview and ai_contract return markdown/JSON text and produce no
// structuredContent, so advertising a schema for them would be a lie the spec
// forbids.
//
// Every declared schema admits two shapes. MCP 2025-11-25 requires servers to
// "provide structured results that conform to this schema" with no exemption
// for isError, and a failed tool call still carries the ErrorDetail the agent
// surface depends on — sqlstate, exit code, the notice stream. Declaring only
// the success shape would make every failure a spec violation that a
// validating client may reject rather than display.

func withErrorVariant(success map[string]any) map[string]any {
	return map[string]any{
		"type":  "object",
		"anyOf": []any{success, errorOutputSchema()},
	}
}

// errorOutputSchema describes pgmi.ErrorDetail. Additional properties are
// permitted: a failed deploy folds its notice stream in beside these fields.
func errorOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"message":    stringProp("Human-readable failure summary"),
		"exitCode":   intProp("Exit code the CLI would return for this failure"),
		"sqlstate":   stringProp("PostgreSQL SQLSTATE, when the failure came from the server"),
		"detail":     stringProp("PostgreSQL DETAIL"),
		"hint":       stringProp("PostgreSQL HINT"),
		"where":      stringProp("PostgreSQL WHERE context"),
		"failedFile": stringProp("Project file the template attributed the failure to"),
		"failedFileCandidates": arrayOf(stringProp("SQL file path"),
			"Every SQL file holding the failing text, when more than one does; failedFile is the first"),
		"phase":          stringProp("test when the failure is in a test or fixture under __test__/, deploy for any other failure of deploy.sql"),
		"script":         stringProp("The file the failure was placed in: a project or test file, or deploy.sql"),
		"line":           intProp("Line within that script"),
		"column":         intProp("Column within that line"),
		"sourceLine":     stringProp("Text of the offending line"),
		"scriptExpanded": boolProp("True when line refers to the macro-expanded script, not the file on disk"),
	}, "message", "exitCode")
}

func templatesOutputSchema() map[string]any {
	return withErrorVariant(objectSchema(map[string]any{
		"templates": arrayOf(objectSchema(map[string]any{
			"name":        stringProp("Template name, pass to init"),
			"description": stringProp("One-line summary"),
			"bestFor":     stringProp("When to choose this template"),
		}, "name"), "Available project templates"),
	}, "templates"))
}

func metadataPlanOutputSchema() map[string]any {
	return withErrorVariant(objectSchema(map[string]any{
		"totalFiles": intProp("Loaded non-test files"),
		"plan": arrayOf(objectSchema(map[string]any{
			"executionOrder": intProp("pgmi_plan_view.execution_order"),
			"path":           stringProp("Project-relative file path"),
			"sortKey":        stringProp("The sort key of this row; the path when the file has none"),
			"id":             stringProp("<pgmi-meta> id, or the path-derived fallback id"),
			"idempotent":     boolProp("Whether the script is safe to re-run"),
			"description":    stringProp("<pgmi-meta> description"),
			"isSqlFile":      boolProp("pgmi_source_view.is_sql_file"),
		}, "executionOrder", "path", "sortKey"), "The rows of pgmi_plan_view, one per file per sort key, in execution order"),
	}, "totalFiles", "plan"))
}

func metadataValidateOutputSchema() map[string]any {
	return withErrorVariant(objectSchema(map[string]any{
		"totalFiles":           intProp("Files scanned"),
		"filesWithMetadata":    intProp("Files carrying a <pgmi-meta> block"),
		"filesWithoutMetadata": intProp("Files with no metadata (ordered by path)"),
		"validationPassed":     boolProp("True when every block parses and ids are unique"),
		"duplicateIds":         arrayOf(stringProp("Duplicated <pgmi-meta> id"), "Ids claimed by more than one file"),
		"warnings":             arrayOf(stringProp("A file whose <pgmi-meta> is never read"), "Non-fatal findings, such as a -- <pgmi-meta> line comment"),
		"error":                stringProp("Why validation failed, when it did"),
		"exitCode":             intProp("Exit code the CLI returns for the failure"),
	}, "totalFiles", "validationPassed"))
}

func initOutputSchema() map[string]any {
	return withErrorVariant(objectSchema(map[string]any{
		"created":  boolProp("True when the project was scaffolded"),
		"path":     stringProp("Directory the project was written to"),
		"template": stringProp("Template used"),
	}, "created", "path", "template"))
}

func deployOutputSchema() map[string]any {
	return withErrorVariant(objectSchema(map[string]any{
		"status":       stringProp("\"success\" — a failure is returned as an MCP error result"),
		"exitCode":     intProp("The exit code `pgmi deploy` would have returned"),
		"filesLoaded":  intProp("Project files loaded into the session"),
		"pathsIgnored": intProp("Paths left out by the built-in exclusions and .pgmiignore; an ignored directory counts once"),
		"ignoredPaths": arrayOf(objectSchema(map[string]any{
			"path": stringProp("The ignored path in ./ form; a directory ends with /"),
			"rule": stringProp("built-in or .pgmiignore"),
		}, "path", "rule"), "Each ignored path and the rule that left it out"),
		"testMacros":     intProp("pgmi_test() macros expanded in deploy.sql"),
		"durationMs":     intProp("Deployment wall time in milliseconds"),
		"database":       stringProp("Target database"),
		"created":        boolProp("True when this deploy created the database; on a routine deploy it usually means a mistyped database name"),
		"executionUnits": intProp("Units deploy.sql split into at its first top-level COMMIT"),
		"unitsCommitted": intProp("Units that committed"),
		"notices": arrayOf(objectSchema(map[string]any{
			"severity": stringProp("NOTICE, WARNING, INFO, ..."),
			"message":  stringProp("The notice text"),
		}, "severity", "message"), "The deploy's notice stream: the first 50, up to 200 WARNINGs from the middle, and the last 150"),
		"noticesTruncated": intProp("Notices dropped from the middle of the stream, WARNINGs beyond the first 200 included; absent when none were"),
	}, "status", "exitCode", "filesLoaded", "database"))
}

func scaffoldVersion() string {
	v, _, _ := resolveVersionInfo()
	return v
}
