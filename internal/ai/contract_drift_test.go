package ai_test

import (
	"os"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/ai"
)

func TestContract_ViewsExistInSQL(t *testing.T) {
	schema, err := os.ReadFile("../params/schema.sql")
	if err != nil {
		t.Fatalf("read schema.sql: %v", err)
	}
	apiV1, err := os.ReadFile("../contract/api-v1.sql")
	if err != nil {
		t.Fatalf("read api-v1.sql: %v", err)
	}

	combined := string(schema) + "\n" + string(apiV1)
	c := ai.GetContract()

	for _, v := range c.Views {
		if !strings.Contains(combined, v.Name) {
			t.Errorf("view %q declared in contract but not found in schema.sql or api-v1.sql", v.Name)
		}
	}

	for _, f := range c.Functions {
		if !strings.Contains(combined, f.Name) {
			t.Errorf("function %q declared in contract but not found in schema.sql or api-v1.sql", f.Name)
		}
	}

	for _, st := range c.StepTypes {
		if !strings.Contains(combined, "'"+st+"'") {
			t.Errorf("step type %q declared in contract but not found as literal in SQL", st)
		}
	}
}
