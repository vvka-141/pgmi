package testgen

import (
	"fmt"
	"strings"

	"github.com/vvka-141/pgmi/internal/sourcemap"
	"github.com/vvka-141/pgmi/internal/testdiscovery"
)

// PlanModeGenerator produces PERFORM pgmi_plan_command() calls for planning mode.
type PlanModeGenerator struct{}

// NewPlanModeGenerator creates a new PlanModeGenerator.
func NewPlanModeGenerator() *PlanModeGenerator {
	return &PlanModeGenerator{}
}

// Generate converts execution plan rows to PL/pgSQL PERFORM statements.
func (g *PlanModeGenerator) Generate(rows []testdiscovery.TestScriptRow) *GeneratedSQL {
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
			lines = append(lines, g.wrapCommand(*row.BeforeExec))
			lineNum++
		}

		// File execution (fixture or test)
		if row.Path != nil {
			// Build the SELECT statement
			execCmd := fmt.Sprintf("SELECT pg_temp.pgmi_execute_test_file('%s');", *row.Path)
			lines = append(lines, g.wrapCommandDollar(execCmd))

			// Add source map entry
			desc := fmt.Sprintf("%s: %s", row.ScriptType, *row.Path)
			result.SourceMap.Add(lineNum, lineNum, *row.Path, 1, desc)
			lineNum++
		}

		// AfterExec (ROLLBACK TO, RELEASE)
		if row.AfterExec != nil {
			lines = append(lines, g.wrapCommand(*row.AfterExec))
			lineNum++
		}

		// Add source map entry for control rows if no file
		if row.Path == nil && (row.BeforeExec != nil || row.AfterExec != nil) {
			desc := fmt.Sprintf("%s: %s", row.ScriptType, row.Directory)
			result.SourceMap.Add(startLine, lineNum-1, row.Directory, 0, desc)
		}
	}

	result.SQL = strings.Join(lines, "\n")
	return result
}

// wrapCommand wraps a simple SQL command in pgmi_plan_command with single quotes.
// Use for commands that don't contain single quotes (savepoint, rollback).
func (g *PlanModeGenerator) wrapCommand(cmd string) string {
	// Escape any single quotes in the command
	escaped := EscapeSQLString(cmd)
	return fmt.Sprintf("PERFORM pg_temp.pgmi_plan_command('%s');", escaped)
}

// wrapCommandDollar wraps a SQL command using dollar quoting.
// Use for commands that may contain single quotes (file executions with paths).
func (g *PlanModeGenerator) wrapCommandDollar(cmd string) string {
	return fmt.Sprintf("PERFORM pg_temp.pgmi_plan_command($__pgmi__$%s$__pgmi__$);", cmd)
}
