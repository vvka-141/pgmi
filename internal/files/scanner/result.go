package scanner

import "github.com/vvka-141/pgmi/pkg/pgmi"

// Result is what scanning a project directory found.
type Result struct {
	Files     []pgmi.FileMetadata
	DeploySQL string
	// Ignored lists each path discovery left out, highest first: an ignored
	// directory appears once, not file by file.
	Ignored []pgmi.IgnoredPath
}
