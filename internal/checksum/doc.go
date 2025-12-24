// Package checksum provides file content hashing with normalization support.
//
// The package implements pgmi's dual checksum strategy:
//
//   - Raw checksum: Hash of the exact file content (detects all changes)
//   - Normalized checksum: Hash after removing comments and normalizing whitespace
//     (enables rename detection and formatting-independent content identity)
//
// # Normalization Strategy
//
// Normalization makes checksums resilient to formatting changes:
//  1. Convert content to lowercase
//  2. Remove SQL comments (single-line -- and multi-line /* */)
//  3. Collapse all whitespace sequences to single spaces
//  4. Trim leading/trailing whitespace
//
// This allows pgmi to detect when a file's logical content is unchanged
// despite reformatting, and enables rename detection by matching normalized
// checksums across different file paths.
//
// # Example Usage
//
//	calculator := checksum.New()
//	rawChecksum := calculator.CalculateRaw(fileContent)
//	normalizedChecksum := calculator.CalculateNormalized(fileContent)
//
// # Thread Safety
//
// SHA256 is safe for concurrent use by multiple goroutines.
package checksum
