package docs_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/ai"
)

// Exit codes are pgmi's contract with CI: a pipeline branches on them. They are
// declared once in pkg/pgmi and then written out by hand in three more places.
// ai.GetContract() references the constants rather than copying the numbers, so
// it cannot drift and serves as the authority here.
//
// Two doc surfaces are treated differently on purpose. docs/CLI.md is the
// reference and must list every code. pgmi-debug-deploy is a diagnosis table
// that deliberately omits 0-3 — nobody debugs a success — so it only has to
// avoid inventing codes. Padding it to full coverage would be the wrong fix.
func TestExitCodeTablesMatchTheConstants(t *testing.T) {
	declared := map[int]bool{}
	for _, e := range ai.GetContract().ExitCodes {
		declared[e.Code] = true
	}
	if len(declared) == 0 {
		t.Fatal("the contract declares no exit codes")
	}

	t.Run("every Exit constant reaches the contract", func(t *testing.T) {
		src := read(t, "../../pkg/pgmi/constants.go")
		constRe := regexp.MustCompile(`(?m)^\s*(Exit\w+)\s*=\s*(\d+)`)

		matches := constRe.FindAllStringSubmatch(src, -1)
		if len(matches) == 0 {
			t.Fatal("no Exit* constants found — has constants.go moved?")
		}
		for _, m := range matches {
			code, err := strconv.Atoi(m[2])
			if err != nil {
				t.Fatalf("%s: %v", m[1], err)
			}
			if !declared[code] {
				t.Errorf("%s = %d is declared in pkg/pgmi but missing from ai.GetContract(), "+
					"so `pgmi ai contract` does not tell an agent it exists", m[1], code)
			}
		}
		if len(matches) != len(declared) {
			t.Errorf("pkg/pgmi declares %d Exit constants, the contract %d", len(matches), len(declared))
		}
	})

	t.Run("docs/CLI.md lists exactly the real codes", func(t *testing.T) {
		got := tableCodes(read(t, "../../docs/CLI.md"), "## Exit Codes")
		for code := range declared {
			if !got[code] {
				t.Errorf("exit code %d is missing from the CLI reference table", code)
			}
		}
		for code := range got {
			if !declared[code] {
				t.Errorf("the CLI reference documents exit code %d, which pgmi never returns", code)
			}
		}
	})

	t.Run("pgmi-debug-deploy invents no codes", func(t *testing.T) {
		got := tableCodes(read(t, "../../internal/ai/content/skills/pgmi-debug-deploy.md"), "## Exit code → diagnosis")
		if len(got) == 0 {
			t.Fatal("no exit-code rows found — has the diagnosis table moved?")
		}
		for code := range got {
			if !declared[code] {
				t.Errorf("the debug skill diagnoses exit code %d, which pgmi never returns", code)
			}
		}
	})
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// tableCodes collects the integer in the first cell of every markdown table row
// in the section starting at heading, stopping at the next "## ".
var rowCodeRe = regexp.MustCompile(`(?m)^\|\s*[` + "`" + `*]*(\d+)[` + "`" + `*]*\s*\|`)

func tableCodes(doc, heading string) map[int]bool {
	_, section, found := strings.Cut(doc, heading)
	if !found {
		return nil
	}
	section, _, _ = strings.Cut(section, "\n## ")

	codes := map[int]bool{}
	for _, m := range rowCodeRe.FindAllStringSubmatch(section, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			codes[n] = true
		}
	}
	return codes
}
