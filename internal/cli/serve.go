package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
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
		Name:        "ai_skills",
		Description: "List the embedded pgmi skills (name + description) available via ai_skill.",
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
		Name:        "templates_list",
		Description: "List the available pgmi project templates (basic, advanced) with descriptions.",
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
		Name:        "deploy",
		Description: "Run a pgmi deployment against a database and return the structured result. Provide a connection string and target database; pass secrets here, never on a shared command line.",
		InputSchema: objectSchema(map[string]any{
			"path":       stringProp("Path to the pgmi project directory (contains deploy.sql)"),
			"connection": stringProp("PostgreSQL connection string (URI or ADO.NET)"),
			"database":   stringProp("Target database name"),
			"overwrite":  boolProp("Drop and recreate the target database before deploying"),
			"timeout":    stringProp("Catastrophic-failure timeout, e.g. \"3m\" (default 3m)"),
			"maintenanceDatabase": stringProp("Database used for CREATE/DROP DATABASE (default \"postgres\")"),
			"params": map[string]any{
				"type":                 "object",
				"description":          "Deployment parameters, available as current_setting('pgmi.key')",
				"additionalProperties": map[string]any{"type": "string"},
			},
		}, "path", "connection", "database"),
		Handler: mcpDeployHandler,
	})

	return s
}

func mcpDeployHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	a, err := decodeArgs[struct {
		Path                string            `json:"path"`
		Connection          string            `json:"connection"`
		Database            string            `json:"database"`
		Overwrite           bool              `json:"overwrite"`
		Timeout             string            `json:"timeout"`
		MaintenanceDatabase string            `json:"maintenanceDatabase"`
		Params              map[string]string `json:"params"`
	}](raw)
	if err != nil {
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

	result, err := runMCPDeploy(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status":      "success",
		"filesLoaded": result.FilesLoaded,
		"testMacros":  result.TestMacros,
		"durationMs":  result.Duration.Milliseconds(),
		"database":    result.Database,
	}, nil
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
