package cli

import (
	"runtime/debug"
	"testing"
)

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

func TestResolveVersionInfo_GoInstallMatchesReleaseFormat(t *testing.T) {
	origV, origC, origD, origRead := version, commit, date, readBuildInfo
	defer func() { version, commit, date, readBuildInfo = origV, origC, origD, origRead }()

	version, commit, date = "dev", "unknown", "unknown"
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.12.0"}}, true
	}

	if v, _, _ := resolveVersionInfo(); v != "0.12.0" {
		t.Errorf("go install version = %q, want %q to match release binaries", v, "0.12.0")
	}
}
