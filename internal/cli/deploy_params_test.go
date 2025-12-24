package cli

import (
	"testing"

	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/stretchr/testify/require"
)

// TestLoadParamsFromFiles tests the params file loading with filesystem abstraction
func TestLoadParamsFromFiles(t *testing.T) {
	tests := []struct {
		name        string
		files       map[string]string // filename -> content
		paramsFiles []string          // ordered list of files to load
		verbose     bool
		expected    map[string]string
		expectError bool
		errorMsg    string
	}{
		{
			name: "Single params file",
			files: map[string]string{
				"/test/prod.env": `DB_HOST=localhost
DB_PORT=5432
DB_NAME=production`,
			},
			paramsFiles: []string{"/test/prod.env"},
			verbose:     false,
			expected: map[string]string{
				"DB_HOST": "localhost",
				"DB_PORT": "5432",
				"DB_NAME": "production",
			},
		},
		{
			name: "Multiple params files - later overrides earlier",
			files: map[string]string{
				"/test/base.env": `ENV=development
DB_HOST=localhost
DB_PORT=5432`,
				"/test/prod.env": `ENV=production
DB_HOST=prod.example.com`,
			},
			paramsFiles: []string{"/test/base.env", "/test/prod.env"},
			verbose:     false,
			expected: map[string]string{
				"ENV":     "production", // overridden by prod.env
				"DB_HOST": "prod.example.com", // overridden by prod.env
				"DB_PORT": "5432", // from base.env
			},
		},
		{
			name: "Three-layer override",
			files: map[string]string{
				"/test/base.env": `A=1
B=2
C=3`,
				"/test/staging.env": `B=20
C=30`,
				"/test/prod.env": `C=300`,
			},
			paramsFiles: []string{"/test/base.env", "/test/staging.env", "/test/prod.env"},
			verbose:     false,
			expected: map[string]string{
				"A": "1",   // from base
				"B": "20",  // from staging
				"C": "300", // from prod
			},
		},
		{
			name: "File with comments and empty lines",
			files: map[string]string{
				"/test/config.env": `# Database config
DB_HOST=localhost

# Port configuration
DB_PORT=5432`,
			},
			paramsFiles: []string{"/test/config.env"},
			verbose:     false,
			expected: map[string]string{
				"DB_HOST": "localhost",
				"DB_PORT": "5432",
			},
		},
		{
			name: "File with quoted values",
			files: map[string]string{
				"/test/secrets.env": `API_KEY="sk-1234567890"
DB_PASS='my$ecret!pass'`,
			},
			paramsFiles: []string{"/test/secrets.env"},
			verbose:     false,
			expected: map[string]string{
				"API_KEY": "sk-1234567890",
				"DB_PASS": "my$ecret!pass",
			},
		},
		{
			name: "Empty params file",
			files: map[string]string{
				"/test/empty.env": ``,
			},
			paramsFiles: []string{"/test/empty.env"},
			verbose:     false,
			expected:    map[string]string{},
		},
		{
			name: "File not found error",
			files: map[string]string{
				"/test/existing.env": `KEY=value`,
			},
			paramsFiles: []string{"/test/nonexistent.env"},
			verbose:     false,
			expectError: true,
			errorMsg:    "failed to read params file",
		},
		{
			name: "Malformed params file - missing equals",
			files: map[string]string{
				"/test/bad.env": `INVALID_LINE_WITHOUT_EQUALS`,
			},
			paramsFiles: []string{"/test/bad.env"},
			verbose:     false,
			expectError: true,
			errorMsg:    "failed to parse params file",
		},
		{
			name: "Malformed params file - empty key",
			files: map[string]string{
				"/test/bad.env": `=value_without_key`,
			},
			paramsFiles: []string{"/test/bad.env"},
			verbose:     false,
			expectError: true,
			errorMsg:    "failed to parse params file",
		},
		{
			name: "Complex real-world scenario",
			files: map[string]string{
				"/test/base.env": `# Base configuration
APP_NAME=pgmi
ENV=development
DB_HOST=localhost
DB_PORT=5432
DB_USER=postgres`,
				"/test/staging.env": `# Staging overrides
ENV=staging
DB_HOST=staging.db.example.com
DB_NAME=pgmi_staging`,
				"/test/prod.env": `# Production overrides
ENV=production
DB_HOST=prod.db.example.com
DB_NAME=pgmi_prod
DB_SSL=require`,
			},
			paramsFiles: []string{"/test/base.env", "/test/staging.env", "/test/prod.env"},
			verbose:     false,
			expected: map[string]string{
				"APP_NAME": "pgmi",                // from base
				"ENV":      "production",              // overridden by prod
				"DB_HOST":  "prod.db.example.com",     // overridden by prod
				"DB_PORT":  "5432",                    // from base
				"DB_USER":  "postgres",                // from base
				"DB_NAME":  "pgmi_prod",           // overridden by prod
				"DB_SSL":   "require",                 // from prod
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create in-memory filesystem
			mfs := filesystem.NewMemoryFileSystem("/")

			// Add all files to the filesystem
			for path, content := range tt.files {
				mfs.AddFile(path, content)
			}

			// Call the function under test
			result, err := loadParamsFromFiles(mfs, tt.paramsFiles, tt.verbose)

			if tt.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errorMsg)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
		})
	}
}

// TestLoadParamsFromFiles_Verbose tests verbose output behavior
func TestLoadParamsFromFiles_Verbose(t *testing.T) {
	mfs := filesystem.NewMemoryFileSystem("/")
	mfs.AddFile("/test/config.env", "KEY=value")

	// Test that verbose mode doesn't cause errors
	// (We can't easily capture stdout in this test, but we verify it doesn't error)
	result, err := loadParamsFromFiles(mfs, []string{"/test/config.env"}, true)

	require.NoError(t, err)
	require.Equal(t, map[string]string{"KEY": "value"}, result)
}

// TestLoadParamsFromFiles_EmptyList tests behavior with no params files
func TestLoadParamsFromFiles_EmptyList(t *testing.T) {
	mfs := filesystem.NewMemoryFileSystem("/")

	result, err := loadParamsFromFiles(mfs, []string{}, false)

	require.NoError(t, err)
	require.Empty(t, result)
}
