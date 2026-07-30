package ai_test

import (
	"os"
	"regexp"
	"slices"
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

	for _, ct := range c.Types {
		if !strings.Contains(combined, ct.Name) {
			t.Errorf("type %q declared in contract but not found in schema.sql or api-v1.sql", ct.Name)
		}
		for _, ev := range ct.Events {
			if !strings.Contains(combined, "'"+ev+"'") {
				t.Errorf("event %q of type %q not found as literal in SQL", ev, ct.Name)
			}
		}
	}
}

func TestContract_ViewColumnsMatchSchema(t *testing.T) {
	schema, err := os.ReadFile("../params/schema.sql")
	if err != nil {
		t.Fatalf("read schema.sql: %v", err)
	}
	apiV1, err := os.ReadFile("../contract/api-v1.sql")
	if err != nil {
		t.Fatalf("read api-v1.sql: %v", err)
	}

	backingTable := map[string]string{
		"pgmi_source_view":          "_pgmi_source",
		"pgmi_parameter_view":       "_pgmi_parameter",
		"pgmi_test_source_view":     "_pgmi_test_source",
		"pgmi_test_directory_view":  "_pgmi_test_directory",
		"pgmi_source_metadata_view": "_pgmi_source_metadata",
	}

	combined := string(schema) + "\n" + string(apiV1)
	c := ai.GetContract()
	colRe := regexp.MustCompile(`(?m)^\s+"?(\w+)"?\s+(?:TEXT|INTEGER|BIGINT|BOOLEAN|BOOL|UUID|INT|TIMESTAMPTZ|SERIAL)`)

	for _, v := range c.Views {
		table, ok := backingTable[v.Name]
		if !ok {
			continue
		}

		tableStart := strings.Index(combined, "CREATE TEMP TABLE pg_temp."+table)
		if tableStart == -1 {
			tableStart = strings.Index(combined, "CREATE TEMP TABLE "+table)
		}
		if tableStart == -1 {
			t.Errorf("backing table %q for view %q not found in SQL", table, v.Name)
			continue
		}

		parenClose := strings.Index(combined[tableStart:], ");")
		if parenClose == -1 {
			t.Errorf("could not find end of CREATE TABLE for %q", table)
			continue
		}
		tableDef := combined[tableStart : tableStart+parenClose]

		var sqlCols []string
		for _, m := range colRe.FindAllStringSubmatch(tableDef, -1) {
			sqlCols = append(sqlCols, m[1])
		}

		if len(sqlCols) == 0 {
			t.Errorf("parsed zero columns from %q — regex may need updating", table)
			continue
		}

		if !slices.Equal(v.Columns, sqlCols) {
			t.Errorf("view %q: contract columns %v != SQL columns %v (order matters)",
				v.Name, v.Columns, sqlCols)
		}
	}
}

func TestContract_FunctionDefaultsMatchSQL(t *testing.T) {
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
	for _, f := range c.Functions {
		for _, arg := range f.Args {
			if !strings.Contains(arg, "DEFAULT") {
				continue
			}
			parts := strings.SplitN(arg, " DEFAULT ", 2)
			if len(parts) != 2 {
				continue
			}
			defaultVal := strings.TrimSpace(parts[1])
			paramName := strings.Fields(parts[0])[0]

			searchDefault := paramName + " TEXT DEFAULT " + defaultVal
			if !strings.Contains(strings.ToLower(combined), strings.ToLower(searchDefault)) {
				t.Errorf("function %q: contract default %q for param %q not found in SQL",
					f.Name, defaultVal, paramName)
			}
		}
	}
}
