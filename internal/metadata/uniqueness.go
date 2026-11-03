package metadata

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// DuplicateIDs lists each <pgmi-meta id> shared by more than one file, as
// "id: path, path", sorted. Deploy, plan and validate all read this one list,
// so they cannot disagree about whether a project is valid.
func DuplicateIDs(files []pgmi.FileMetadata) []string {
	pathsByID := make(map[uuid.UUID][]string)
	for _, f := range files {
		if f.Metadata == nil || f.Metadata.ID == uuid.Nil {
			continue
		}
		pathsByID[f.Metadata.ID] = append(pathsByID[f.Metadata.ID], f.Path)
	}

	conflicts := []string{}
	for id, paths := range pathsByID {
		if len(paths) > 1 {
			slices.SortFunc(paths, cmp.Compare)
			conflicts = append(conflicts, fmt.Sprintf("%s: %s", id, strings.Join(paths, ", ")))
		}
	}
	slices.SortFunc(conflicts, cmp.Compare)
	return conflicts
}

// CheckUniqueIDs rejects a project in which two files share a <pgmi-meta id>.
// A shared id makes the second run-once script silently skip on deploy, since
// its id already has an execution-log row.
func CheckUniqueIDs(files []pgmi.FileMetadata) error {
	dups := DuplicateIDs(files)
	if len(dups) == 0 {
		return nil
	}
	return fmt.Errorf("duplicate <pgmi-meta id> across files; each script must have a unique id:\n  %s: %w",
		strings.Join(dups, "\n  "), pgmi.ErrInvalidConfig)
}
