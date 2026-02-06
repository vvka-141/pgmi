package testdiscovery

import (
	"fmt"
)

// SavepointNamer generates unique savepoint names.
type SavepointNamer struct {
	counter int
}

// NewSavepointNamer creates a new SavepointNamer.
func NewSavepointNamer() *SavepointNamer {
	return &SavepointNamer{counter: 0}
}

// Next returns the next savepoint name in sequence.
func (n *SavepointNamer) Next() string {
	name := fmt.Sprintf("__pgmi_%d__", n.counter)
	n.counter++
	return name
}

// PlanBuilder converts a TestTree to an ordered execution plan.
type PlanBuilder struct{}

// NewPlanBuilder creates a new PlanBuilder.
func NewPlanBuilder() *PlanBuilder {
	return &PlanBuilder{}
}

// Build converts a TestTree to an ordered list of TestScriptRows.
func (b *PlanBuilder) Build(tree *TestTree) []TestScriptRow {
	if tree == nil || tree.IsEmpty() {
		return nil
	}

	var rows []TestScriptRow
	sortKey := 1
	namer := NewSavepointNamer()

	// Process each top-level directory
	for _, dir := range tree.Directories {
		rows, sortKey = b.processDirectory(dir, rows, sortKey, namer)
	}

	return rows
}

// processDirectory recursively processes a directory and its children.
// Returns updated rows slice and next sortKey.
func (b *PlanBuilder) processDirectory(dir *TestDirectory, rows []TestScriptRow, sortKey int, namer *SavepointNamer) ([]TestScriptRow, int) {
	// Create entry savepoint for this directory
	entrySavepoint := namer.Next()
	rows = append(rows, TestScriptRow{
		SortKey:    sortKey,
		ScriptType: "savepoint",
		BeforeExec: Ptr(fmt.Sprintf("SAVEPOINT %s;", entrySavepoint)),
		Directory:  dir.Path,
		Depth:      dir.Depth,
	})
	sortKey++

	// Savepoint to rollback to after each test
	testRollbackSavepoint := entrySavepoint

	// Execute fixture if present
	if dir.HasFixture() {
		rows = append(rows, TestScriptRow{
			SortKey:    sortKey,
			Path:       Ptr(dir.Fixture.Path),
			ScriptType: "fixture",
			Directory:  dir.Path,
			Depth:      dir.Depth,
		})
		sortKey++

		// Create savepoint after fixture for test rollbacks
		testRollbackSavepoint = namer.Next()
		rows = append(rows, TestScriptRow{
			SortKey:    sortKey,
			ScriptType: "savepoint",
			BeforeExec: Ptr(fmt.Sprintf("SAVEPOINT %s;", testRollbackSavepoint)),
			Directory:  dir.Path,
			Depth:      dir.Depth,
		})
		sortKey++
	}

	// Execute each test, rolling back after each
	for _, test := range dir.Tests {
		rows = append(rows, TestScriptRow{
			SortKey:    sortKey,
			Path:       Ptr(test.Path),
			ScriptType: "test",
			AfterExec:  Ptr(fmt.Sprintf("ROLLBACK TO SAVEPOINT %s;", testRollbackSavepoint)),
			Directory:  dir.Path,
			Depth:      dir.Depth,
		})
		sortKey++
	}

	// Process child directories
	for _, child := range dir.Children {
		rows, sortKey = b.processDirectory(child, rows, sortKey, namer)
	}

	// Cleanup: rollback to entry savepoint and release
	rows = append(rows, TestScriptRow{
		SortKey:    sortKey,
		ScriptType: "cleanup",
		BeforeExec: Ptr(fmt.Sprintf("ROLLBACK TO SAVEPOINT %s;", entrySavepoint)),
		AfterExec:  Ptr(fmt.Sprintf("RELEASE SAVEPOINT %s;", entrySavepoint)),
		Directory:  dir.Path,
		Depth:      dir.Depth,
	})
	sortKey++

	return rows, sortKey
}
