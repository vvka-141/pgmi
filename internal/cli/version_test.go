package cli

import "testing"

func TestResolveVersionInfo_LdflagsOverride(t *testing.T) {
	original := version
	defer func() { version = original }()

	version = "1.2.3"
	v, _, _ := resolveVersionInfo()
	if v != "1.2.3" {
		t.Errorf("expected ldflags version '1.2.3', got %q", v)
	}
}

func TestResolveVersionInfo_DevFallback(t *testing.T) {
	origV, origC, origD := version, commit, date
	defer func() { version, commit, date = origV, origC, origD }()

	version, commit, date = "dev", "unknown", "unknown"
	v, c, d := resolveVersionInfo()

	if v == "" {
		t.Error("version should not be empty")
	}
	// In a test binary, ReadBuildInfo returns test module info.
	// We just verify it doesn't panic and returns something.
	t.Logf("resolved: version=%s commit=%s date=%s", v, c, d)
}
