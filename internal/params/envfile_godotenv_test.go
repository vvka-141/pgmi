package params

import (
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

func TestParseEnvFile_AgreesWithDotenvOrRefuses(t *testing.T) {
	agree := []string{
		"pw=secret",
		"pw = secret  ",
		"pw='abc$Word1'",
		"pw='a\\nb # not a comment'",
		"pw=\"plain words\"",
		"pw=a#b",
		"export pw=1",
		"# a comment",
		"pw=",
	}
	for _, line := range agree {
		t.Run(line, func(t *testing.T) {
			got, err := ParseEnvFile([]byte(line + "\n"))
			if err != nil {
				t.Fatalf("refused a line both parsers read alike: %v", err)
			}
			want, err := godotenv.Parse(strings.NewReader(line + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("got %q, godotenv %q", got, want)
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s = %q, godotenv reads %q", k, got[k], v)
				}
			}
		})
	}

	refuse := []string{
		"pw=SECRETabc   # rotated monthly",
		"pw= #SECRETabc",
		"pw=SECRETabc$Word1",
		"pw=\"SECRETa\\nb\"",
		"pw=\"SECRETabc$XY\"",
		"pw=\"SECRETunterminated",
		"pw='SECRETunterminated",
		"pw=\"SECRETa\" trailing",
	}
	for _, line := range refuse {
		t.Run(line, func(t *testing.T) {
			_, err := ParseEnvFile([]byte("ok=1\n" + line + "\n"))
			if err == nil {
				t.Fatal("accepted a line the project .env would read differently")
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Errorf("the error prints the value: %v", err)
			}
			if !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "single quotes") {
				t.Errorf("want the line number and the single-quote remedy, got: %v", err)
			}
		})
	}
}
