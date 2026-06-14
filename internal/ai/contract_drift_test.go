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

		sqlCols := map[string]bool{}
		for _, m := range colRe.FindAllStringSubmatch(tableDef, -1) {
			sqlCols[m[1]] = true
		}

		if len(sqlCols) == 0 {
			t.Errorf("parsed zero columns from %q — regex may need updating", table)
			continue
		}

		for _, col := range v.Columns {
			if !sqlCols[col] {
				t.Errorf("view %q: contract column %q not found in backing table %q columns: %v",
					v.Name, col, table, keys(sqlCols))
			}
		}

		for col := range sqlCols {
			if !slices.Contains(v.Columns, col) {
				t.Errorf("view %q: SQL column %q in %q missing from contract",
					v.Name, col, table)
			}
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

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
