package params

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseEnvFile(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		expected    map[string]string
		expectError bool
		errorMsg    string
	}{
		{
			name: "Simple key-value pairs",
			content: `KEY1=value1
KEY2=value2
KEY3=value3`,
			expected: map[string]string{
				"KEY1": "value1",
				"KEY2": "value2",
				"KEY3": "value3",
			},
		},
		{
			name: "Values with spaces",
			content: `NAME=John Doe
ADDRESS=123 Main Street`,
			expected: map[string]string{
				"NAME":    "John Doe",
				"ADDRESS": "123 Main Street",
			},
		},
		{
			name: "Double quoted values",
			content: `NAME="John Doe"
PATH="/usr/local/bin"`,
			expected: map[string]string{
				"NAME": "John Doe",
				"PATH": "/usr/local/bin",
			},
		},
		{
			name: "Single quoted values",
			content: `NAME='John Doe'
PATH='/usr/local/bin'`,
			expected: map[string]string{
				"NAME": "John Doe",
				"PATH": "/usr/local/bin",
			},
		},
		{
			name: "Comments and empty lines",
			content: `# This is a comment
KEY1=value1

# Another comment
KEY2=value2

`,
			expected: map[string]string{
				"KEY1": "value1",
				"KEY2": "value2",
			},
		},
		{
			name: "Whitespace around equals",
			content: `KEY1 = value1
KEY2= value2
KEY3 =value3
KEY4  =  value4`,
			expected: map[string]string{
				"KEY1": "value1",
				"KEY2": "value2",
				"KEY3": "value3",
				"KEY4": "value4",
			},
		},
		{
			name: "Empty values",
			content: `KEY1=
KEY2=""
KEY3=''`,
			expected: map[string]string{
				"KEY1": "",
				"KEY2": "",
				"KEY3": "",
			},
		},
		{
			name: "Values with equals sign",
			content: `CONN=host=localhost port=5432
URL=https://example.com?foo=bar`,
			expected: map[string]string{
				"CONN": "host=localhost port=5432",
				"URL":  "https://example.com?foo=bar",
			},
		},
		{
			name:        "Invalid format - no equals",
			content:     `INVALID_LINE`,
			expectError: true,
			errorMsg:    "invalid format",
		},
		{
			name:        "Invalid format - empty key",
			content:     `=value`,
			expectError: true,
			errorMsg:    "empty key",
		},
		{
			name: "Complex real-world example",
			content: `# Database Configuration
DB_HOST=localhost
DB_PORT=5432
DB_NAME=myapp_production
DB_USER=admin

# API Configuration
API_KEY="sk-1234567890abcdef"
API_URL='https://api.example.com/v1'

# Feature Flags
ENABLE_CACHE=true
MAX_CONNECTIONS=100`,
			expected: map[string]string{
				"DB_HOST":          "localhost",
				"DB_PORT":          "5432",
				"DB_NAME":          "myapp_production",
				"DB_USER":          "admin",
				"API_KEY":          "sk-1234567890abcdef",
				"API_URL":          "https://api.example.com/v1",
				"ENABLE_CACHE":     "true",
				"MAX_CONNECTIONS":  "100",
			},
		},
		{
			name:     "Empty file",
			content:  "",
			expected: map[string]string{},
		},
		{
			name: "Only comments",
			content: `# Comment 1
# Comment 2
# Comment 3`,
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseEnvFile([]byte(tt.content))

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
