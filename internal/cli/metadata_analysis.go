package cli

import (
	"path/filepath"
	"sort"

	"github.com/google/uuid"
	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/metadata"
)

// MetadataPlanEntry is one file in execution-order plan output.
type MetadataPlanEntry struct {
	Path        string   `json:"path"`
	ID          string   `json:"id"`
	Idempotent  bool     `json:"idempotent"`
	SortKeys    []string `json:"sort_keys"`
	Description string   `json:"description"`
}

// MetadataPlanResult is the structured result of analyzing a project's plan.
type MetadataPlanResult struct {
	TotalFiles int                 `json:"total_files"`
	Plan       []MetadataPlanEntry `json:"plan"`
}

// MetadataValidateResult is the structured result of validating a project's metadata.
type MetadataValidateResult struct {
	TotalFiles           int      `json:"total_files"`
	FilesWithMetadata    int      `json:"files_with_metadata"`
	FilesWithoutMetadata int      `json:"files_without_metadata"`
	ValidationPassed     bool     `json:"validation_passed"`
	DuplicateIDs         []string `json:"duplicate_ids"`
}

// planProject scans a project and returns its files ordered to approximate
// deployment execution order (smallest sort key, then path).
func planProject(projectPath string) (MetadataPlanResult, error) {
	scanResult, err := scanner.NewScanner(checksum.New()).ScanDirectory(projectPath)
	if err != nil {
		return MetadataPlanResult{}, err
	}

	plan := make([]MetadataPlanEntry, 0, len(scanResult.Files))
	for _, file := range scanResult.Files {
		if file.Metadata == nil {
			plan = append(plan, MetadataPlanEntry{
				Path:        file.Path,
				ID:          metadata.GenerateFallbackID(file.Path).String(),
				Idempotent:  true,
				SortKeys:    []string{filepath.Base(file.Path)},
				Description: "No metadata (fallback)",
			})
			continue
		}
		plan = append(plan, MetadataPlanEntry{
			Path:        file.Path,
			ID:          file.Metadata.ID.String(),
			Idempotent:  file.Metadata.Idempotent,
			SortKeys:    file.Metadata.SortKeys,
			Description: file.Metadata.Description,
		})
	}

	sort.SliceStable(plan, func(i, j int) bool {
		ki, kj := minSortKey(plan[i]), minSortKey(plan[j])
		if ki != kj {
			return ki < kj
		}
		return plan[i].Path < plan[j].Path
	})

	return MetadataPlanResult{TotalFiles: len(plan), Plan: plan}, nil
}

func minSortKey(e MetadataPlanEntry) string {
	if len(e.SortKeys) == 0 {
		return e.Path
	}
	m := e.SortKeys[0]
	for _, k := range e.SortKeys[1:] {
		if k < m {
			m = k
		}
	}
	return m
}

// validateProject scans a project, checks for duplicate metadata IDs, and
// returns the validation summary. The error is non-nil only on scan failure;
// a failed validation is reported via ValidationPassed.
func validateProject(projectPath string) (MetadataValidateResult, error) {
	scanResult, err := scanner.NewScanner(checksum.New()).ScanDirectory(projectPath)
	if err != nil {
		return MetadataValidateResult{}, err
	}

	idToPath := make(map[uuid.UUID]string)
	duplicates := []string{}
	withMetadata := 0
	for _, file := range scanResult.Files {
		if file.Metadata == nil {
			continue
		}
		withMetadata++
		if existing, dup := idToPath[file.Metadata.ID]; dup {
			duplicates = append(duplicates, file.Metadata.ID.String()+": "+existing+", "+file.Path)
		} else {
			idToPath[file.Metadata.ID] = file.Path
		}
	}

	return MetadataValidateResult{
		TotalFiles:           len(scanResult.Files),
		FilesWithMetadata:    withMetadata,
		FilesWithoutMetadata: len(scanResult.Files) - withMetadata,
		ValidationPassed:     len(duplicates) == 0,
		DuplicateIDs:         duplicates,
	}, nil
}
