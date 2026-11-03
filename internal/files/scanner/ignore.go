package scanner

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ignoreRule is one .pgmiignore pattern, compiled to a regular expression over
// the slash-separated path relative to the project root.
type ignoreRule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// ignoreMatcher applies .pgmiignore patterns with .gitignore semantics: the
// last matching pattern decides, "!" re-includes, a trailing "/" matches only
// directories, a pattern with an inner or leading "/" is anchored at the
// project root, and "**" spans directories. A file inside an excluded
// directory cannot be re-included, because the scanner never walks into it.
type ignoreMatcher struct {
	rules []ignoreRule
}

func parseIgnore(content string) (*ignoreMatcher, error) {
	m := &ignoreMatcher{}
	for n, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = trimTrailingSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			rule.negate = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			rule.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		anchored := strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		body, err := globToRegexp(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n+1, err)
		}
		if !anchored {
			body = "(?:.*/)?" + body
		}
		re, err := regexp.Compile("^" + body + "$")
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid pattern %q: %w", n+1, line, err)
		}
		rule.re = re
		m.rules = append(m.rules, rule)
	}
	return m, nil
}

// Match reports whether relPath ("a/b/c", no leading "./") is excluded on its
// own, without looking at its parent directories.
func (m *ignoreMatcher) Match(relPath string, isDir bool) bool {
	ignored := false
	for _, r := range m.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.re.MatchString(relPath) {
			ignored = !r.negate
		}
	}
	return ignored
}

// trimTrailingSpace drops trailing spaces unless the last one is escaped.
func trimTrailingSpace(s string) string {
	for strings.HasSuffix(s, " ") && !strings.HasSuffix(s, `\ `) {
		s = s[:len(s)-1]
	}
	return s
}

// globToRegexp translates one gitignore glob. "*" and "?" never cross a "/";
// "**" as a whole path component spans any number of directories.
func globToRegexp(glob string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '\\' && i+1 < len(glob):
			r, size := utf8.DecodeRuneInString(glob[i+1:])
			b.WriteString(regexp.QuoteMeta(string(r)))
			i += size
		case strings.HasPrefix(glob[i:], "**") && (i == 0 || glob[i-1] == '/'):
			rest := glob[i+2:]
			switch {
			case rest == "":
				b.WriteString(".*")
			case strings.HasPrefix(rest, "/"):
				b.WriteString("(?:.*/)?")
				i++
			default:
				b.WriteString("[^/]*")
			}
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := glob[i+1 : i+1+end]
			if class == "" {
				return "", fmt.Errorf("empty character class in %q", glob)
			}
			if class[0] == '!' {
				if len(class) == 1 {
					return "", fmt.Errorf("empty character class in %q", glob)
				}
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		default:
			r, size := utf8.DecodeRuneInString(glob[i:])
			b.WriteString(regexp.QuoteMeta(string(r)))
			i += size - 1
		}
	}
	return b.String(), nil
}
