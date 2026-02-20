package contract

import (
	"strings"
	"testing"
)

func TestLoad_Latest(t *testing.T) {
	sql, version, err := Load("")
	if err != nil {
		t.Fatalf("Load('') failed: %v", err)
	}
	if version != Latest {
		t.Errorf("expected version %q, got %q", Latest, version)
	}
	if sql == "" {
		t.Error("expected non-empty SQL")
	}
	if len(sql) < 100 {
		t.Errorf("SQL seems too short: %d bytes", len(sql))
	}
}

func TestLoad_V1(t *testing.T) {
	sql, version, err := Load("1")
	if err != nil {
		t.Fatalf("Load('1') failed: %v", err)
	}
	if version != V1 {
		t.Errorf("expected version %q, got %q", V1, version)
	}
	if sql == "" {
		t.Error("expected non-empty SQL")
	}
}

func TestLoad_UnsupportedVersion(t *testing.T) {
	_, _, err := Load("99")
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
}

func TestSupportedVersions(t *testing.T) {
	versions := SupportedVersions()
	if len(versions) == 0 {
		t.Fatal("expected at least one supported version")
	}

	found := false
	for _, v := range versions {
		if v == V1 {
			found = true
			break
		}
	}
	if !found {
		t.Error("V1 not found in supported versions")
	}
}

func TestLatestVersion(t *testing.T) {
	if LatestVersion() != V1 {
		t.Errorf("expected latest version %q, got %q", V1, LatestVersion())
	}
}

func TestAPIContainsExpectedObjects(t *testing.T) {
	sql, _, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	expectedPatterns := []string{
		"pgmi_source_view",
		"pgmi_parameter_view",
		"pgmi_test_source_view",
		"pgmi_test_directory_view",
		"pgmi_source_metadata_view",
		"pgmi_plan_view",
		"pgmi_test_generate",
	}

	for _, pattern := range expectedPatterns {
		if !strings.Contains(sql, pattern) {
			t.Errorf("API SQL missing expected pattern: %s", pattern)
		}
	}
}
