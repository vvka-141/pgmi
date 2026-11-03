package scaffold

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const leakSalt = "pgmi-no-leak:"

var leakDenylist = map[string]bool{
	"dc7db3f228d591261d965100b4f2ff5026f202826d5ded84e416923d2b666cc7": true,
	"e6c95557d2f65a7deb6fd743edad22a25a3e811e544686c4d556193074bbf642": true,
	"0ce4ae35360032521b63ef0521ebd6d6e95618f9960c0af7aae8f130e593c57f": true,
	"0a09f260b768edec68fabb062ae14003fc3d7e54699a0617f3a54945670897d5": true,
	"25d4841abf00cd1d9f0e49c7b0c45d1b9996944b8689132ec3e75118800fd314": true,
	"e4e2a76a51a54088bcd0faaca4ac865bb7d7ad98bd648bf222f0890509a8cd16": true,
	"b8e462aedba4b08f25a1d9ea6c6cb30b02d78ab9fa939e67495223d1cc86447f": true,
	"dfd1094b5d62725fcb8657018ea8abefdc1f699bcebb0d353b712c738aace121": true,
	"cd7f6da2cad56451afddc83705bf35c0c6877b65892e3fede7486adfdd566d52": true,
	"746bd32616289b074df4a54e8405e1e8347c06ff68b4829f26f08e50df3b38c9": true,
	"c448b118071af8abfc9d8ba14012ead25ba9055dca3e80227e8a50c3b6bed34a": true,
	"544a2a77cfd3cb5711931e334651399079edec9a4230c7e99878e00959bdd634": true,
	"f94017e9bc24a6df952f3e77cbcc2cb59950c75be54ca7067e501ab1e414471c": true,
	"c36f9cca9aa9f66790ef9232b138a9e6aeaf35b0ce561ca837a4b56e3d1bab57": true,
	"93cfe9c8ed504f31b11bb046312289e3b31b28456ddc28415d0edd41ac4be956": true,
	"96f1916458063414bc5b68f272c8d25c1c7332d9996e6791773962562210bb8d": true,
	"5f01e2822c47bc0878b79e10098be59a28fb2d77052c330d8bdc9a80348483d3": true,
	"dc225cdb37f8354e98aa3b6f628f92a59336b8271632a1e227a35591f93016db": true,
	"6a17e199b33042a5e959030fcd51ae45e87cc1db43ee3c3e2ce0b127e51f01aa": true,
	"d9a0a98596aad579e2e10dcb38ab4a0c4b29f70dab4588611cdcbd830601c1a3": true,
}

var cyrillic = regexp.MustCompile(`\p{Cyrillic}`)

func denylisted(token string) bool {
	sum := sha256.Sum256([]byte(leakSalt + token))
	return leakDenylist[hex.EncodeToString(sum[:])]
}

func leakLines(content []byte) []int {
	var lines []int
	prev := ""
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(nil, 1<<24)
	for n := 1; scanner.Scan(); n++ {
		line := norm.NFKC.String(strings.ToLower(scanner.Text()))
		hit := cyrillic.MatchString(line)
		for _, word := range strings.FieldsFunc(line, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			hit = hit || denylisted(word) || (prev != "" && denylisted(prev+" "+word))
			prev = word
		}
		if hit {
			lines = append(lines, n)
		}
	}
	return lines
}

func TestShippedContentCarriesNoSourceProductVocabulary(t *testing.T) {
	trees := map[string]fs.FS{"templates": templatesFS, sharedSkillsDir: sharedSkillsFS, "../ai/content": os.DirFS("../ai/content")}
	for name, tree := range trees {
		root, prefix := ".", name+"/"
		if name == "templates" || name == sharedSkillsDir {
			root, prefix = name, ""
		}
		err := fs.WalkDir(tree, root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			content, err := fs.ReadFile(tree, path)
			if err != nil {
				return err
			}
			for _, line := range leakLines(content) {
				t.Errorf("%s%s:%d: denylisted term", prefix, path, line)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
