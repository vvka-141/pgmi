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

// ContentResolver retrieves file content by path.
type ContentResolver func(path string) (string, error)

// PlanBuilder converts a TestTree to an ordered execution plan.
type PlanBuilder struct {
	contentResolver ContentResolver
}

// NewPlanBuilder creates a new PlanBuilder with a content resolver.
func NewPlanBuilder(resolver ContentResolver) *PlanBuilder {
	return &PlanBuilder{contentResolver: resolver}
}

// Build converts a TestTree to an ordered list of TestScriptRows.
func (b *PlanBuilder) Build(tree *TestTree) ([]TestScriptRow, error) {
	if tree == nil || tree.IsEmpty() {
		return nil, nil
	}

	var rows []TestScriptRow
	ordinal := 1
	namer := NewSavepointNamer()

	// Process each top-level directory
	var err error
	for _, dir := range tree.Directories {
		rows, ordinal, err = b.processDirectory(dir, rows, ordinal, namer)
		if err != nil {
			return nil, err
		}
	}

	return rows, nil
}

// processDirectory recursively processes a directory and its children.
// Returns updated rows slice, next ordinal, and any error.
func (b *PlanBuilder) processDirectory(dir *TestDirectory, rows []TestScriptRow, ordinal int, namer *SavepointNamer) ([]TestScriptRow, int, error) {
	// Create entry savepoint for this directory
	entrySavepoint := namer.Next()

	// Savepoint to rollback to after each test (initially the entry savepoint)
	testRollbackSavepoint := entrySavepoint

	// Execute fixture if present
	if dir.HasFixture() {
		content, err := b.contentResolver(dir.Fixture.Path)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to resolve fixture %s: %w", dir.Fixture.Path, err)
		}

		rows = append(rows, TestScriptRow{
			Ordinal:    ordinal,
			StepType:   "fixture",
			ScriptPath: Ptr(dir.Fixture.Path),
			Directory:  dir.Path,
			Depth:      dir.Depth,
			PreExec:    Ptr(fmt.Sprintf("SAVEPOINT %s;", entrySavepoint)),
			ScriptSQL:  Ptr(content),
		})
		ordinal++

		// Create savepoint after fixture for test rollbacks
		testRollbackSavepoint = namer.Next()
	}

	// Execute each test, rolling back after each
	for _, test := range dir.Tests {
		content, err := b.contentResolver(test.Path)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to resolve test %s: %w", test.Path, err)
		}

		// Pre-exec: SAVEPOINT only for first test if no fixture (use entry savepoint)
		var preExec *string
		if !dir.HasFixture() && test == dir.Tests[0] {
			preExec = Ptr(fmt.Sprintf("SAVEPOINT %s;", entrySavepoint))
		} else if dir.HasFixture() && test == dir.Tests[0] {
			preExec = Ptr(fmt.Sprintf("SAVEPOINT %s;", testRollbackSavepoint))
		}

		rows = append(rows, TestScriptRow{
			Ordinal:    ordinal,
			StepType:   "test",
			ScriptPath: Ptr(test.Path),
			Directory:  dir.Path,
			Depth:      dir.Depth,
			PreExec:    preExec,
			ScriptSQL:  Ptr(content),
			PostExec:   Ptr(fmt.Sprintf("ROLLBACK TO SAVEPOINT %s;", testRollbackSavepoint)),
		})
		ordinal++
	}

	// Process child directories
	var err error
	for _, child := range dir.Children {
		rows, ordinal, err = b.processDirectory(child, rows, ordinal, namer)
		if err != nil {
			return nil, 0, err
		}
	}

	// Teardown: rollback to entry savepoint and release
	rows = append(rows, TestScriptRow{
		Ordinal:   ordinal,
		StepType:  "teardown",
		Directory: dir.Path,
		Depth:     dir.Depth,
		PreExec:   Ptr(fmt.Sprintf("ROLLBACK TO SAVEPOINT %s;", entrySavepoint)),
		PostExec:  Ptr(fmt.Sprintf("RELEASE SAVEPOINT %s;", entrySavepoint)),
	})
	ordinal++

	return rows, ordinal, nil
}
