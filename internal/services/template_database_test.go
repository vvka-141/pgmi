package services

import "testing"

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
			if got := isTemplateDatabase(tt.name); got != tt.want {
				t.Errorf("IsTemplateDatabase(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
