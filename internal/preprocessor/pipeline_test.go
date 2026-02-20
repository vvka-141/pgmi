package preprocessor

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// mockTestGenerate returns a testGenerateFunc that maps patterns to fixed SQL.
func mockTestGenerate(responses map[string]string) testGenerateFunc {
	return func(_ context.Context, _ *pgxpool.Conn, pattern string, _ string) (string, error) {
		if sql, ok := responses[pattern]; ok {
			return sql, nil
		}
		return "", nil
	}
}

func mockTestGenerateError(errMsg string) testGenerateFunc {
	return func(_ context.Context, _ *pgxpool.Conn, _ string, _ string) (string, error) {
		return "", fmt.Errorf("%s", errMsg)
	}
}

func newTestPipeline(fn testGenerateFunc) *Pipeline {
	return &Pipeline{
		commentStripper: NewCommentStripper(),
		macroDetector:   NewMacroDetector(),
		testGenerateFn:  fn,
	}
}

func TestPipeline_Process_NoMacros(t *testing.T) {
	p := newTestPipeline(mockTestGenerate(nil))
	ctx := context.Background()

	sql := "SELECT 1;\nCREATE TABLE t (id INT);"
	result, err := p.Process(ctx, nil, sql)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.MacroCount != 0 {
		t.Errorf("MacroCount = %d, want 0", result.MacroCount)
	}
	if result.ExpandedSQL != sql {
		t.Errorf("ExpandedSQL changed for no-macro input")
	}
}

func TestPipeline_Process_SingleMacro(t *testing.T) {
	p := newTestPipeline(mockTestGenerate(map[string]string{
		"": "-- generated test SQL\nSELECT 'test';",
	}))
	ctx := context.Background()

	sql := "BEGIN;\nCALL pgmi_test();\nCOMMIT;"
	result, err := p.Process(ctx, nil, sql)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.MacroCount != 1 {
		t.Errorf("MacroCount = %d, want 1", result.MacroCount)
	}
	if result.ExpandedSQL == sql {
		t.Error("ExpandedSQL was not modified")
	}
	// The macro should be replaced with generated SQL
	expected := "BEGIN;\n-- generated test SQL\nSELECT 'test';\nCOMMIT;"
	if result.ExpandedSQL != expected {
		t.Errorf("ExpandedSQL = %q, want %q", result.ExpandedSQL, expected)
	}
}

func TestPipeline_Process_MacroWithPattern(t *testing.T) {
	p := newTestPipeline(mockTestGenerate(map[string]string{
		"auth": "-- auth tests\nSELECT 'auth';",
	}))
	ctx := context.Background()

	sql := "CALL pgmi_test('auth');"
	result, err := p.Process(ctx, nil, sql)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.MacroCount != 1 {
		t.Errorf("MacroCount = %d, want 1", result.MacroCount)
	}
	if result.ExpandedSQL != "-- auth tests\nSELECT 'auth';" {
		t.Errorf("ExpandedSQL = %q", result.ExpandedSQL)
	}
}

func TestPipeline_Process_MultipleMacros(t *testing.T) {
	callCount := 0
	p := newTestPipeline(func(_ context.Context, _ *pgxpool.Conn, pattern string, _ string) (string, error) {
		callCount++
		return fmt.Sprintf("/* expanded %d */", callCount), nil
	})
	ctx := context.Background()

	sql := "CALL pgmi_test();\nSELECT 1;\nCALL pgmi_test();"
	result, err := p.Process(ctx, nil, sql)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.MacroCount != 2 {
		t.Errorf("MacroCount = %d, want 2", result.MacroCount)
	}
	// Processed in reverse order: second macro gets callCount=1, first gets callCount=2
	expected := "/* expanded 2 */\nSELECT 1;\n/* expanded 1 */"
	if result.ExpandedSQL != expected {
		t.Errorf("ExpandedSQL = %q, want %q", result.ExpandedSQL, expected)
	}
}

func TestPipeline_Process_MacroInComment(t *testing.T) {
	p := newTestPipeline(mockTestGenerate(nil))
	ctx := context.Background()

	sql := "/* CALL pgmi_test(); */\nSELECT 1;"
	result, err := p.Process(ctx, nil, sql)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.MacroCount != 0 {
		t.Errorf("MacroCount = %d, want 0 (macro in comment should be ignored)", result.MacroCount)
	}
	if result.ExpandedSQL != sql {
		t.Error("ExpandedSQL should not be modified when macro is inside a comment")
	}
}

func TestPipeline_Process_EmptyGenerateResult(t *testing.T) {
	p := newTestPipeline(mockTestGenerate(map[string]string{
		"": "",
	}))
	ctx := context.Background()

	sql := "BEGIN;\nCALL pgmi_test();\nCOMMIT;"
	result, err := p.Process(ctx, nil, sql)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.MacroCount != 1 {
		t.Errorf("MacroCount = %d, want 1", result.MacroCount)
	}
	expected := "BEGIN;\n\nCOMMIT;"
	if result.ExpandedSQL != expected {
		t.Errorf("ExpandedSQL = %q, want %q", result.ExpandedSQL, expected)
	}
}

func TestPipeline_Process_GenerateError(t *testing.T) {
	p := newTestPipeline(mockTestGenerateError("database connection lost"))
	ctx := context.Background()

	sql := "CALL pgmi_test();"
	_, err := p.Process(ctx, nil, sql)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "database connection lost" {
		t.Errorf("error = %q, want %q", err.Error(), "database connection lost")
	}
}

func TestPipeline_Process_MacroWithPgTempPrefix(t *testing.T) {
	p := newTestPipeline(mockTestGenerate(map[string]string{
		"": "-- tests",
	}))
	ctx := context.Background()

	sql := "CALL pg_temp.pgmi_test();"
	result, err := p.Process(ctx, nil, sql)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.MacroCount != 1 {
		t.Errorf("MacroCount = %d, want 1", result.MacroCount)
	}
}

func TestPipeline_Process_InvalidCallback(t *testing.T) {
	p := newTestPipeline(mockTestGenerate(nil))
	ctx := context.Background()

	sql := "CALL pgmi_test('.*', 'DROP TABLE users; --');"
	_, err := p.Process(ctx, nil, sql)
	if err == nil {
		t.Fatal("expected error for invalid callback name, got nil")
	}
}

func TestPipeline_Process_EmptyInput(t *testing.T) {
	p := newTestPipeline(mockTestGenerate(nil))
	ctx := context.Background()

	result, err := p.Process(ctx, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MacroCount != 0 {
		t.Errorf("MacroCount = %d, want 0", result.MacroCount)
	}
	if result.ExpandedSQL != "" {
		t.Errorf("ExpandedSQL = %q, want empty", result.ExpandedSQL)
	}
}
