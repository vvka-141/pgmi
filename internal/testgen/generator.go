package testgen

import (
	"fmt"
	"strings"

	"github.com/vvka-141/pgmi/internal/sourcemap"
	"github.com/vvka-141/pgmi/internal/testdiscovery"
)

// GeneratedSQL holds the generated SQL and its source map for error attribution.
type GeneratedSQL struct {
	SQL       string
	SourceMap *sourcemap.SourceMap
}

// DirectGenerator produces literal SQL for direct test execution.
type DirectGenerator struct{}

// NewDirectGenerator creates a new DirectGenerator.
func NewDirectGenerator() *DirectGenerator {
	return &DirectGenerator{}
}

// Generate converts execution plan rows to SQL for direct execution.
// Outputs embedded SQL content directly instead of calling helper functions.
// The generated SQL is wrapped in BEGIN/COMMIT for savepoint support.
func (g *DirectGenerator) Generate(rows []testdiscovery.TestScriptRow) *GeneratedSQL {
	return g.GenerateWithCallback(rows, "")
}

// GenerateWithCallback converts execution plan rows to SQL with optional callback invocations.
// When callback is empty, behaves exactly as Generate().
// When callback is specified, emits callback invocations for each test event.
func (g *DirectGenerator) GenerateWithCallback(rows []testdiscovery.TestScriptRow, callback string) *GeneratedSQL {
	result := &GeneratedSQL{
		SourceMap: sourcemap.New(),
	}

	if len(rows) == 0 {
		return result
	}

	var lines []string
	lineNum := 1
	ordinal := 0

	// Begin transaction for savepoint support
	lines = append(lines, "BEGIN;")
	lineNum++

	// Suite start callback
	if callback != "" {
		lines = append(lines, FormatCallbackInvocation(callback, EventSuiteStart, nil, "", 0, 0))
		lineNum++
	}

	for _, row := range rows {
		ordinal++
		startLine := lineNum

		switch row.StepType {
		case testdiscovery.StepTypeFixture:
			// Fixture start callback
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventFixtureStart, row.ScriptPath, row.Directory, row.Depth, ordinal))
				lineNum++
			}

			// PreExec (SAVEPOINT)
			if row.PreExec != nil {
				lines = append(lines, *row.PreExec)
				lineNum++
			}

			// Embedded SQL content
			if row.ScriptSQL != nil {
				content := *row.ScriptSQL
				lines = append(lines, content)
				contentLines := strings.Count(content, "\n") + 1
				if row.ScriptPath != nil {
					desc := fmt.Sprintf("%s: %s", row.StepType, *row.ScriptPath)
					result.SourceMap.Add(lineNum, lineNum+contentLines-1, *row.ScriptPath, 1, desc)
				}
				lineNum += contentLines
			}

			// PostExec
			if row.PostExec != nil {
				lines = append(lines, *row.PostExec)
				lineNum++
			}

			// Fixture end callback
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventFixtureEnd, row.ScriptPath, row.Directory, row.Depth, ordinal))
				lineNum++
			}

		case testdiscovery.StepTypeTest:
			// Test start callback
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventTestStart, row.ScriptPath, row.Directory, row.Depth, ordinal))
				lineNum++
			}

			// PreExec (SAVEPOINT)
			if row.PreExec != nil {
				lines = append(lines, *row.PreExec)
				lineNum++
			}

			// Embedded SQL content
			if row.ScriptSQL != nil {
				content := *row.ScriptSQL
				lines = append(lines, content)
				contentLines := strings.Count(content, "\n") + 1
				if row.ScriptPath != nil {
					desc := fmt.Sprintf("%s: %s", row.StepType, *row.ScriptPath)
					result.SourceMap.Add(lineNum, lineNum+contentLines-1, *row.ScriptPath, 1, desc)
				}
				lineNum += contentLines
			}

			// Test end callback (before rollback)
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventTestEnd, row.ScriptPath, row.Directory, row.Depth, ordinal))
				lineNum++
			}

			// PostExec (ROLLBACK TO)
			if row.PostExec != nil {
				// Rollback callback
				if callback != "" {
					lines = append(lines, FormatCallbackInvocation(callback, EventRollback, row.ScriptPath, row.Directory, row.Depth, ordinal))
					lineNum++
				}
				lines = append(lines, *row.PostExec)
				lineNum++
			}

		case testdiscovery.StepTypeTeardown:
			// Teardown start callback
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventTeardownStart, nil, row.Directory, row.Depth, ordinal))
				lineNum++
			}

			// PreExec (ROLLBACK TO)
			if row.PreExec != nil {
				lines = append(lines, *row.PreExec)
				lineNum++
			}

			// PostExec (RELEASE)
			if row.PostExec != nil {
				lines = append(lines, *row.PostExec)
				lineNum++
			}

			// Source map for teardown
			desc := fmt.Sprintf("%s: %s", row.StepType, row.Directory)
			result.SourceMap.Add(startLine, lineNum-1, row.Directory, 0, desc)

			// Teardown end callback
			if callback != "" {
				lines = append(lines, FormatCallbackInvocation(callback, EventTeardownEnd, nil, row.Directory, row.Depth, ordinal))
				lineNum++
			}

		default:
			// Unknown step type - handle like original code
			if row.PreExec != nil {
				lines = append(lines, *row.PreExec)
				lineNum++
			}
			if row.ScriptSQL != nil {
				content := *row.ScriptSQL
				lines = append(lines, content)
				contentLines := strings.Count(content, "\n") + 1
				if row.ScriptPath != nil {
					desc := fmt.Sprintf("%s: %s", row.StepType, *row.ScriptPath)
					result.SourceMap.Add(lineNum, lineNum+contentLines-1, *row.ScriptPath, 1, desc)
				}
				lineNum += contentLines
			}
			if row.PostExec != nil {
				lines = append(lines, *row.PostExec)
				lineNum++
			}
		}
	}

	// Suite end callback
	if callback != "" {
		lines = append(lines, FormatCallbackInvocation(callback, EventSuiteEnd, nil, "", 0, ordinal))
	}

	// Commit transaction
	lines = append(lines, "COMMIT;")

	result.SQL = strings.Join(lines, "\n")
	return result
}
