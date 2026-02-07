package preprocessor

import (
	"sort"
	"strings"

	"github.com/vvka-141/pgmi/internal/sourcemap"
	"github.com/vvka-141/pgmi/internal/testdiscovery"
	"github.com/vvka-141/pgmi/internal/testgen"
)

// PreprocessResult contains the result of preprocessing deploy.sql.
type PreprocessResult struct {
	ExpandedSQL string             // SQL with macros expanded
	SourceMap   *sourcemap.SourceMap // Mapping for error attribution
	MacroCount  int                // Number of macros expanded
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

// Process preprocesses SQL by expanding pgmi_test() macros.
// Uses pre-built rows with row-level filtering (legacy interface).
func (p *Pipeline) Process(sql string, rows []testdiscovery.TestScriptRow) (*PreprocessResult, error) {
	result := &PreprocessResult{
		ExpandedSQL: sql,
		SourceMap:   sourcemap.New(),
		MacroCount:  0,
	}

	// Strip comments to find macros
	strippedSQL, _ := p.commentStripper.Strip(sql)

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

		// Filter rows by pattern if specified
		filteredRows := rows
		if macro.Pattern != "" {
			filteredRows = testdiscovery.FilterByPattern(rows, macro.Pattern)
		}

		// Generate expansion using direct execution
		generator := testgen.NewDirectGenerator()
		generated := generator.GenerateWithCallback(filteredRows, macro.Callback)

		// Extract macro text from stripped SQL and find it in expandedSQL
		// Since we process in reverse order, use LastIndex to find the rightmost occurrence
		macroText := strippedSQL[macro.StartPos:macro.EndPos]
		idx := strings.LastIndex(expandedSQL, macroText)
		if idx == -1 {
			continue
		}

		// Replace the macro with generated SQL
		expandedSQL = expandedSQL[:idx] + generated.SQL + expandedSQL[idx+len(macroText):]

		// Merge source map (TODO: adjust line numbers based on insertion point)
		if generated.SourceMap != nil {
			result.SourceMap.Merge(generated.SourceMap, 0)
		}
	}

	result.ExpandedSQL = expandedSQL
	return result, nil
}

// ProcessWithTree preprocesses SQL using tree-level filtering.
// This produces correct sequential savepoints when patterns filter tests.
func (p *Pipeline) ProcessWithTree(sql string, tree *testdiscovery.TestTree, resolver testdiscovery.ContentResolver) (*PreprocessResult, error) {
	result := &PreprocessResult{
		ExpandedSQL: sql,
		SourceMap:   sourcemap.New(),
		MacroCount:  0,
	}

	// Strip comments to find macros
	strippedSQL, _ := p.commentStripper.Strip(sql)

	// Detect macros in stripped SQL
	macros := p.macroDetector.Detect(strippedSQL)
	if len(macros) == 0 {
		return result, nil
	}

	result.MacroCount = len(macros)

	// Process macros in reverse order to maintain correct positions
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

		// Filter tree by pattern
		filteredTree := tree
		if macro.Pattern != "" {
			filteredTree = tree.FilterByPattern(macro.Pattern)
		}

		// Build rows from filtered tree (produces sequential savepoints)
		planBuilder := testdiscovery.NewPlanBuilder(resolver)
		rows, err := planBuilder.Build(filteredTree)
		if err != nil {
			return nil, err
		}

		// Generate expansion using direct execution
		generator := testgen.NewDirectGenerator()
		generated := generator.GenerateWithCallback(rows, macro.Callback)

		// Extract macro text from stripped SQL and find it in expandedSQL
		macroText := strippedSQL[macro.StartPos:macro.EndPos]
		idx := strings.LastIndex(expandedSQL, macroText)
		if idx == -1 {
			continue
		}

		// Replace the macro with generated SQL
		expandedSQL = expandedSQL[:idx] + generated.SQL + expandedSQL[idx+len(macroText):]

		// Merge source map
		if generated.SourceMap != nil {
			result.SourceMap.Merge(generated.SourceMap, 0)
		}
	}

	result.ExpandedSQL = expandedSQL
	return result, nil
}
