package params

import (
	"strings"
	"testing"
)

func TestParseKeyValuePairs(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    map[string]string
		wantErr string
	}{
		{
			name:  "single pair",
			input: []string{"env=production"},
			want:  map[string]string{"env": "production"},
		},
		{
			name:  "multiple pairs",
			input: []string{"env=prod", "db=myapp", "port=5432"},
			want:  map[string]string{"env": "prod", "db": "myapp", "port": "5432"},
		},
		{
			name:  "empty input",
			input: []string{},
			want:  map[string]string{},
		},
		{
			name:  "nil input",
			input: nil,
			want:  map[string]string{},
		},
		{
			name:  "empty value",
			input: []string{"key="},
			want:  map[string]string{"key": ""},
		},
		{
			name:  "value with equals",
			input: []string{"conn=host=localhost dbname=test"},
			want:  map[string]string{"conn": "host=localhost dbname=test"},
		},
		{
			name:  "value with special chars",
			input: []string{"password=p@ss!w0rd#123"},
			want:  map[string]string{"password": "p@ss!w0rd#123"},
		},
		{
			name:    "missing equals",
			input:   []string{"noequalssign"},
			wantErr: "not in key=value format",
		},
		{
			name:    "empty key",
			input:   []string{"=value"},
			wantErr: "empty key",
		},
		{
			name:    "error on second pair",
			input:   []string{"good=pair", "bad"},
			wantErr: "not in key=value format",
		},
		{
			name:  "duplicate key last wins",
			input: []string{"env=dev", "env=prod"},
			want:  map[string]string{"env": "prod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseKeyValuePairs(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Expected error containing %q, got: %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Length mismatch: got %d, want %d", len(got), len(tt.want))
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("Key %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
