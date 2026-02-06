package sourcemap

import (
	"testing"
)

func TestNew(t *testing.T) {
	sm := New()
	if sm == nil {
		t.Fatal("New() returned nil")
	}
}

func TestSourceMap_Add_Single(t *testing.T) {
	sm := New()
	sm.Add(1, 10, "deploy.sql", 1, "main file")

	file, line, desc, found := sm.Resolve(5)
	if !found {
		t.Fatal("Resolve() returned found=false, expected true")
	}
	if file != "deploy.sql" {
		t.Errorf("file = %q, expected %q", file, "deploy.sql")
	}
	if line != 1 {
		t.Errorf("line = %d, expected %d", line, 1)
	}
	if desc != "main file" {
		t.Errorf("desc = %q, expected %q", desc, "main file")
	}
}

func TestSourceMap_Add_Multiple(t *testing.T) {
	sm := New()
	sm.Add(1, 5, "deploy.sql", 1, "header")
	sm.Add(6, 10, "./test/fixture.sql", 1, "fixture")
	sm.Add(11, 15, "./test/test1.sql", 1, "test 1")
	sm.Add(16, 20, "deploy.sql", 10, "footer")

	tests := []struct {
		queryLine    int
		expectedFile string
		expectedLine int
		expectedDesc string
	}{
		{1, "deploy.sql", 1, "header"},
		{3, "deploy.sql", 1, "header"},
		{5, "deploy.sql", 1, "header"},
		{6, "./test/fixture.sql", 1, "fixture"},
		{8, "./test/fixture.sql", 1, "fixture"},
		{11, "./test/test1.sql", 1, "test 1"},
		{16, "deploy.sql", 10, "footer"},
		{20, "deploy.sql", 10, "footer"},
	}

	for _, tt := range tests {
		file, line, desc, found := sm.Resolve(tt.queryLine)
		if !found {
			t.Errorf("Resolve(%d) returned found=false", tt.queryLine)
			continue
		}
		if file != tt.expectedFile {
			t.Errorf("Resolve(%d) file = %q, expected %q", tt.queryLine, file, tt.expectedFile)
		}
		if line != tt.expectedLine {
			t.Errorf("Resolve(%d) line = %d, expected %d", tt.queryLine, line, tt.expectedLine)
		}
		if desc != tt.expectedDesc {
			t.Errorf("Resolve(%d) desc = %q, expected %q", tt.queryLine, desc, tt.expectedDesc)
		}
	}
}

func TestSourceMap_Resolve_NotFound(t *testing.T) {
	sm := New()
	sm.Add(5, 10, "file.sql", 1, "test")

	tests := []int{0, 1, 4, 11, 100}

	for _, queryLine := range tests {
		_, _, _, found := sm.Resolve(queryLine)
		if found {
			t.Errorf("Resolve(%d) returned found=true, expected false", queryLine)
		}
	}
}

func TestSourceMap_Resolve_EmptyMap(t *testing.T) {
	sm := New()

	_, _, _, found := sm.Resolve(1)
	if found {
		t.Error("Resolve(1) on empty map returned found=true, expected false")
	}
}

func TestSourceMap_Merge_Basic(t *testing.T) {
	sm1 := New()
	sm1.Add(1, 5, "deploy.sql", 1, "before expansion")

	sm2 := New()
	sm2.Add(1, 3, "./test/test1.sql", 1, "test 1")
	sm2.Add(4, 6, "./test/test2.sql", 1, "test 2")

	// Merge sm2 into sm1 with line offset of 5 (expansion starts at line 6)
	sm1.Merge(sm2, 5)

	tests := []struct {
		queryLine    int
		expectedFile string
		expectedDesc string
		shouldFind   bool
	}{
		{1, "deploy.sql", "before expansion", true},
		{5, "deploy.sql", "before expansion", true},
		{6, "./test/test1.sql", "test 1", true},  // 1 + 5
		{8, "./test/test1.sql", "test 1", true},  // 3 + 5
		{9, "./test/test2.sql", "test 2", true},  // 4 + 5
		{11, "./test/test2.sql", "test 2", true}, // 6 + 5
		{12, "", "", false},
	}

	for _, tt := range tests {
		file, _, desc, found := sm1.Resolve(tt.queryLine)
		if found != tt.shouldFind {
			t.Errorf("Resolve(%d) found = %v, expected %v", tt.queryLine, found, tt.shouldFind)
			continue
		}
		if tt.shouldFind {
			if file != tt.expectedFile {
				t.Errorf("Resolve(%d) file = %q, expected %q", tt.queryLine, file, tt.expectedFile)
			}
			if desc != tt.expectedDesc {
				t.Errorf("Resolve(%d) desc = %q, expected %q", tt.queryLine, desc, tt.expectedDesc)
			}
		}
	}
}

func TestSourceMap_Merge_MultipleExpansions(t *testing.T) {
	sm := New()
	sm.Add(1, 5, "deploy.sql", 1, "section 1")

	// First expansion
	exp1 := New()
	exp1.Add(1, 3, "./test/a.sql", 1, "test a")
	sm.Merge(exp1, 5)

	sm.Add(9, 12, "deploy.sql", 10, "section 2")

	// Second expansion
	exp2 := New()
	exp2.Add(1, 2, "./test/b.sql", 1, "test b")
	sm.Merge(exp2, 12)

	sm.Add(15, 20, "deploy.sql", 20, "section 3")

	tests := []struct {
		queryLine    int
		expectedFile string
		shouldFind   bool
	}{
		{3, "deploy.sql", true},
		{6, "./test/a.sql", true},
		{10, "deploy.sql", true},
		{13, "./test/b.sql", true},
		{17, "deploy.sql", true},
	}

	for _, tt := range tests {
		file, _, _, found := sm.Resolve(tt.queryLine)
		if found != tt.shouldFind {
			t.Errorf("Resolve(%d) found = %v, expected %v", tt.queryLine, found, tt.shouldFind)
			continue
		}
		if tt.shouldFind && file != tt.expectedFile {
			t.Errorf("Resolve(%d) file = %q, expected %q", tt.queryLine, file, tt.expectedFile)
		}
	}
}

func TestSourceMap_AddWithLineOffset(t *testing.T) {
	sm := New()

	// Simulating a multi-line test file mapped to expanded lines
	// Test file has 5 lines, starts at expanded line 10
	sm.Add(10, 10, "./test/multi.sql", 1, "line 1")
	sm.Add(11, 11, "./test/multi.sql", 2, "line 2")
	sm.Add(12, 12, "./test/multi.sql", 3, "line 3")
	sm.Add(13, 13, "./test/multi.sql", 4, "line 4")
	sm.Add(14, 14, "./test/multi.sql", 5, "line 5")

	for i := 1; i <= 5; i++ {
		expandedLine := 9 + i
		file, line, _, found := sm.Resolve(expandedLine)
		if !found {
			t.Errorf("Resolve(%d) returned found=false", expandedLine)
			continue
		}
		if file != "./test/multi.sql" {
			t.Errorf("Resolve(%d) file = %q, expected %q", expandedLine, file, "./test/multi.sql")
		}
		if line != i {
			t.Errorf("Resolve(%d) line = %d, expected %d", expandedLine, line, i)
		}
	}
}

func TestSourceMap_Entries(t *testing.T) {
	sm := New()
	sm.Add(1, 5, "file1.sql", 1, "desc1")
	sm.Add(6, 10, "file2.sql", 2, "desc2")

	entries := sm.Entries()
	if len(entries) != 2 {
		t.Fatalf("Entries() returned %d entries, expected 2", len(entries))
	}

	if entries[0].OriginalFile != "file1.sql" {
		t.Errorf("entries[0].OriginalFile = %q, expected %q", entries[0].OriginalFile, "file1.sql")
	}
	if entries[1].OriginalFile != "file2.sql" {
		t.Errorf("entries[1].OriginalFile = %q, expected %q", entries[1].OriginalFile, "file2.sql")
	}
}

func TestSourceMap_Len(t *testing.T) {
	sm := New()
	if sm.Len() != 0 {
		t.Errorf("Len() = %d, expected 0", sm.Len())
	}

	sm.Add(1, 5, "file.sql", 1, "desc")
	if sm.Len() != 1 {
		t.Errorf("Len() = %d, expected 1", sm.Len())
	}

	sm.Add(6, 10, "file2.sql", 1, "desc2")
	if sm.Len() != 2 {
		t.Errorf("Len() = %d, expected 2", sm.Len())
	}
}

func TestSourceMap_RealWorldScenario(t *testing.T) {
	// Simulating:
	// deploy.sql lines 1-5
	// pgmi_test() at line 6 expands to 10 lines of test execution
	// deploy.sql continues at line 16-20

	sm := New()

	// Original deploy.sql before expansion
	sm.Add(1, 5, "deploy.sql", 1, "preamble")

	// Expansion from pgmi_test()
	expansion := New()
	expansion.Add(1, 1, "deploy.sql", 6, "SAVEPOINT")
	expansion.Add(2, 2, "./users/__test__/fixture.sql", 1, "fixture")
	expansion.Add(3, 3, "deploy.sql", 6, "SAVEPOINT after fixture")
	expansion.Add(4, 4, "./users/__test__/test1.sql", 1, "test 1")
	expansion.Add(5, 5, "deploy.sql", 6, "ROLLBACK")
	expansion.Add(6, 6, "./users/__test__/test2.sql", 1, "test 2")
	expansion.Add(7, 7, "deploy.sql", 6, "ROLLBACK")
	expansion.Add(8, 10, "deploy.sql", 6, "cleanup")

	sm.Merge(expansion, 5)

	// Continue with deploy.sql after expansion
	sm.Add(16, 20, "deploy.sql", 7, "postamble")

	// Test resolving various lines
	tests := []struct {
		line         int
		expectedFile string
		expectedDesc string
	}{
		{1, "deploy.sql", "preamble"},
		{5, "deploy.sql", "preamble"},
		{6, "deploy.sql", "SAVEPOINT"},
		{7, "./users/__test__/fixture.sql", "fixture"},
		{9, "./users/__test__/test1.sql", "test 1"},
		{11, "./users/__test__/test2.sql", "test 2"},
		{16, "deploy.sql", "postamble"},
	}

	for _, tt := range tests {
		file, _, desc, found := sm.Resolve(tt.line)
		if !found {
			t.Errorf("Resolve(%d) returned found=false", tt.line)
			continue
		}
		if file != tt.expectedFile {
			t.Errorf("Resolve(%d) file = %q, expected %q", tt.line, file, tt.expectedFile)
		}
		if desc != tt.expectedDesc {
			t.Errorf("Resolve(%d) desc = %q, expected %q", tt.line, desc, tt.expectedDesc)
		}
	}
}

func BenchmarkSourceMap_Resolve(b *testing.B) {
	sm := New()
	for i := 0; i < 100; i++ {
		start := i*10 + 1
		end := start + 9
		sm.Add(start, end, "file.sql", i+1, "section")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm.Resolve(500) // Middle of the map
	}
}

func BenchmarkSourceMap_Add(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm := New()
		for j := 0; j < 100; j++ {
			sm.Add(j*10+1, j*10+10, "file.sql", j+1, "section")
		}
	}
}
