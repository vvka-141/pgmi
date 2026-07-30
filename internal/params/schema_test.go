package params

import (
	"strings"
	"testing"
)

func TestSchemaSQL_HasRunTestSourceHelper(t *testing.T) {
	if !strings.Contains(schemaSQL, "pgmi_run_test_source") {
		t.Error("schema.sql must define pgmi_run_test_source() — the helper that pgmi_test_generate delegates to for dollar-quote-safe test execution")
	}
}
