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

func TestLoad_EmptyAndV1_ReturnIdenticalSQL(t *testing.T) {
	sqlEmpty, vEmpty, errEmpty := Load("")
	sqlV1, vV1, errV1 := Load("1")

	if errEmpty != nil {
		t.Fatalf("Load('') error: %v", errEmpty)
	}
	if errV1 != nil {
		t.Fatalf("Load('1') error: %v", errV1)
	}
	if vEmpty != vV1 {
		t.Errorf("versions differ: %q vs %q", vEmpty, vV1)
	}
	if sqlEmpty != sqlV1 {
		t.Error("SQL content differs between Load('') and Load('1')")
	}
}

func TestLoad_UnsupportedVersion_ErrorIncludesSupportedVersions(t *testing.T) {
	_, _, err := Load("99")
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
	msg := err.Error()
	for _, v := range SupportedVersions() {
		if !strings.Contains(msg, string(v)) {
			t.Errorf("error message %q does not mention supported version %q", msg, v)
		}
	}
}

func TestLatestVersion_InSupportedVersions(t *testing.T) {
	latest := LatestVersion()
	for _, v := range SupportedVersions() {
		if v == latest {
			return
		}
	}
	t.Errorf("LatestVersion() %q not found in SupportedVersions() %v", latest, SupportedVersions())
}

func TestAPISQL_PgTempSchemaQualified(t *testing.T) {
	sql, _, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	requiredRefs := []string{
		"pg_temp._pgmi_source",
		"pg_temp._pgmi_parameter",
		"pg_temp._pgmi_test_source",
		"pg_temp._pgmi_test_directory",
		"pg_temp._pgmi_source_metadata",
	}

	for _, ref := range requiredRefs {
		if !strings.Contains(sql, ref) {
			t.Errorf("API SQL missing pg_temp-qualified reference: %s", ref)
		}
	}
}

func TestAPISQL_ViewsAreIdempotent(t *testing.T) {
	sql, _, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	lines := strings.Split(sql, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.ToUpper(line))
		if !strings.HasPrefix(trimmed, "CREATE") {
			continue
		}
		// Skip comments
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		isIdempotent := strings.Contains(trimmed, "OR REPLACE") ||
			strings.Contains(trimmed, "CREATE TEMP")
		if !isIdempotent {
			t.Errorf("non-idempotent CREATE statement: %s", strings.TrimSpace(line))
		}
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
