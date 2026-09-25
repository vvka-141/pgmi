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
	origV, origC, origD, origRead := version, commit, date, readBuildInfo
	defer func() { version, commit, date, readBuildInfo = origV, origC, origD, origRead }()

	version, commit, date = "dev", "unknown", "unknown"
	readBuildInfo = func() (*debug.BuildInfo, bool) { return nil, false }

	if v, c, d := resolveVersionInfo(); v != "dev" || c != "unknown" || d != "unknown" {
		t.Errorf("without ldflags or build info want dev/unknown/unknown, got %s/%s/%s", v, c, d)
	}
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
