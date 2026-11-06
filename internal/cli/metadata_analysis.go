package cli

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/pathrule"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/metadata"
)

// MetadataPlanEntry is one row of the plan: a file at one of its sort keys.
type MetadataPlanEntry struct {
	ExecutionOrder int    `json:"executionOrder"`
	Path           string `json:"path"`
	SortKey        string `json:"sortKey"`
	ID             string `json:"id"`
	Idempotent     bool   `json:"idempotent"`
	Description    string `json:"description"`
	IsSQLFile      bool   `json:"isSqlFile"`
}

// MetadataPlanResult is the structured result of analyzing a project's plan.
type MetadataPlanResult struct {
	TotalFiles int                 `json:"totalFiles"`
	Plan       []MetadataPlanEntry `json:"plan"`
}

// MetadataValidateResult is the structured result of validating a project's metadata.
type MetadataValidateResult struct {
	TotalFiles           int      `json:"totalFiles"`
	FilesWithMetadata    int      `json:"filesWithMetadata"`
	FilesWithoutMetadata int      `json:"filesWithoutMetadata"`
	ValidationPassed     bool     `json:"validationPassed"`
	DuplicateIDs         []string `json:"duplicateIds"`
	Warnings             []string `json:"warnings"`
	Error                string   `json:"error,omitempty"`
	ExitCode             int      `json:"exitCode,omitempty"`
}

// planProject computes, without a database, the rows pgmi_plan_view will hold:
// every loaded non-test file once per sort key (its path when it has none),
// numbered in sort_key, path order under COLLATE "C" — Go's byte order.
func planProject(projectPath string) (MetadataPlanResult, error) {
	scanResult, err := scanner.NewScanner(checksum.New()).ScanDirectory(projectPath)
	if err != nil {
		return MetadataPlanResult{}, fmt.Errorf("failed to scan directory: %w", err)
	}
	if err := metadata.CheckUniqueIDs(scanResult.Files); err != nil {
		return MetadataPlanResult{}, err
	}

	files := 0
	var plan []MetadataPlanEntry
	for _, file := range scanResult.Files {
		if pathrule.IsTestPath(file.Path) {
			continue
		}
		files++
		base := MetadataPlanEntry{
			Path:       file.Path,
			ID:         metadata.GenerateFallbackID(file.Path).String(),
			Idempotent: true,
			IsSQLFile:  pathrule.IsSQLExtension(file.Extension),
		}
		keys := []string{file.Path}
		if m := file.Metadata; m != nil {
			base.ID, base.Idempotent, base.Description = m.ID.String(), m.Idempotent, m.Description
			if len(m.SortKeys) > 0 {
				keys = m.SortKeys
			}
		}
		for _, k := range keys {
			e := base
			e.SortKey = k
			plan = append(plan, e)
		}
	}

	slices.SortStableFunc(plan, func(a, b MetadataPlanEntry) int {
		if n := cmp.Compare(a.SortKey, b.SortKey); n != 0 {
			return n
		}
		return cmp.Compare(a.Path, b.Path)
	})
	for i := range plan {
		plan[i].ExecutionOrder = i + 1
	}
	if plan == nil {
		plan = []MetadataPlanEntry{}
	}

	return MetadataPlanResult{TotalFiles: files, Plan: plan}, nil
}

// validateProject scans a project, checks for duplicate metadata IDs, and
// returns the validation summary. The error is non-nil only on scan failure;
// a failed validation is reported via ValidationPassed.
func validateProject(projectPath string) (MetadataValidateResult, error) {
	scanResult, err := scanner.NewScanner(checksum.New()).ScanDirectory(projectPath)
	if err != nil {
		return MetadataValidateResult{}, err
	}

	withMetadata := 0
	warnings := []string{}
	for _, file := range scanResult.Files {
		if file.Metadata != nil {
			withMetadata++
			continue
		}
		if pathrule.IsSQLExtension(file.Extension) && !pathrule.IsTestPath(file.Path) && metadata.MentionsMetaTag(file.Content) {
			warnings = append(warnings, file.Path+": <pgmi-meta> outside a /* */ block comment is not read; "+
				"a -- line comment or a string is ignored, so this file has no metadata")
		}
	}
	duplicates := metadata.DuplicateIDs(scanResult.Files)

	return MetadataValidateResult{
		TotalFiles:           len(scanResult.Files),
		FilesWithMetadata:    withMetadata,
		FilesWithoutMetadata: len(scanResult.Files) - withMetadata,
		ValidationPassed:     len(duplicates) == 0,
		DuplicateIDs:         duplicates,
		Warnings:             warnings,
	}, nil
}
