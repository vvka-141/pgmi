package preprocessor

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/sourcemap"
	"github.com/vvka-141/pgmi/internal/testgen"
)

// PreprocessResult contains the result of preprocessing deploy.sql.
type PreprocessResult struct {
	ExpandedSQL string              // SQL with macros expanded
	SourceMap   *sourcemap.SourceMap // Mapping for error attribution
	MacroCount  int                 // Number of macros expanded
}

// Pipeline preprocesses SQL by expanding macros.
type Pipeline struct {
	commentStripper CommentStripper
	macroDetector   MacroDetector
}

// NewPipeline creates a new preprocessing pipeline.
func NewPipeline() *Pipeline {
	return &Pipeline{
		commentStripper: NewCommentStripper(),
		macroDetector:   NewMacroDetector(),
	}
}

// Process preprocesses SQL by expanding CALL pgmi_test() macros.
// Queries the pg_temp.pgmi_test_plan() function for test execution plan
// and generates EXECUTE-based SQL that fetches content from pgmi_test_source.
func (p *Pipeline) Process(ctx context.Context, conn *pgxpool.Conn, sql string) (*PreprocessResult, error) {
	result := &PreprocessResult{
		ExpandedSQL: sql,
		SourceMap:   sourcemap.New(),
		MacroCount:  0,
	}

	// Strip comments to find macros
	strippedSQL := p.commentStripper.Strip(sql)

	// Detect macros in stripped SQL
	macros := p.macroDetector.Detect(strippedSQL)
	if len(macros) == 0 {
		return result, nil
	}

	result.MacroCount = len(macros)

	// Process macros in reverse order to maintain correct positions
	// (since each replacement may change the length of the string)
	sortedMacros := make([]MacroCall, len(macros))
	copy(sortedMacros, macros)
	sort.Slice(sortedMacros, func(i, j int) bool {
		return sortedMacros[i].StartPos > sortedMacros[j].StartPos
	})

	expandedSQL := sql

	for _, macro := range sortedMacros {
		// Validate callback name format
		if err := testgen.ValidateCallbackName(macro.Callback); err != nil {
			return nil, err
		}

		// Call pg_temp.pgmi_test_generate() to get the test execution SQL
		generatedSQL, err := callTestGenerate(ctx, conn, macro.Pattern, macro.Callback)
		if err != nil {
			return nil, err
		}

		// Extract macro text from stripped SQL and find it in expandedSQL
		// Since we process in reverse order, use LastIndex to find the rightmost occurrence
		macroText := strippedSQL[macro.StartPos:macro.EndPos]
		idx := strings.LastIndex(expandedSQL, macroText)
		if idx == -1 {
			return nil, fmt.Errorf("failed to locate macro %q in SQL for expansion", macroText)
		}

		// Replace the macro with generated SQL
		expandedSQL = expandedSQL[:idx] + generatedSQL + expandedSQL[idx+len(macroText):]
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
