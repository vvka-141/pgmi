package mcp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestErrorResultTextCarriesExitCodeAndPhase(t *testing.T) {
	test := "DO $$\nBEGIN\n    RAISE EXCEPTION 'TEST FAILED: x';\nEND $$;\n"
	var scriptErr error = &pgmi.ScriptError{Err: &pgconn.PgError{Code: "P0001", Message: "TEST FAILED: x",
		Where: "PL/pgSQL function inline_code_block line 3 at RAISE\nSQL statement \"" + test + "\"\nPL/pgSQL function inline_code_block line 9 at EXECUTE",
	}, Name: "deploy.sql", Script: "DO $$ ... $$;"}
	scriptErr.(*pgmi.ScriptError).Files = []pgmi.FileMetadata{{Path: "./__test__/test_x.sql", Content: test}}
	err := fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, scriptErr)

	result := errorResult(err)
	if text := result.Content[0].Text; !strings.Contains(text, "\nexit code 13, phase test") {
		t.Errorf("the text must carry the exit code and phase:\n%s", text)
	}
	if d, ok := result.StructuredContent.(*pgmi.ErrorDetail); !ok || d.ExitCode != 13 || d.Phase != "test" {
		t.Errorf("structuredContent changed: %#v", result.StructuredContent)
	}

	plain := errorResult(fmt.Errorf("%w: bad", pgmi.ErrInvalidConfig))
	if text := plain.Content[0].Text; !strings.HasSuffix(text, "\nexit code 10") {
		t.Errorf("a failure without a phase carries the exit code alone:\n%s", text)
	}
}
