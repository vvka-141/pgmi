package ai

import (
	"encoding/json"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

type ContractView struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
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

type Contract struct {
	Views     []ContractView     `json:"views"`
	Functions []ContractFunction `json:"functions"`
	StepTypes []string           `json:"step_types"`
	ExitCodes []ContractExitCode `json:"exit_codes"`
	Macros    []ContractMacro    `json:"macros"`
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
			},
			{
				Name:    "pgmi_plan_view",
				Columns: []string{"path", "content", "checksum", "generic_id", "id", "idempotent", "description", "sort_key", "execution_order"},
			},
			{
				Name:    "pgmi_source_metadata_view",
				Columns: []string{"path", "id", "idempotent", "description", "sort_keys"},
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
				Args:    []string{"pattern text DEFAULT NULL"},
				Returns: []string{"ordinal", "step_type", "script_path", "directory", "depth"},
			},
			{
				Name:    "pgmi_test_generate",
				Args:    []string{"pattern text DEFAULT NULL", "callback text DEFAULT 'pg_temp.pgmi_test_callback'"},
				Returns: []string{"sql text"},
			},
			{
				Name:    "pgmi_is_sql_file",
				Args:    []string{"filename text"},
				Returns: []string{"boolean"},
			},
			{
				Name:    "pgmi_persist_test_plan",
				Args:    []string{"target_schema text", "pattern text DEFAULT NULL"},
				Returns: []string{"void"},
			},
		},
		StepTypes: []string{"fixture", "test", "teardown"},
		ExitCodes: []ContractExitCode{
			{Code: pgmi.ExitSuccess, Name: "ExitSuccess", Description: "Deployment/test completed successfully"},
			{Code: pgmi.ExitGeneralError, Name: "ExitGeneralError", Description: "Unknown or unclassified error"},
			{Code: pgmi.ExitUsageError, Name: "ExitUsageError", Description: "CLI usage error (missing args, invalid flags)"},
			{Code: pgmi.ExitPanic, Name: "ExitPanic", Description: "Internal panic (unexpected crash)"},
			{Code: pgmi.ExitConfigError, Name: "ExitConfigError", Description: "Invalid configuration or parameters"},
			{Code: pgmi.ExitConnectionError, Name: "ExitConnectionError", Description: "Failed to connect to database"},
			{Code: pgmi.ExitApprovalDenied, Name: "ExitApprovalDenied", Description: "User denied overwrite approval"},
			{Code: pgmi.ExitExecutionFailed, Name: "ExitExecutionFailed", Description: "SQL execution failed"},
			{Code: pgmi.ExitDeploySQLMissing, Name: "ExitDeploySQLMissing", Description: "deploy.sql not found"},
			{Code: pgmi.ExitConcurrentDeploy, Name: "ExitConcurrentDeploy", Description: "Another pgmi deployment is in progress"},
			{Code: pgmi.ExitTimeout, Name: "ExitTimeout", Description: "Operation exceeded --timeout (context deadline exceeded)"},
			{Code: pgmi.ExitInterrupted, Name: "ExitInterrupted", Description: "Process interrupted by SIGINT (Ctrl-C)"},
		},
		Macros: []ContractMacro{
			{Form: "CALL pgmi_test()", Description: "Run all tests with default callback"},
			{Form: "CALL pgmi_test('pattern')", Description: "Filter tests by POSIX regex"},
			{Form: "CALL pgmi_test('pattern', 'callback')", Description: "Custom callback function"},
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
