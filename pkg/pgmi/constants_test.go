package pgmi_test

import (
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestIsTestPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"./migrations/__test__/test_foo.sql", true},
		{"./migrations/__tests__/test_foo.sql", true},
		{"./__test__/test.sql", true},
		{"./__tests__/test.sql", true},
		{"./migrations/schema.sql", false},
		{"./migrations/__foo__/bar.sql", false},
		{"./test/test.sql", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := pgmi.IsTestPath(tt.path); got != tt.want {
				t.Errorf("IsTestPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestValidateDunderDirectories(t *testing.T) {
	tests := []struct {
		path    string
		wantErr bool
	}{
		{"./migrations/__test__/test_foo.sql", false},
		{"./migrations/__tests__/test_foo.sql", false},
		{"./migrations/schema.sql", false},
		{"./migrations/__foo__/bar.sql", true},
		{"./migrations/__bar__/x.sql", true},
		{"./test/test.sql", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := pgmi.ValidateDunderDirectories(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDunderDirectories(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}
