package ai_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/ai"
)

// Every event the generator emits is written as ROW(”name”, ...) inside the
// SQL that pgmi_test_generate builds — the doubled quotes are because it is
// itself a quoted string being assembled.
var emittedEventRe = regexp.MustCompile(`ROW\(''([a-z_]+)''`)

// TestEveryEmittedTestEventIsDeclared closes the direction the existing drift
// test leaves open. TestContract_ViewsExistInSQL checks each *declared* event
// appears somewhere in the SQL; nothing checked the reverse, so a tenth
// emission site would ship with the contract still advertising nine and a
// callback written from the contract would fall through its CASE silently.
//
// A callback receives whatever the generator sends. The contract is the only
// list of what that can be.
func TestEveryEmittedTestEventIsDeclared(t *testing.T) {
	body, err := os.ReadFile("../contract/api-v1.sql")
	if err != nil {
		t.Fatalf("read api-v1.sql: %v", err)
	}

	var emitted []string
	for _, m := range emittedEventRe.FindAllStringSubmatch(string(body), -1) {
		if !slices.Contains(emitted, m[1]) {
			emitted = append(emitted, m[1])
		}
	}
	if len(emitted) == 0 {
		t.Fatal("no ROW('' event '' ...) emissions found — has pgmi_test_generate been restructured?")
	}

	var declared []string
	for _, ct := range ai.GetContract().Types {
		declared = append(declared, ct.Events...)
	}
	if len(declared) == 0 {
		t.Fatal("the contract declares no events")
	}

	for _, e := range emitted {
		if !slices.Contains(declared, e) {
			t.Errorf("pgmi_test_generate emits %q, which the contract does not declare — "+
				"a callback written from `pgmi ai contract` would ignore it", e)
		}
	}
	for _, d := range declared {
		if !slices.Contains(emitted, d) {
			t.Errorf("the contract declares event %q, which nothing emits", d)
		}
	}

	// Order is the lifecycle an author reads top to bottom; the contract's list
	// should not drift into some other arrangement.
	slices.Sort(emitted)
	sortedDeclared := slices.Clone(declared)
	slices.Sort(sortedDeclared)
	if !slices.Equal(emitted, sortedDeclared) {
		t.Errorf("event sets differ:\n  emitted  %s\n  declared %s",
			strings.Join(emitted, ", "), strings.Join(sortedDeclared, ", "))
	}
}
