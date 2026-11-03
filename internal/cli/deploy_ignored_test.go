package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/services"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var ignoringResult = &services.DeployResult{
	FilesLoaded: 7,
	Duration:    200 * time.Millisecond,
	Database:    "myapp",
	Ignored: []pgmi.IgnoredPath{
		{Path: "./.git/", Rule: pgmi.IgnoreRuleBuiltIn},
		{Path: "./clients/", Rule: pgmi.IgnoreRulePgmiignore},
	},
}

func TestDeploySummary_CountsIgnoredPaths(t *testing.T) {
	out := captureStderr(t, func() { printDeploySummary(ignoringResult, nil) })
	if !strings.Contains(out, "7 files loaded, 2 paths ignored") {
		t.Errorf("summary must count ignored paths, got:\n%s", out)
	}

	quiet := *ignoringResult
	quiet.Ignored = nil
	out = captureStderr(t, func() { printDeploySummary(&quiet, nil) })
	if strings.Contains(out, "ignored") {
		t.Errorf("nothing ignored, nothing said; got:\n%s", out)
	}
}

func TestIgnoredPathsListNamesEachPathAndItsRule(t *testing.T) {
	out := captureStderr(t, func() { printIgnoredPaths(ignoringResult) })
	for _, want := range []string{"./.git/ (built-in)", "./clients/ (.pgmiignore)"} {
		if !strings.Contains(out, want) {
			t.Errorf("verbose ignored list must contain %q, got:\n%s", want, out)
		}
	}
}

func TestDeployJSON_CountsIgnoredPaths(t *testing.T) {
	env := decodeEnvelope(t, captureStdout(t, func() {
		printDeployJSON(ignoringResult, nil, deployProvenance{})
	}))
	if env["pathsIgnored"] != float64(2) {
		t.Errorf("pathsIgnored = %#v, want 2", env["pathsIgnored"])
	}
}
