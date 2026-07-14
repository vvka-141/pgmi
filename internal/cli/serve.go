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
commands — no new deployment semantics. Connection and parameters are passed per
tool call, never stored in server state.

Add it to Claude Code with:

  claude mcp add pgmi -- pgmi serve

Tools: deploy, init, metadata_plan, metadata_validate, templates_list,
ai_overview, ai_skills, ai_skill, ai_contract.

The server reads JSON-RPC from stdin and writes responses to stdout; all
diagnostics go to stderr. It exits cleanly on EOF or SIGINT.`,
	Args:          cobra.NoArgs,
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
	return buildMCPServer(version).Serve(ctx, os.Stdin, os.Stdout)
}

// buildMCPServer registers every pgmi tool on a fresh MCP server.
func buildMCPServer(serverVersion string) *mcp.Server {
	s := mcp.NewServer("pgmi", serverVersion)

	s.Register(mcp.Tool{
		Name:        "ai_overview",
		Description: "Return the pgmi overview for AI assistants (what pgmi is, core model, CLI reference).",
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return ai.GetOverview()
		},
	})

	s.Register(mcp.Tool{
		Name:         "ai_skills",
		Description:  "List the embedded pgmi skills (name + description) available via ai_skill.",
		OutputSchema: skillsOutputSchema(),
		Handler: func(context.Context, json.RawMessage) (any, error) {
			skills, err := ai.ListSkills()
			if err != nil {
				return nil, err
			}
			return map[string]any{"skills": skills}, nil
		},
	})

	s.Register(mcp.Tool{
		Name:        "ai_skill",
		Description: "Return the full content of a named pgmi skill (e.g. pgmi-sql, pgmi-handler-patterns).",
		InputSchema: objectSchema(map[string]any{
			"name": stringProp("Skill name, e.g. pgmi-sql"),
		}, "name"),
		Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
			a, err := decodeArgs[struct {
				Name string `json:"name"`
			}](raw)
			if err != nil {
				return nil, err
			}
			if a.Name == "" {
				return nil, errors.New("name is required")
			}
			return ai.GetSkill(a.Name)
		},
	})

	s.Register(mcp.Tool{
		Name:        "ai_contract",
		Description: "Return the pgmi session API contract (views, functions, exit codes, test macros) as JSON.",
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return ai.GetContractJSON()
		},
	})

	s.Register(mcp.Tool{
		Name:         "templates_list",
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
		Description: "Scan an advanced-template project and return its files in approximate deployment execution order. Filesystem-only, no database.",
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
				return nil, errors.New("unknown template: " + tmpl)
			}
			if err := scaffold.NewScaffolder(false).CreateProject(a.Name, tmpl, a.Path); err != nil {
				return nil, err
			}
			return map[string]any{"created": true, "path": a.Path, "template": tmpl}, nil
		},
	})

	s.Register(mcp.Tool{
		Name: "deploy",
		Description: "Run a pgmi deployment against a database and return the structured result. " +
			"Provide a connection string and target database; pass secrets here, never on a shared command line. " +
			"overwrite=true DROPS the target database: it additionally requires confirmDatabaseName to equal database.",
		InputSchema: objectSchema(map[string]any{
			"path":       stringProp("Path to the pgmi project directory (contains deploy.sql)"),
			"connection": stringProp("PostgreSQL connection string (URI or ADO.NET)"),
			"database":   stringProp("Target database name"),
			"overwrite":  boolProp("Drop and recreate the target database before deploying (destructive)"),
			"confirmDatabaseName": stringProp(
				"Required when overwrite=true: repeat the target database name exactly. " +
					"A mismatch aborts before any connection is made."),
			"timeout":             stringProp("Catastrophic-failure timeout, e.g. \"3m\" (default 3m)"),
			"maintenanceDatabase": stringProp("Database used for CREATE/DROP DATABASE (default \"postgres\")"),
			"params": map[string]any{
				"type":                 "object",
				"description":          "Deployment parameters, available as current_setting('pgmi.key')",
				"additionalProperties": map[string]any{"type": "string"},
			},
		}, "path", "connection", "database"),
		OutputSchema: deployOutputSchema(),
		Handler:      mcpDeployHandler,
	})

	return s
}

// noticeBuffer captures the RAISE NOTICE stream of a deploy for the MCP tool
// result while still forwarding to stderr for the human operator. Bounded:
// keeps the last max lines and counts the rest as truncated.
type noticeBuffer struct {
	mu    sync.Mutex
	max   int
	lines []string
	total int
}

func (b *noticeBuffer) add(message, detail, hint string) {
	b.mu.Lock()
	b.total++
	b.lines = append(b.lines, message)
	if len(b.lines) > b.max {
		b.lines = b.lines[1:]
	}
	b.mu.Unlock()
	db.DefaultNoticeHandler(message, detail, hint)
}

func (b *noticeBuffer) fields() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	lines := slices.Clone(b.lines)
	if lines == nil {
		lines = []string{}
	}
	f := map[string]any{"notices": lines}
	if truncated := b.total - len(b.lines); truncated > 0 {
		f["noticesTruncated"] = truncated
	}
	return f
}

func mcpDeployHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	a, err := decodeArgs[struct {
		Path                string            `json:"path"`
		Connection          string            `json:"connection"`
		Database            string            `json:"database"`
		Overwrite           bool              `json:"overwrite"`
		ConfirmDatabaseName string            `json:"confirmDatabaseName"`
		Timeout             string            `json:"timeout"`
		MaintenanceDatabase string            `json:"maintenanceDatabase"`
		Params              map[string]string `json:"params"`
	}](raw)
	if err != nil {
		return nil, err
	}

	// overwrite drops the database and this path auto-approves (no TTY to prompt).
	// The echo-back is the only friction between a hallucinated database name and
	// a destroyed database, so it is checked before anything connects.
	if err := confirmOverwrite(a.Overwrite, a.Database, a.ConfirmDatabaseName); err != nil {
		return nil, err
	}

	timeout := mcpDeployDefaultTimeout
	if a.Timeout != "" {
		timeout, err = time.ParseDuration(a.Timeout)
		if err != nil {
			return nil, errors.New("invalid timeout: " + err.Error())
		}
	}

	maintenanceDB := a.MaintenanceDatabase
	if maintenanceDB == "" {
		maintenanceDB = "postgres"
	}

	cfg := pgmi.DeploymentConfig{
		SourcePath:          a.Path,
		DatabaseName:        a.Database,
		MaintenanceDatabase: maintenanceDB,
		ConnectionString:    a.Connection,
		Overwrite:           a.Overwrite,
		Force:               a.Overwrite, // non-interactive; auto-approve the drop
		Parameters:          a.Params,
		Timeout:             timeout,
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Capture the notice stream for the tool result; the stdio loop is
	// sequential, so swapping the package-level handler is race-free
	notices := &noticeBuffer{max: 200}
	origHandler := db.NoticeHandler
	db.NoticeHandler = notices.add
	defer func() { db.NoticeHandler = origHandler }()

	result, err := runMCPDeploy(ctx, cfg)
	if err != nil {
		return nil, &mcp.FieldsError{Err: err, Fields: notices.fields()}
	}
	out := map[string]any{
		"status":      "success",
		"filesLoaded": result.FilesLoaded,
		"testMacros":  result.TestMacros,
		"durationMs":  result.Duration.Milliseconds(),
		"database":    result.Database,
	}
	maps.Copy(out, notices.fields())
	return out, nil
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
	sessionManager := services.NewSessionManager(db.NewConnector, fileScanner, fileLoader, logger)
	deployer := services.NewDeploymentService(db.NewConnector, autoApprover{}, logger, sessionManager, fileScanner, dbManager)

	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	if err := deployer.Deploy(ctx, cfg); err != nil {
		return nil, err
	}
	return deployer.LastResult(), nil
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
// ai_overview, ai_skill and ai_contract return markdown/JSON text and produce no
// structuredContent, so advertising a schema for them would be a lie the spec
// forbids.

func skillsOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"skills": arrayOf(objectSchema(map[string]any{
			"name":        stringProp("Skill name, pass to ai_skill"),
			"description": stringProp("What the skill covers"),
		}, "name", "description"), "Embedded pgmi skills"),
	}, "skills")
}

func templatesOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"templates": arrayOf(objectSchema(map[string]any{
			"name":        stringProp("Template name, pass to init"),
			"description": stringProp("One-line summary"),
			"bestFor":     stringProp("When to choose this template"),
		}, "name"), "Available project templates"),
	}, "templates")
}

func metadataPlanOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"total_files": intProp("Files scanned"),
		"plan": arrayOf(objectSchema(map[string]any{
			"path":        stringProp("Project-relative file path"),
			"id":          stringProp("<pgmi-meta> id; empty when the file has no metadata"),
			"idempotent":  boolProp("Whether the script is safe to re-run"),
			"sort_keys":   arrayOf(stringProp("Sort key"), "Sort keys from <pgmi-meta>; empty means path order"),
			"description": stringProp("<pgmi-meta> description"),
		}, "path", "idempotent"), "Files in approximate deployment execution order"),
	}, "total_files", "plan")
}

func metadataValidateOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"total_files":            intProp("Files scanned"),
		"files_with_metadata":    intProp("Files carrying a <pgmi-meta> block"),
		"files_without_metadata": intProp("Files with no metadata (ordered by path)"),
		"validation_passed":      boolProp("True when every block parses and ids are unique"),
		"duplicate_ids":          arrayOf(stringProp("Duplicated <pgmi-meta> id"), "Ids claimed by more than one file"),
	}, "total_files", "validation_passed")
}

func initOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"created":  boolProp("True when the project was scaffolded"),
		"path":     stringProp("Directory the project was written to"),
		"template": stringProp("Template used"),
	}, "created", "path", "template")
}

func deployOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"status":           stringProp("\"success\" — a failure is returned as an MCP error result"),
		"filesLoaded":      intProp("Project files loaded into the session"),
		"testMacros":       intProp("pgmi_test() macros expanded in deploy.sql"),
		"durationMs":       intProp("Deployment wall time in milliseconds"),
		"database":         stringProp("Target database"),
		"notices":          arrayOf(stringProp("RAISE NOTICE line"), "The deploy's notice stream"),
		"noticesTruncated": intProp("Notices dropped before the retained tail; absent when none were"),
	}, "status", "filesLoaded", "database")
}
