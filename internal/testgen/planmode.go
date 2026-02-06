package testgen

import (
	"fmt"
	"strings"

	"github.com/vvka-141/pgmi/internal/sourcemap"
	"github.com/vvka-141/pgmi/internal/testdiscovery"
)

// PlanModeGenerator produces a single PERFORM pgmi_plan_command() call
// containing the entire test execution sequence.
type PlanModeGenerator struct{}

// NewPlanModeGenerator creates a new PlanModeGenerator.
func NewPlanModeGenerator() *PlanModeGenerator {
	return &PlanModeGenerator{}
}

// Generate converts execution plan rows to a single pgmi_plan_command() call.
func (g *PlanModeGenerator) Generate(rows []testdiscovery.TestScriptRow) *GeneratedSQL {
	result := &GeneratedSQL{
		SourceMap: sourcemap.New(),
	}

	if len(rows) == 0 {
		return result
	}

	var innerLines []string
	// Line 1 is "PERFORM pg_temp.pgmi_plan_command($__pgmi__$"
	// Inner SQL starts at line 2
	lineNum := 2

	for _, row := range rows {
		startLine := lineNum

		if row.BeforeExec != nil {
			innerLines = append(innerLines, *row.BeforeExec)
			lineNum++
		}

		if row.Path != nil {
			escapedPath := EscapeSQLString(*row.Path)
			execLine := fmt.Sprintf("SELECT pg_temp.pgmi_execute_test_file('%s');", escapedPath)
			innerLines = append(innerLines, execLine)

			desc := fmt.Sprintf("%s: %s", row.ScriptType, *row.Path)
			result.SourceMap.Add(lineNum, lineNum, *row.Path, 1, desc)
			lineNum++
		}

		if row.AfterExec != nil {
			innerLines = append(innerLines, *row.AfterExec)
			lineNum++
		}

		if row.Path == nil && (row.BeforeExec != nil || row.AfterExec != nil) {
			desc := fmt.Sprintf("%s: %s", row.ScriptType, row.Directory)
			result.SourceMap.Add(startLine, lineNum-1, row.Directory, 0, desc)
		}
	}

	innerSQL := strings.Join(innerLines, "\n")
	result.SQL = fmt.Sprintf("PERFORM pg_temp.pgmi_plan_command($__pgmi__$\n%s\n$__pgmi__$);", innerSQL)

	return result
}
