package shared

import (
	"embed"
	"io/fs"
	"os/exec"
	"path"
	"slices"
	"strings"
	"testing"
)

func trackedFiles(t *testing.T, dir string) []string {
	t.Helper()
	out, err := exec.Command("git", "ls-files", "-z", "--", dir).Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	var files []string
	for f := range strings.SplitSeq(strings.TrimRight(string(out), "\x00"), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	return files
}

func TestEmbeddedTreesEqualTrackedFiles(t *testing.T) {
	for dir, tree := range map[string]embed.FS{"templates": Templates, "skills": Skills, "ai": AI} {
		var embedded []string
		err := fs.WalkDir(tree, dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				embedded = append(embedded, p)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(embedded)
		tracked := trackedFiles(t, dir)
		for _, f := range tracked {
			if _, found := slices.BinarySearch(embedded, f); !found {
				t.Errorf("%s is tracked by git but not embedded", f)
			}
		}
		for _, f := range embedded {
			if _, found := slices.BinarySearch(tracked, f); !found {
				t.Errorf("%s is embedded but not tracked by git: an untracked file would ship in this build", f)
			}
		}
	}
}

func TestNoPackageManifestUnderShared(t *testing.T) {
	for _, f := range trackedFiles(t, ".") {
		if f == "Cargo.toml" {
			continue
		}
		switch path.Base(f) {
		case "go.mod", "Cargo.toml":
			t.Errorf("%s: a %s makes its folder a separate package, which go:embed and cargo package leave out; name it %s.scaffold",
				f, path.Base(f), path.Base(f))
		}
	}
}
