// Package testdiscovery handles discovery and organization of test files
// in __test__/ directories for the pgmi test execution system.
//
// It provides functionality to:
// - Traverse source files and identify test directories
// - Detect fixtures vs test files by naming convention
// - Build an ordered execution plan with savepoint structure
// - Support glob pattern filtering for selective test execution
package testdiscovery
