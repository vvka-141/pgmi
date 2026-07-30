package metadata

import (
	"crypto/md5"
	"testing"

	"github.com/google/uuid"
)

func TestGenerateFallbackID_Deterministic(t *testing.T) {
	path := "./migrations/001_create_users.sql"

	id1 := GenerateFallbackID(path)
	id2 := GenerateFallbackID(path)

	if id1 != id2 {
		t.Errorf("Expected deterministic ID generation, got different IDs: %s vs %s", id1, id2)
	}

	if id1 == uuid.Nil {
		t.Error("Expected non-nil UUID")
	}
}

func TestGenerateFallbackID_MatchesMD5(t *testing.T) {
	paths := []string{
		"./migrations/001_users.sql",
		"./migrations/002_products.sql",
		"./setup/schema.sql",
		"",
	}

	for _, path := range paths {
		got := GenerateFallbackID(path)
		h := md5.Sum([]byte(path))
		want := uuid.UUID(h)
		if got != want {
			t.Errorf("GenerateFallbackID(%q) = %s, want %s (raw md5-as-uuid)", path, got, want)
		}
	}
}

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
		if existingPath, exists := ids[id]; exists {
			t.Errorf("Collision: paths %q and %q generated same ID: %s", path, existingPath, id)
		}
		ids[id] = path
	}
}

func TestGenerateFallbackID_CaseSensitive(t *testing.T) {
	lower := GenerateFallbackID("./migrations/001_users.sql")
	upper := GenerateFallbackID("./migrations/001_Users.sql")

	if lower == upper {
		t.Error("Expected case-sensitive: different cases should produce different IDs")
	}
}

func TestGenerateFallbackID_PrefixMatters(t *testing.T) {
	withPrefix := GenerateFallbackID("./migrations/001_users.sql")
	withoutPrefix := GenerateFallbackID("migrations/001_users.sql")

	if withPrefix == withoutPrefix {
		t.Error("Expected ./ prefix to matter: with and without should produce different IDs")
	}
}

func TestGenerateFallbackID_EmptyPath(t *testing.T) {
	id1 := GenerateFallbackID("")
	id2 := GenerateFallbackID("")

	if id1 != id2 {
		t.Error("Expected deterministic ID for empty path")
	}

	if id1 == uuid.Nil {
		t.Error("Expected non-nil UUID even for empty path")
	}
}

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
		if existingPath, exists := ids[id]; exists {
			t.Errorf("Collision: paths %q and %q generated same ID: %s", path, existingPath, id)
		}
		ids[id] = path
		if id == uuid.Nil {
			t.Errorf("Path %q generated nil UUID", path)
		}
	}
}

func TestGenerateFallbackID_ConsistencyAcrossRuns(t *testing.T) {
	path := "./migrations/001_users.sql"
	firstID := GenerateFallbackID(path)

	for i := 0; i < 100; i++ {
		id := GenerateFallbackID(path)
		if id != firstID {
			t.Fatalf("Inconsistent ID generation at run %d: expected %s, got %s", i, firstID, id)
		}
	}
}
