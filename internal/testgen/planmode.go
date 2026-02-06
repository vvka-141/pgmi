package testgen

import (
	"fmt"
	"strings"

	"github.com/vvka-141/pgmi/internal/sourcemap"
	"github.com/vvka-141/pgmi/internal/testdiscovery"
)

// PlanModeGenerator produces a single PERFORM pgmi_plan_command() call
// containing the entire test execution sequence with embedded content.
type PlanModeGenerator struct{}

// NewPlanModeGenerator creates a new PlanModeGenerator.
func NewPlanModeGenerator() *PlanModeGenerator {
	return &PlanModeGenerator{}
}

// Generate converts execution plan rows to a single pgmi_plan_command() call.
// Outputs embedded SQL content directly instead of calling helper functions.
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

		if row.PreExec != nil {
			innerLines = append(innerLines, *row.PreExec)
			lineNum++
		}

		if row.ScriptSQL != nil {
			content := *row.ScriptSQL
			innerLines = append(innerLines, content)
			contentLines := strings.Count(content, "\n") + 1

			if row.ScriptPath != nil {
				desc := fmt.Sprintf("%s: %s", row.StepType, *row.ScriptPath)
				result.SourceMap.Add(lineNum, lineNum+contentLines-1, *row.ScriptPath, 1, desc)
			}
			lineNum += contentLines
		}

		if row.PostExec != nil {
			innerLines = append(innerLines, *row.PostExec)
			lineNum++
		}

		if row.ScriptSQL == nil && (row.PreExec != nil || row.PostExec != nil) {
			desc := fmt.Sprintf("%s: %s", row.StepType, row.Directory)
			result.SourceMap.Add(startLine, lineNum-1, row.Directory, 0, desc)
		}
	}

	innerSQL := strings.Join(innerLines, "\n")
	result.SQL = fmt.Sprintf("PERFORM pg_temp.pgmi_plan_command($__pgmi__$\n%s\n$__pgmi__$);", innerSQL)

	return result
}
