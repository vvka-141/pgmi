package checksum

import (
	"strings"
	"testing"
)

// BenchmarkCalculateRaw benchmarks raw checksum calculation
func BenchmarkCalculateRaw(b *testing.B) {
	calculator := New()
	content := []byte(strings.Repeat("SELECT * FROM users WHERE id = 1;\n", 100))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calculator.CalculateRaw(content)
	}
}

// BenchmarkCalculateNormalized benchmarks normalized checksum calculation
func BenchmarkCalculateNormalized(b *testing.B) {
	calculator := New()
	content := []byte(strings.Repeat("SELECT * FROM users; -- comment\n", 100))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calculator.CalculateNormalized(content)
	}
}

// BenchmarkCalculateNormalizedLargeFile benchmarks normalization of large SQL files
func BenchmarkCalculateNormalizedLargeFile(b *testing.B) {
	calculator := New()
	// Simulate a large SQL file with mixed content
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		sb.WriteString("-- This is a comment\n")
		sb.WriteString("SELECT * FROM users WHERE id = ")
		sb.WriteString(strings.Repeat("1", 10))
		sb.WriteString(";\n")
		sb.WriteString("/* Multi-line\n   comment */\n")
		sb.WriteString("INSERT INTO logs (message) VALUES ('test message');\n\n")
	}
	content := []byte(sb.String())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calculator.CalculateNormalized(content)
	}
}

// BenchmarkNormalize benchmarks just the normalization step
func BenchmarkNormalize(b *testing.B) {
	calculator := New()
	content := strings.Repeat("SELECT * FROM users; -- comment\n", 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calculator.normalize(content)
	}
}

// BenchmarkRemoveComments benchmarks comment removal
func BenchmarkRemoveComments(b *testing.B) {
	calculator := New()
	content := strings.Repeat("SELECT * FROM users; -- comment\n/* multi\nline */\n", 50)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calculator.removeComments(content)
	}
}
