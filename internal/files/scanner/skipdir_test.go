package scanner

import (
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/filesystem"
)

type stubInfo struct {
	name string
	dir  bool
}

func (i stubInfo) Name() string       { return i.name }
func (i stubInfo) Size() int64        { return 0 }
func (i stubInfo) Mode() fs.FileMode  { return 0 }
func (i stubInfo) ModTime() time.Time { return time.Time{} }
func (i stubInfo) IsDir() bool        { return i.dir }
func (i stubInfo) Sys() any           { return nil }

type stubEntry struct {
	rel string
	dir bool
}

func (e stubEntry) Path() string                 { return "/p/" + e.rel }
func (e stubEntry) RelativePath() string         { return e.rel }
func (e stubEntry) Info() filesystem.FileInfo    { return stubInfo{name: e.rel, dir: e.dir} }
func (e stubEntry) ReadContent() ([]byte, error) { return nil, nil }

// unreadableGitDir walks like filepath.WalkDir over a project whose .git holds
// a file the walker cannot stat: descending into .git surfaces that error.
type unreadableGitDir struct{}

func (unreadableGitDir) Path() string { return "/p" }
func (unreadableGitDir) Walk(fn func(filesystem.File, error) error) error {
	if err := fn(stubEntry{rel: ".", dir: true}, nil); err != nil {
		return err
	}
	err := fn(stubEntry{rel: ".git", dir: true}, nil)
	if errors.Is(err, fs.SkipDir) {
		return nil
	}
	if err != nil {
		return err
	}
	return fn(nil, errors.New("open .git/objects/pack/tmp_pack_x: permission denied"))
}

type stubProvider struct{}

func (stubProvider) Open(string) (filesystem.Directory, error) { return unreadableGitDir{}, nil }
func (stubProvider) ReadFile(string) ([]byte, error)           { return nil, nil }
func (stubProvider) Stat(string) (filesystem.FileInfo, error)  { return stubInfo{dir: true}, nil }

// The scanner excludes .git, node_modules and dot-directories from the load,
// but used to walk into them anyway, so any walk error inside one (a pack file
// being rewritten, a permission-denied cache) failed the deploy (PGMI-382).
func TestScanDirectory_DoesNotDescendIntoExcludedDirectories(t *testing.T) {
	s := NewScannerWithFS(checksum.New(), stubProvider{})
	if _, err := s.ScanDirectory("/p"); err != nil {
		t.Fatalf("an unreadable file inside .git failed the scan: %v", err)
	}
}
