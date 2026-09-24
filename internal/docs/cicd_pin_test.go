package docs_test

import (
	"os"
	"regexp"
	"testing"
)

// The CI guide's copy-paste recipes pin a release. The pin sat at v0.11.0 for
// a release that changed the execution contract, so a pipeline built from the
// guide ran today's deploy.sql patterns on a binary that predates them. Every
// release section in RELEASES.md must be matched by the guide's pin.
func TestCICDGuidePinsNewestRelease(t *testing.T) {
	releases, err := os.ReadFile("../../RELEASES.md")
	if err != nil {
		t.Fatal(err)
	}
	newest := regexp.MustCompile(`(?m)^## (v\d+\.\d+\.\d+)`).FindSubmatch(releases)
	if newest == nil {
		t.Fatal("no release heading in RELEASES.md")
	}

	guide, err := os.ReadFile("../../docs/CICD.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range regexp.MustCompile(`v\d+\.\d+\.\d+`).FindAll(guide, -1) {
		if string(pin) != string(newest[1]) {
			t.Errorf("docs/CICD.md pins %s; the newest release in RELEASES.md is %s", pin, newest[1])
		}
	}
}
