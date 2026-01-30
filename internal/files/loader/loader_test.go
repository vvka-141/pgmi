package loader

import (
	"strings"
	"testing"
)

func TestValidateParameterKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
		errMsg  string
	}{
		{"valid simple", "env", false, ""},
		{"valid underscore", "my_param", false, ""},
		{"valid numeric", "param123", false, ""},
		{"valid single char", "x", false, ""},
		{"valid max length", strings.Repeat("a", 63), false, ""},
		{"empty", "", true, "invalid parameter key"},
		{"too long", strings.Repeat("a", 64), true, "invalid parameter key"},
		{"spaces", "my param", true, "invalid parameter key"},
		{"special chars", "my-param", true, "invalid parameter key"},
		{"dot", "my.param", true, "invalid parameter key"},
		{"sql injection", "'; DROP TABLE", true, "invalid parameter key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateParameterKey(tt.key)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Expected error for key %q", tt.key)
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Fatalf("Expected %q in error, got: %v", tt.errMsg, err)
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error for key %q: %v", tt.key, err)
				}
			}
		})
	}
}
