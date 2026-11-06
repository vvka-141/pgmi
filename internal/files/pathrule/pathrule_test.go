package pathrule

import "testing"

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
			if got := IsTestPath(tt.path); got != tt.want {
				t.Errorf("IsTestPath(%q) = %v, want %v", tt.path, got, tt.want)
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
			if got := IsSQLExtension(tt.ext); got != tt.want {
				t.Errorf("IsSQLExtension(%q) = %v, want %v", tt.ext, got, tt.want)
			}
		})
	}
}
