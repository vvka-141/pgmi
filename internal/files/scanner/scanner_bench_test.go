package scanner

import (
	"os"
	"testing"

	"github.com/vvka-141/pgmi/internal/checksum"
)

// BenchmarkScanDirectory benchmarks directory scanning with real filesystem
func BenchmarkScanDirectory(b *testing.B) {
	// Create temporary directory structure for benchmarking
	tempDir := b.TempDir()

	// Create test files
	for i := 0; i < 10; i++ {
		filename := tempDir + string(os.PathSeparator) + "test" + string(rune('0'+i)) + ".sql"
		content := "SELECT * FROM users WHERE id = 1;\n-- Comment\n/* Multi-line */\nINSERT INTO logs VALUES ('test');\n"
		if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
			b.Fatal(err)
		}
	}

	calculator := checksum.New()
	fileScanner := NewScanner(calculator)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := fileScanner.ScanDirectory(tempDir)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// NOTE: In-memory filesystem benchmarking removed as NewMemoryFileSystemProvider
// is not exported. Real filesystem benchmarking via BenchmarkScanDirectory is sufficient.

// BenchmarkExtractPlaceholders benchmarks placeholder extraction
