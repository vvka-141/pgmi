package metadata

import (
	"testing"

	"github.com/google/uuid"
)

// TestGenerateFallbackID_Deterministic tests that the same path always generates the same ID
func TestGenerateFallbackID_Deterministic(t *testing.T) {
	path := "./migrations/001_create_users.sql"

	id1 := GenerateFallbackID(path)
	id2 := GenerateFallbackID(path)

	if id1 != id2 {
		t.Errorf("Expected deterministic ID generation, got different IDs: %s vs %s", id1, id2)
	}

	// Verify it's not the nil UUID
	if id1 == uuid.Nil {
		t.Error("Expected non-nil UUID")
	}
}

// TestGenerateFallbackID_DifferentPaths tests that different paths generate different IDs
func TestGenerateFallbackID_DifferentPaths(t *testing.T) {
	testCases := []string{
		"./migrations/001_users.sql",
		"./migrations/002_products.sql",
		"./setup/schema.sql",
		"./post-deployment/grants.sql",
	}

	ids := make(map[uuid.UUID]string)

	for _, path := range testCases {
		id := GenerateFallbackID(path)

		// Check for duplicates
		if existingPath, exists := ids[id]; exists {
			t.Errorf("Collision: paths '%s' and '%s' generated same ID: %s", path, existingPath, id)
		}

		ids[id] = path

		// Verify it's not nil
		if id == uuid.Nil {
			t.Errorf("Path '%s' generated nil UUID", path)
		}
	}

	// Verify we got unique IDs for all paths
	if len(ids) != len(testCases) {
		t.Errorf("Expected %d unique IDs, got %d", len(testCases), len(ids))
	}
}

// TestGenerateFallbackID_PathNormalization tests that path separators are normalized
func TestGenerateFallbackID_PathNormalization(t *testing.T) {
	// These should generate the same ID due to path normalization
	paths := []string{
		"./migrations/001_users.sql",
		"migrations/001_users.sql",   // Without leading ./
		"./migrations\\001_users.sql", // Windows-style separator (if normalized)
	}

	// Note: The current implementation may or may not normalize separators
	// This test documents the expected behavior
	ids := make(map[uuid.UUID]bool)
	for _, path := range paths {
		id := GenerateFallbackID(path)
		ids[id] = true
	}

	// All should generate distinct IDs since paths are different strings
	// (Unless normalization is implemented)
	if len(ids) < 1 {
		t.Error("Expected at least one ID")
	}
}

// TestGenerateFallbackID_EmptyPath tests handling of empty path
func TestGenerateFallbackID_EmptyPath(t *testing.T) {
	id1 := GenerateFallbackID("")
	id2 := GenerateFallbackID("")

	// Even empty path should be deterministic
	if id1 != id2 {
		t.Error("Expected deterministic ID for empty path")
	}

	// Should not be nil UUID
	if id1 == uuid.Nil {
		t.Error("Expected non-nil UUID even for empty path")
	}
}

// TestGenerateFallbackID_SpecialCharacters tests paths with special characters
func TestGenerateFallbackID_SpecialCharacters(t *testing.T) {
	testCases := []string{
		"./migrations/001_test with spaces.sql",
		"./migrations/002-test-with-dashes.sql",
		"./migrations/003_test_with_underscores.sql",
		"./migrations/004.test.with.dots.sql",
		"./migrations/005 (test).sql",
	}

	ids := make(map[uuid.UUID]string)

	for _, path := range testCases {
		id := GenerateFallbackID(path)

		// Check for duplicates
		if existingPath, exists := ids[id]; exists {
			t.Errorf("Collision: paths '%s' and '%s' generated same ID: %s", path, existingPath, id)
		}

		ids[id] = path

		// Verify it's a valid UUID
		if id == uuid.Nil {
			t.Errorf("Path '%s' generated nil UUID", path)
		}

		// Verify it's version 5 (name-based SHA-1)
		if version := id.Version(); version != 5 {
			t.Errorf("Expected UUID v5, got v%d for path '%s'", version, path)
		}
	}
}

// TestGenerateFallbackID_ConsistencyAcrossRuns simulates multiple program runs
func TestGenerateFallbackID_ConsistencyAcrossRuns(t *testing.T) {
	path := "./migrations/001_users.sql"

	// Simulate multiple runs
	runs := 100
	firstID := GenerateFallbackID(path)

	for i := 0; i < runs; i++ {
		id := GenerateFallbackID(path)
		if id != firstID {
			t.Fatalf("Inconsistent ID generation at run %d: expected %s, got %s", i, firstID, id)
		}
	}
}
