package ai

import (
	"encoding/json"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

type ContractView struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	// Note carries behaviour a column list cannot express — a normalisation
	// applied on load, say. Omitted where there is nothing to say.
	Note string `json:"note,omitempty"`
}

type ContractFunction struct {
	Name    string   `json:"name"`
	Args    []string `json:"args"`
	Returns []string `json:"returns"`
}

type ContractExitCode struct {
	Code        int    `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ContractMacro struct {
	Form        string `json:"form"`
	Description string `json:"description"`
}

type ContractTypeField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type ContractType struct {
	Name   string              `json:"name"`
	Fields []ContractTypeField `json:"fields"`
	Events []string            `json:"events,omitempty"`
	Note   string              `json:"note,omitempty"`
}

// ContractExecution describes how pgmi splits deploy.sql across transactions.
// Agents that skip this write CREATE INDEX CONCURRENTLY into a plan loop and
// get 25001, or assume a mid-file COMMIT still leaves them atomic.
type ContractExecution struct {
	Summary          string   `json:"summary"`
	Head             string   `json:"head"`
	Tail             string   `json:"tail"`
	Terminators      []string `json:"terminators"`
	NotTerminators   []string `json:"not_terminators"`
	Consequences     []string `json:"consequences"`
	NotViaExecute    []string `json:"not_via_execute"`
	NotViaExecuteWhy string   `json:"not_via_execute_why"`
}

type Contract struct {
	Views             []ContractView     `json:"views"`
	Functions         []ContractFunction `json:"functions"`
	Types             []ContractType     `json:"types"`
	StepTypes         []string           `json:"step_types"`
	StepTypesNote     string             `json:"step_types_note"`
	ExitCodes         []ContractExitCode `json:"exit_codes"`
	Macros            []ContractMacro    `json:"macros"`
	ExecutionContract ContractExecution  `json:"execution_contract"`
}

func GetContract() Contract {
	return Contract{
		Views: []ContractView{
			{
				Name:    "pgmi_source_view",
				Columns: []string{"path", "name", "directory", "extension", "depth", "content", "size_bytes", "checksum", "pgmi_checksum", "path_parts", "is_sql_file", "is_test_file", "parent_folder_name"},
			},
			{
				Name:    "pgmi_parameter_view",
				Columns: []string{"key", "value", "type", "required", "default_value", "description"},
				Note:    "key is lower-cased on load, matching the session variable it mirrors: --param apiVersion=2 is stored as 'apiversion'. WHERE key = 'apiVersion' returns no row, silently, while current_setting('pgmi.apiVersion', true) still works because GUC names are case-insensitive. Compare lower-cased.",
			},
			{
				Name:    "pgmi_plan_view",
				Columns: []string{"path", "content", "checksum", "generic_id", "id", "idempotent", "description", "sort_key", "execution_order"},
			},
			{
				Name:    "pgmi_source_metadata_view",
				Columns: []string{"path", "id", "idempotent", "sort_keys", "description"},
			},
			{
				Name:    "pgmi_test_source_view",
				Columns: []string{"path", "directory", "filename", "content", "is_fixture"},
			},
			{
				Name:    "pgmi_test_directory_view",
				Columns: []string{"path", "parent_path", "depth"},
			},
		},
		Functions: []ContractFunction{
			{
				Name:    "pgmi_test_plan",
				Args:    []string{"p_pattern text DEFAULT NULL"},
				Returns: []string{"ordinal", "step_type", "script_path", "directory", "depth"},
			},
			{
				Name:    "pgmi_test_generate",
				Args:    []string{"p_pattern text DEFAULT NULL", "p_callback text DEFAULT 'pg_temp.pgmi_test_callback'"},
				Returns: []string{"text"},
			},
			{
				Name:    "pgmi_test_callback",
				Args:    []string{"e pg_temp.pgmi_test_event"},
				Returns: []string{"void"},
			},
			{
				Name:    "pgmi_validate_pattern",
				Args:    []string{"p_pattern text"},
				Returns: []string{"text"},
			},
			{
				Name:    "pgmi_has_tests",
				Args:    []string{"p_directory text", "p_pattern text DEFAULT NULL"},
				Returns: []string{"boolean"},
			},
			{
				Name:    "pgmi_register_file",
				Args:    []string{"in_path text", "in_content text", "in_checksum text", "in_pgmi_checksum text"},
				Returns: []string{"_pgmi_source"},
			},
			{
				Name:    "pgmi_is_sql_file",
				Args:    []string{"filename text"},
				Returns: []string{"boolean"},
			},
			{
				Name:    "pgmi_persist_test_plan",
				Args:    []string{"target_schema text", "p_pattern text DEFAULT NULL"},
				Returns: []string{"void"},
			},
		},
		Types: []ContractType{
			{
				Name: "pgmi_test_event",
				Fields: []ContractTypeField{
					{Name: "event", Type: "text"},
					{Name: "path", Type: "text"},
					{Name: "directory", Type: "text"},
					{Name: "depth", Type: "integer"},
					{Name: "ordinal", Type: "integer"},
					{Name: "context", Type: "jsonb"},
				},
				Events: []string{
					"suite_start", "fixture_start", "fixture_end",
					"test_start", "test_end", "rollback",
					"teardown_start", "teardown_end", "suite_end",
				},
				Note: "Passed to the callback function during CALL pgmi_test(). Custom callbacks must accept (pg_temp.pgmi_test_event) and return void. Schema-qualify the callback name: pgmi_test('pattern', 'pg_temp.my_callback').",
			},
		},
		StepTypes:     []string{"fixture", "test", "teardown"},
		StepTypesNote: "Values of pgmi_test_plan.step_type — NOT the callback event values. The callback receives pgmi_test_event.event, which uses a different, finer-grained vocabulary (see types.pgmi_test_event.events).",
		ExitCodes: []ContractExitCode{
			{Code: pgmi.ExitSuccess, Name: "ExitSuccess", Description: "Deployment/test completed successfully"},
			{Code: pgmi.ExitGeneralError, Name: "ExitGeneralError", Description: "Unknown or unclassified error"},
			{Code: pgmi.ExitUsageError, Name: "ExitUsageError", Description: "CLI usage error (missing args, invalid flags)"},
			{Code: pgmi.ExitPanic, Name: "ExitPanic", Description: "Internal panic (unexpected crash)"},
			{Code: pgmi.ExitConfigError, Name: "ExitConfigError", Description: "Invalid pgmi configuration, rejected before connecting (a parameter your SQL requires is exit 13)"},
			{Code: pgmi.ExitConnectionError, Name: "ExitConnectionError", Description: "Failed to connect to database"},
			{Code: pgmi.ExitApprovalDenied, Name: "ExitApprovalDenied", Description: "User denied overwrite approval"},
			{Code: pgmi.ExitExecutionFailed, Name: "ExitExecutionFailed", Description: "SQL execution failed"},
			{Code: pgmi.ExitDeploySQLMissing, Name: "ExitDeploySQLMissing", Description: "deploy.sql not found"},
			{Code: pgmi.ExitConcurrentDeploy, Name: "ExitConcurrentDeploy", Description: "Another pgmi deployment is in progress"},
			{Code: pgmi.ExitTimeout, Name: "ExitTimeout", Description: "Operation exceeded --timeout (context deadline exceeded)"},
			{Code: pgmi.ExitInterrupted, Name: "ExitInterrupted", Description: "Process interrupted by SIGINT (Ctrl-C)"},
		},
		Macros: []ContractMacro{
			{Form: "CALL pgmi_test()", Description: "Run all tests with default callback. Expands to SAVEPOINT/ROLLBACK TO SAVEPOINT, which PostgreSQL refuses inside the implicit block of a multi-statement query, so the call MUST sit inside an explicit top-level BEGIN ... COMMIT. Without one the deploy fails with 25P01 'SAVEPOINT can only be used in transaction blocks' naming a statement you never wrote."},
			{Form: "CALL pgmi_test('pattern')", Description: "Filter tests by POSIX regex"},
			{Form: "CALL pgmi_test('pattern', 'callback')", Description: "Custom callback function"},
		},
		ExecutionContract: ContractExecution{
			Summary: "Before your first top-level COMMIT, atomic mode; after it, psql mode.",
			Head:    "Everything through the first top-level transaction terminator is sent as one unit and runs as a single transaction. A deploy.sql with no top-level terminator is entirely atomic. The scaffolded templates are NOT that shape: they open with BEGIN, COMMIT after the test gate, and keep the DONE banner in the tail, so anything appended to one runs in psql mode.",
			Tail:    "Every top-level statement after that terminator runs on its own with per-statement autocommit, exactly as in psql. An explicit BEGIN ... COMMIT forms a real multi-statement transaction.",
			Terminators: []string{
				"COMMIT", "END", "ROLLBACK", "ABORT",
				"... optionally followed by WORK or TRANSACTION",
				"... optionally followed by AND CHAIN (which leaves a chained transaction open)",
			},
			NotTerminators: []string{
				"ROLLBACK TO [SAVEPOINT] name (savepoint control, not a transaction end)",
				"COMMIT PREPARED / ROLLBACK PREPARED (acts on a foreign prepared transaction)",
				"The END closing a BEGIN ATOMIC function body (the body is stepped over whole, semicolons and all)",
			},
			Consequences: []string{
				"Statements between mid-file COMMITs are not implicitly grouped: a later phase that must be atomic writes its own BEGIN ... COMMIT.",
				"A mid-tail failure stops the deploy but keeps earlier autocommitted statements applied, so tail statements must be idempotent.",
				"A transaction-scoped advisory lock taken in the head is released by the head's COMMIT and does not guard the tail.",
			},
			NotViaExecute:    []string{"CREATE INDEX CONCURRENTLY", "VACUUM", "CREATE DATABASE"},
			NotViaExecuteWhy: "EXECUTE runs statements in a function context, so these are refused with SQLSTATE 25001 ('cannot be executed from a function') whatever the transaction state. Write them at top level in the tail, never inside a plan loop.",
		},
	}
}

func GetContractJSON() (string, error) {
	c := GetContract()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
