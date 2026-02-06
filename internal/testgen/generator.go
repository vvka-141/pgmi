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
func (g *DirectGenerator) Generate(rows []testdiscovery.TestScriptRow) *GeneratedSQL {
	result := &GeneratedSQL{
		SourceMap: sourcemap.New(),
	}

	if len(rows) == 0 {
		return result
	}

	var lines []string
	lineNum := 1

	for _, row := range rows {
		startLine := lineNum

		// BeforeExec (SAVEPOINT, ROLLBACK TO)
		if row.BeforeExec != nil {
			lines = append(lines, *row.BeforeExec)
			lineNum++
		}

		// File execution (fixture or test)
		if row.Path != nil {
			escapedPath := EscapeSQLString(*row.Path)
			execLine := fmt.Sprintf("SELECT pg_temp.pgmi_execute_test_file('%s');", escapedPath)
			lines = append(lines, execLine)

			// Add source map entry for this line
			desc := fmt.Sprintf("%s: %s", row.ScriptType, *row.Path)
			result.SourceMap.Add(lineNum, lineNum, *row.Path, 1, desc)
			lineNum++
		}

		// AfterExec (ROLLBACK TO, RELEASE)
		if row.AfterExec != nil {
			lines = append(lines, *row.AfterExec)
			lineNum++
		}

		// Add source map entry for control rows (savepoint, cleanup) if no file
		if row.Path == nil && (row.BeforeExec != nil || row.AfterExec != nil) {
			desc := fmt.Sprintf("%s: %s", row.ScriptType, row.Directory)
			result.SourceMap.Add(startLine, lineNum-1, row.Directory, 0, desc)
		}
	}

	result.SQL = strings.Join(lines, "\n")
	return result
}
