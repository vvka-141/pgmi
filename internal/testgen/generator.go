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

		// PreExec (SAVEPOINT)
		if row.PreExec != nil {
			lines = append(lines, *row.PreExec)
			lineNum++
		}

		// Embedded SQL content (fixture or test)
		if row.ScriptSQL != nil {
			// Count lines in embedded content for source map
			content := *row.ScriptSQL
			lines = append(lines, content)
			contentLines := strings.Count(content, "\n") + 1

			// Add source map entry
			if row.ScriptPath != nil {
				desc := fmt.Sprintf("%s: %s", row.StepType, *row.ScriptPath)
				result.SourceMap.Add(lineNum, lineNum+contentLines-1, *row.ScriptPath, 1, desc)
			}
			lineNum += contentLines
		}

		// PostExec (ROLLBACK TO, RELEASE)
		if row.PostExec != nil {
			lines = append(lines, *row.PostExec)
			lineNum++
		}

		// Add source map entry for teardown rows (no file content)
		if row.ScriptSQL == nil && (row.PreExec != nil || row.PostExec != nil) {
			desc := fmt.Sprintf("%s: %s", row.StepType, row.Directory)
			result.SourceMap.Add(startLine, lineNum-1, row.Directory, 0, desc)
		}
	}

	result.SQL = strings.Join(lines, "\n")
	return result
}
