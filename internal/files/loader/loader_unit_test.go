package loader

import (
	"context"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestLoadFilesIntoSession_NilFiles(t *testing.T) {
	l := NewLoader()
	err := l.LoadFilesIntoSession(context.TODO(), nil, nil)
	if err != nil {
		t.Fatalf("Expected nil error for nil files, got: %v", err)
	}
}

func TestLoadFilesIntoSession_EmptyFiles(t *testing.T) {
	l := NewLoader()
	err := l.LoadFilesIntoSession(context.TODO(), nil, []pgmi.FileMetadata{})
	if err != nil {
		t.Fatalf("Expected nil error for empty files, got: %v", err)
	}
}

func TestLoadParametersIntoSession_EmptyParams(t *testing.T) {
	l := NewLoader()
	err := l.LoadParametersIntoSession(context.TODO(), nil, map[string]string{})
	if err != nil {
		t.Fatalf("Expected nil error for empty params, got: %v", err)
	}
}

func TestLoadParametersIntoSession_NilParams(t *testing.T) {
	l := NewLoader()
	err := l.LoadParametersIntoSession(context.TODO(), nil, nil)
	if err != nil {
		t.Fatalf("Expected nil error for nil params, got: %v", err)
	}
}

func TestInsertFiles_Empty(t *testing.T) {
	l := NewLoader()
	if err := l.insertFiles(context.TODO(), nil, nil); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
	if err := l.insertFiles(context.TODO(), nil, []pgmi.FileMetadata{}); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
}

func TestInsertParams_Empty(t *testing.T) {
	l := NewLoader()
	if err := l.insertParams(context.TODO(), nil, nil); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
	if err := l.insertParams(context.TODO(), nil, map[string]string{}); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
}

func TestSetSessionVariables_Empty(t *testing.T) {
	l := NewLoader()
	if err := l.setSessionVariables(context.TODO(), nil, nil); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
	if err := l.setSessionVariables(context.TODO(), nil, map[string]string{}); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
}

func TestSetSessionVariables_InvalidKey(t *testing.T) {
	l := NewLoader()
	tests := []struct {
		name string
		key  string
	}{
		{"spaces", "bad key"},
		{"hyphen", "bad-key"},
		{"dot", "bad.key"},
		{"too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"empty", ""},
		{"special chars", "key;DROP TABLE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := l.setSessionVariables(context.TODO(), nil, map[string]string{tt.key: "value"})
			if err == nil {
				t.Fatal("Expected error for invalid key")
			}
		})
	}
}

func TestInsertMetadata_NoMetadataFiles(t *testing.T) {
	l := NewLoader()
	files := []pgmi.FileMetadata{
		{Path: "a.sql", Content: "SELECT 1;"},
		{Path: "b.sql", Content: "SELECT 2;"},
	}
	if err := l.insertMetadata(context.TODO(), nil, files); err != nil {
		t.Fatalf("Expected nil error for files without metadata: %v", err)
	}
}

func TestInsertMetadata_NilFiles(t *testing.T) {
	l := NewLoader()
	if err := l.insertMetadata(context.TODO(), nil, nil); err != nil {
		t.Fatalf("Expected nil error: %v", err)
	}
}

func TestSetSessionVariables_InvalidKeyBeforeDB(t *testing.T) {
	l := NewLoader()
	params := map[string]string{
		"valid_key":   "ok",
		"invalid key": "bad",
	}
	err := l.setSessionVariables(context.TODO(), nil, params)
	if err == nil {
		t.Fatal("Expected error for invalid parameter key")
	}
}

func TestNewLoader(t *testing.T) {
	l := NewLoader()
	if l == nil {
		t.Fatal("Expected non-nil loader")
	}
}
