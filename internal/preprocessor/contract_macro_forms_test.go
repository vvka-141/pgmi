package preprocessor

import (
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/ai"
)

// TestContractMacroFormsAreDetected drives the real detector with the exact
// strings `pgmi ai contract` publishes.
//
// A macro form is not documentation: an agent copies it into deploy.sql and
// expects expansion. A form the detector does not recognise passes through
// untouched and reaches PostgreSQL as a literal CALL to a procedure that does
// not exist — 42883, at deploy time, in the user's project. Nothing checked
// that the published forms and the detector agreed.
func TestContractMacroFormsAreDetected(t *testing.T) {
	macros := ai.GetContract().Macros
	if len(macros) == 0 {
		t.Fatal("the contract publishes no macro forms")
	}

	detector := NewMacroDetector()
	stripper := NewCommentStripper()

	for _, m := range macros {
		t.Run(m.Form, func(t *testing.T) {
			// The form as an agent would write it: inside the explicit
			// transaction block the contract's own description demands.
			sql := "BEGIN;\n" + m.Form + ";\nCOMMIT;\n"

			calls := detector.Detect(sql, stripper.RedactForMacros(sql))
			if len(calls) != 1 {
				t.Fatalf("detector found %d macro calls in %q, want 1", len(calls), m.Form)
			}

			call := calls[0]
			if call.Name != "pgmi_test" {
				t.Errorf("Name = %q, want pgmi_test", call.Name)
			}

			// The arguments the form spells out must survive into the call,
			// otherwise the published form and the expansion disagree about
			// what it does.
			args := argsOf(m.Form)
			if len(args) > 0 && call.Pattern != args[0] {
				t.Errorf("Pattern = %q, want %q (from %s)", call.Pattern, args[0], m.Form)
			}
			if len(args) > 1 && call.Callback != args[1] {
				t.Errorf("Callback = %q, want %q (from %s)", call.Callback, args[1], m.Form)
			}
			// A form that names no argument must not acquire one. The default
			// callback is chosen downstream, not invented by the detector.
			if len(args) < 1 && call.Pattern != "" {
				t.Errorf("Pattern = %q for a form that names none", call.Pattern)
			}
			if len(args) < 2 && call.Callback != "" {
				t.Errorf("Callback = %q for a form that names none", call.Callback)
			}

			// The span swallows the terminating semicolon: expansion replaces a
			// whole statement, and leaving the ';' behind would strand it after
			// the generated block.
			if got, want := sql[call.StartPos:call.EndPos], m.Form+";"; got != want {
				t.Errorf("replaced span is %q, want %q", got, want)
			}
		})
	}
}

// The schema-qualified spelling is legal and CLAUDE.md documents it, so the
// detector must accept it even though the contract lists the bare form.
func TestSchemaQualifiedMacroFormIsDetected(t *testing.T) {
	sql := "BEGIN;\nCALL pg_temp.pgmi_test();\nCOMMIT;\n"
	calls := NewMacroDetector().Detect(sql, NewCommentStripper().RedactForMacros(sql))
	if len(calls) != 1 {
		t.Fatalf("found %d macro calls, want 1", len(calls))
	}
}

// argsOf pulls the quoted arguments out of a published form such as
// CALL pgmi_test('pattern', 'callback').
func argsOf(form string) []string {
	open := strings.Index(form, "(")
	closing := strings.LastIndex(form, ")")
	if open == -1 || closing <= open+1 {
		return nil
	}

	var args []string
	for _, part := range strings.Split(form[open+1:closing], ",") {
		args = append(args, strings.Trim(strings.TrimSpace(part), "'"))
	}
	return args
}
