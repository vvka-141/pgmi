package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// The advanced template keeps client code and build output out of the
// session; the basic template has no gateways/ folder and ships no file.
func TestTemplatesShipPgmiignoreWhereTheyNeedIt(t *testing.T) {
	advanced, err := os.ReadFile(filepath.Join(scaffoldInto(t, "advanced"), pgmi.PgmiignoreFile))
	if err != nil {
		t.Fatalf("advanced scaffold must ship %s: %v", pgmi.PgmiignoreFile, err)
	}
	for _, want := range []string{"gateways/", "target/", "bin/", "obj/", "dist/"} {
		if !strings.Contains(string(advanced), "\n"+want+"\n") {
			t.Errorf("advanced %s must exclude %s", pgmi.PgmiignoreFile, want)
		}
	}

	if _, err := os.Stat(filepath.Join(scaffoldInto(t, "basic"), pgmi.PgmiignoreFile)); err == nil {
		t.Errorf("basic scaffold ships %s but has nothing to exclude", pgmi.PgmiignoreFile)
	}
}
