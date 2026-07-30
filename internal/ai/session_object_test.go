package ai

import (
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// sessionObjectRef matches a schema-qualified reference to a pgmi session
// object, which is how the docs tell agents to address them.
var sessionObjectRef = regexp.MustCompile(`pg_temp\.(pgmi_\w+)`)

// extraSessionObjects are published by the session but are not views or
// functions in the contract, so GetContract cannot supply them.
var extraSessionObjects = map[string]bool{
	"pgmi_test":          true, // the macro, written as CALL pg_temp.pgmi_test()
	"pgmi_test_callback": true, // default callback, overridable by name
	"pgmi_test_event":    true, // composite type a callback receives
}

// TestEmbeddedDocsReferenceOnlyRealSessionObjects catches the failure mode that
// is invisible to every other test: a shipped skill naming a pg_temp object
// that does not exist. Agents trust these examples, and the only feedback is
// 42P01 at deploy time, in the user's project.
func TestEmbeddedDocsReferenceOnlyRealSessionObjects(t *testing.T) {
	contract := GetContract()

	known := map[string]bool{}
	for _, v := range contract.Views {
		known[v.Name] = true
	}
	for _, f := range contract.Functions {
		known[f.Name] = true
	}
	for name := range extraSessionObjects {
		known[name] = true
	}

	unknown := map[string][]string{}

	err := fs.WalkDir(contentFS, "content", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}

		body, err := fs.ReadFile(contentFS, path)
		if err != nil {
			return err
		}
		text := string(body)

		for _, m := range sessionObjectRef.FindAllStringSubmatchIndex(text, -1) {
			name := text[m[2]:m[3]]
			if known[name] {
				continue
			}
			line := strings.Count(text[:m[0]], "\n") + 1
			unknown[name] = append(unknown[name], path+":"+strconv.Itoa(line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking embedded content: %v", err)
	}

	if len(unknown) == 0 {
		return
	}

	names := make([]string, 0, len(unknown))
	for name := range unknown {
		names = append(names, name)
	}
	sort.Strings(names)

	var report []string
	for _, name := range names {
		report = append(report, "pg_temp."+name+" — "+strings.Join(unknown[name], ", "))
	}
	t.Errorf("embedded docs reference %d pg_temp object(s) the session never creates.\n"+
		"An agent copying these gets 42P01 in its own project.\n"+
		"Publish the object in GetContract, or correct the docs:\n  %s",
		len(unknown), strings.Join(report, "\n  "))
}
