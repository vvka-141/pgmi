package preprocessor

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// PreprocessResult contains the result of preprocessing deploy.sql.
type PreprocessResult struct {
	ExpandedSQL string  // SQL with macros expanded
	MacroCount  int     // Number of macros expanded
	Lines       LineMap // maps ExpandedSQL lines back to the input
}

// macroSpan is one expanded macro: its lines in the input and in the output.
type macroSpan struct {
	origLine, expStart, expEnd, shiftAfter int
}

// LineMap maps a line of the expanded script to the line of the script on
// disk. A line inside a macro's expansion maps to the macro's own line.
type LineMap []macroSpan

// OriginalLine returns the input line for an expanded line, and whether the
// expanded line came from a macro expansion rather than from the input.
func (m LineMap) OriginalLine(expanded int) (line int, inMacro bool) {
	shift := 0
	for _, span := range m {
		if expanded < span.expStart {
			break
		}
		if expanded <= span.expEnd {
			return span.origLine, true
		}
		shift = span.shiftAfter
	}
	return expanded - shift, false
}

// testGenerateFunc is the signature for calling pgmi_test_generate.
type testGenerateFunc func(ctx context.Context, conn *pgxpool.Conn, pattern string, callback string) (string, error)

// Pipeline preprocesses SQL by expanding macros.
type Pipeline struct {
	commentStripper *CommentStripper
	macroDetector   *MacroDetector
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
	generated := make(map[int]string, len(sortedMacros))

	for _, macro := range sortedMacros {
		if err := validateCallbackName(macro.Callback); err != nil {
			return nil, err
		}

		generatedSQL, err := p.testGenerateFn(ctx, conn, macro.Pattern, macro.Callback)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", pgmi.ErrExecutionFailed, err)
		}

		expandedSQL = expandedSQL[:macro.StartPos] + generatedSQL + expandedSQL[macro.EndPos:]
		generated[macro.StartPos] = generatedSQL
	}

	result.ExpandedSQL = expandedSQL
	result.Lines = buildLineMap(sql, macros, generated)
	return result, nil
}

func buildLineMap(sql string, macros []MacroCall, generated map[int]string) LineMap {
	ascending := slices.Clone(macros)
	slices.SortFunc(ascending, func(a, b MacroCall) int { return cmp.Compare(a.StartPos, b.StartPos) })
	var (
		m     LineMap
		shift int
	)
	for _, macro := range ascending {
		origLine := strings.Count(sql[:macro.StartPos], "\n") + 1
		origSpan := strings.Count(sql[macro.StartPos:macro.EndPos], "\n")
		expSpan := strings.Count(generated[macro.StartPos], "\n")
		expStart := origLine + shift
		shift += expSpan - origSpan
		m = append(m, macroSpan{origLine: origLine, expStart: expStart, expEnd: expStart + expSpan, shiftAfter: shift})
	}
	return m
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
