package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

var (
	fencedCode  = regexp.MustCompile("(?s)```[^\n]*\n(.*?)```")
	inlineCode  = regexp.MustCompile("`([^`\n]+)`")
	pgmiCommand = regexp.MustCompile(`(?:^|[^\w./-])pgmi((?:[ \t]+[a-z][a-z-]*)+)`)
)

// unknownCommands returns every `pgmi ...` invocation inside the code of a
// markdown document that names a subcommand the CLI does not have. Prose is
// skipped: "pgmi loads your files" is a sentence, not a command.
func unknownCommands(markdown string) []string {
	code := fencedCode.FindAllStringSubmatch(markdown, -1)
	rest := fencedCode.ReplaceAllString(markdown, "")
	code = append(code, inlineCode.FindAllStringSubmatch(rest, -1)...)

	var unknown []string
	for _, block := range code {
		for _, m := range pgmiCommand.FindAllStringSubmatch(block[1], -1) {
			cmd := rootCmd
			for word := range strings.FieldsSeq(m[1]) {
				next := subcommand(cmd, word)
				if next == nil {
					if cmd.HasSubCommands() {
						unknown = append(unknown, "pgmi"+m[1])
					}
					break
				}
				cmd = next
			}
		}
	}
	return unknown
}

func subcommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return c
		}
	}
	return nil
}

func TestUnknownCommandsFindsRemovedCommands(t *testing.T) {
	for _, tc := range []struct {
		markdown string
		want     int
	}{
		{"`pgmi ai skill pgmi-handler-patterns` prints it", 1},
		{"```bash\npgmi ai setup\n```", 1},
		{"`pgmi init --template advanced`", 0},
		{"```bash\npgmi deploy . --overwrite --force\n```", 0},
		{"`pgmi ai contract`", 0},
		{"pgmi loads your files and hands control to deploy.sql", 0},
	} {
		if got := unknownCommands(tc.markdown); len(got) != tc.want {
			t.Errorf("unknownCommands(%q) = %q, want %d hit(s)", tc.markdown, got, tc.want)
		}
	}
}

// TestArticlesNameOnlyExistingCommands keeps the published articles from
// telling readers to run a command this binary no longer has.
func TestArticlesNameOnlyExistingCommands(t *testing.T) {
	paths, err := filepath.Glob("../../website/content/articles/*.md")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no articles found: %v", err)
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, cmd := range unknownCommands(string(body)) {
			t.Errorf("%s names a command pgmi does not have: %s", path, cmd)
		}
	}
}
