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

func TestIsTemplateDatabase(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"template0", true},
		{"template1", true},
		{"Template0", true},
		{"TEMPLATE1", true},
		{"postgres", false},
		{"mydb", false},
		{"template2", false},
		{"template", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pgmi.IsTemplateDatabase(tt.name); got != tt.want {
				t.Errorf("IsTemplateDatabase(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestIsSQLExtension(t *testing.T) {
	tests := []struct {
		ext  string
		want bool
	}{
		{".sql", true},
		{".SQL", true},
		{".ddl", true},
		{".dml", true},
		{".dql", true},
		{".dcl", true},
		{".psql", true},
		{".pgsql", true},
		{".plpgsql", true},
		{".PLPGSQL", true},
		{".go", false},
		{".py", false},
		{".txt", false},
		{".md", false},
		{"", false},
		{".sqlx", false},
	}

	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			if got := pgmi.IsSQLExtension(tt.ext); got != tt.want {
				t.Errorf("IsSQLExtension(%q) = %v, want %v", tt.ext, got, tt.want)
			}
		})
	}
}
