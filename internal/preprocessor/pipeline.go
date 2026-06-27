package preprocessor

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/testgen"
)

// (buildStrippedToOriginalMap was removed together with Strip's usage for
// macro detection. RedactForMacros returns a length-preserved mask so macro
// offsets are directly usable against the original SQL.)

// PreprocessResult contains the result of preprocessing deploy.sql.
type PreprocessResult struct {
	ExpandedSQL string // SQL with macros expanded
	MacroCount  int    // Number of macros expanded
}

// testGenerateFunc is the signature for calling pgmi_test_generate.
type testGenerateFunc func(ctx context.Context, conn *pgxpool.Conn, pattern string, callback string) (string, error)

// Pipeline preprocesses SQL by expanding macros.
type Pipeline struct {
	commentStripper CommentStripper
	macroDetector   MacroDetector
	testGenerateFn  testGenerateFunc
}

// NewPipeline creates a new preprocessing pipeline.
func NewPipeline() *Pipeline {
	return &Pipeline{
		commentStripper: NewCommentStripper(),
		macroDetector:   NewMacroDetector(),
		testGenerateFn:  callTestGenerate,
	}
}

// Process preprocesses SQL by expanding CALL pgmi_test() macros.
// Queries the pg_temp.pgmi_test_plan() function for test execution plan
// and generates EXECUTE-based SQL that fetches content from pgmi_test_source.
func (p *Pipeline) Process(ctx context.Context, conn *pgxpool.Conn, sql string) (*PreprocessResult, error) {
	result := &PreprocessResult{
		ExpandedSQL: sql,
		MacroCount:  0,
	}

	// Mask out comment and string-literal bytes with spaces, preserving
	// byte positions. The macro detector can safely regex over the result
	// without matching inside 'CALL pgmi_test();' literals or $$ quoted $$
	// bodies. Positions it returns are directly usable against `sql`.
	redactedSQL := p.commentStripper.RedactForMacros(sql)

	macros := p.macroDetector.Detect(sql, redactedSQL)
	if len(macros) == 0 {
		return result, nil
	}

	result.MacroCount = len(macros)

	sortedMacros := make([]MacroCall, len(macros))
	copy(sortedMacros, macros)
	slices.SortFunc(sortedMacros, func(a, b MacroCall) int {
		return cmp.Compare(b.StartPos, a.StartPos)
	})

	expandedSQL := sql

	for _, macro := range sortedMacros {
		if err := testgen.ValidateCallbackName(macro.Callback); err != nil {
			return nil, err
		}

		generatedSQL, err := p.testGenerateFn(ctx, conn, macro.Pattern, macro.Callback)
		if err != nil {
			return nil, err
		}

		expandedSQL = expandedSQL[:macro.StartPos] + generatedSQL + expandedSQL[macro.EndPos:]
	}

	result.ExpandedSQL = expandedSQL
	return result, nil
}

// callTestGenerate calls pg_temp.pgmi_test_generate() to get test execution SQL.
// This delegates test SQL generation to PostgreSQL, making it part of the API contract.
func callTestGenerate(ctx context.Context, conn *pgxpool.Conn, pattern string, callback string) (string, error) {
	query := `SELECT pg_temp.pgmi_test_generate($1, $2)`

	var generatedSQL sql.NullString
	err := conn.QueryRow(ctx, query,
		nullIfEmpty(pattern),
		nullIfEmpty(callback),
	).Scan(&generatedSQL)

	if err != nil {
		return "", fmt.Errorf("failed to call pgmi_test_generate: %w", err)
	}

	if !generatedSQL.Valid {
		return "", nil
	}

	return generatedSQL.String, nil
}

// nullIfEmpty returns nil if s is empty, otherwise returns s.
// Used for SQL NULL parameters.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
