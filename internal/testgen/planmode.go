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

// Generate converts execution plan rows to pgmi_plan_command() calls.
// Outputs embedded SQL content directly instead of calling helper functions.
// The generated SQL schedules BEGIN, test execution, and COMMIT as separate commands.
func (g *PlanModeGenerator) Generate(rows []testdiscovery.TestScriptRow) *GeneratedSQL {
	result := &GeneratedSQL{
		SourceMap: sourcemap.New(),
	}

	if len(rows) == 0 {
		return result
	}

	var allLines []string
	// Line 1 is BEGIN command
	lineNum := 1

	// Schedule BEGIN for savepoint support
	allLines = append(allLines, "PERFORM pg_temp.pgmi_plan_command('BEGIN;');")
	lineNum++

	var innerLines []string
	// Inner SQL line tracking starts after BEGIN command
	innerStartLine := lineNum + 1 // +1 for "PERFORM pg_temp.pgmi_plan_command($__pgmi__$"

	for _, row := range rows {
		startLine := innerStartLine + len(innerLines)

		if row.PreExec != nil {
			innerLines = append(innerLines, *row.PreExec)
		}

		if row.ScriptSQL != nil {
			content := *row.ScriptSQL
			innerLines = append(innerLines, content)
			contentLines := strings.Count(content, "\n") + 1

			if row.ScriptPath != nil {
				desc := fmt.Sprintf("%s: %s", row.StepType, *row.ScriptPath)
				result.SourceMap.Add(startLine+len(innerLines)-1, startLine+len(innerLines)-1+contentLines-1, *row.ScriptPath, 1, desc)
			}
		}

		if row.PostExec != nil {
			innerLines = append(innerLines, *row.PostExec)
		}

		if row.ScriptSQL == nil && (row.PreExec != nil || row.PostExec != nil) {
			desc := fmt.Sprintf("%s: %s", row.StepType, row.Directory)
			endLine := startLine + 1
			if row.PreExec != nil {
				endLine++
			}
			if row.PostExec != nil {
				endLine++
			}
			result.SourceMap.Add(startLine, endLine-1, row.Directory, 0, desc)
		}
	}

	// Schedule test execution as single command
	innerSQL := strings.Join(innerLines, "\n")
	allLines = append(allLines, fmt.Sprintf("PERFORM pg_temp.pgmi_plan_command($__pgmi__$\n%s\n$__pgmi__$);", innerSQL))

	// Schedule COMMIT
	allLines = append(allLines, "PERFORM pg_temp.pgmi_plan_command('COMMIT;');")

	result.SQL = strings.Join(allLines, "\n")
	return result
}
