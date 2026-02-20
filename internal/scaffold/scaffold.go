package scaffold

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:templates
var templatesFS embed.FS

// GetTemplatesFS returns the embedded templates filesystem for testing purposes.
// This allows tests to access embedded templates without filesystem I/O.
func GetTemplatesFS() embed.FS {
	return templatesFS
}

// Scaffolder handles project initialization from templates
type Scaffolder struct {
	verbose bool
}

// NewScaffolder creates a new Scaffolder instance
func NewScaffolder(verbose bool) *Scaffolder {
	return &Scaffolder{
		verbose: verbose,
	}
}

// CreateProject creates a new project from a template
func (s *Scaffolder) CreateProject(projectName, templateName, targetPath string) error {
	// Validate template exists
	templatePath := fmt.Sprintf("templates/%s", templateName)
	if _, err := templatesFS.ReadDir(templatePath); err != nil {
		return fmt.Errorf("template '%s' not found: %w", templateName, err)
	}

	// Check if target directory is empty
	isEmpty, err := isDirectoryEmpty(targetPath)
	if err != nil {
		return fmt.Errorf("failed to check target directory: %w", err)
	}
	if !isEmpty {
		return fmt.Errorf("target directory '%s' is not empty\n\npgmi init requires an empty directory to avoid overwriting existing files.\n\nOptions:\n• Choose a different location\n• Remove existing files manually\n• Use a new directory name", targetPath)
	}

	// Create target directory if it doesn't exist
	if err := os.MkdirAll(targetPath, 0755); err != nil {
		return fmt.Errorf("failed to create project directory: %w", err)
	}

	s.logVerbose("Creating project '%s' at %s with template '%s'", projectName, targetPath, templateName)

	// Copy template files
	if err := s.copyTemplateFiles(templatePath, targetPath, projectName); err != nil {
		return fmt.Errorf("failed to copy template files: %w", err)
	}

	s.logVerbose("Project created successfully")
	return nil
}

// copyTemplateFiles recursively copies files from embedded template to target directory
func (s *Scaffolder) copyTemplateFiles(templatePath, targetPath, projectName string) error {
	return fs.WalkDir(templatesFS, templatePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip the root template directory itself
		if path == templatePath {
			return nil
		}

		// Calculate relative path from template root
		relPath, err := filepath.Rel(templatePath, path)
		if err != nil {
			return err
		}

		targetFilePath := filepath.Join(targetPath, relPath)

		if d.IsDir() {
			// Create directory
			s.logVerbose("Creating directory: %s", relPath)
			return os.MkdirAll(targetFilePath, 0755)
		}

		// Read file from embedded FS
		content, err := templatesFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read template file %s: %w", path, err)
		}

		// Process template variables
		processedContent := s.processTemplate(string(content), projectName)

		// Skip pgmi-managed files that already exist (e.g. pgmi.yaml from pgmi config)
		if ManagedFiles[filepath.Base(targetFilePath)] {
			if _, err := os.Stat(targetFilePath); err == nil {
				s.logVerbose("Skipping existing: %s", relPath)
				return nil
			}
		}

		s.logVerbose("Creating file: %s", relPath)
		if err := os.WriteFile(targetFilePath, []byte(processedContent), 0644); err != nil {
			return fmt.Errorf("failed to write file %s: %w", targetFilePath, err)
		}

		return nil
	})
}

// processTemplate replaces template variables in content
func (s *Scaffolder) processTemplate(content, projectName string) string {
	content = strings.ReplaceAll(content, "{{PROJECT_NAME}}", projectName)
	return content
}

func (s *Scaffolder) logVerbose(format string, args ...interface{}) {
	if s.verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] "+format+"\n", args...)
	}
}

// ListTemplates returns available template names
func ListTemplates() ([]string, error) {
	entries, err := templatesFS.ReadDir("templates")
	if err != nil {
		return nil, err
	}

	var templates []string
	for _, entry := range entries {
		if entry.IsDir() {
			templates = append(templates, entry.Name())
		}
	}

	return templates, nil
}

// IsValidTemplate checks if the given name matches an available template.
func IsValidTemplate(name string) bool {
	templates, err := ListTemplates()
	if err != nil {
		return false
	}
	for _, t := range templates {
		if t == name {
			return true
		}
	}
	return false
}

// ManagedFiles are files that pgmi itself creates/manages,
// and should not block project initialization.
var ManagedFiles = map[string]bool{
	"pgmi.yaml": true,
	".env":      true,
}

// isDirectoryEmpty checks if a directory is empty or contains only pgmi-managed files.
// Returns (true, nil) if directory doesn't exist, is empty, or only contains pgmi-managed files.
// Returns (false, nil) if directory exists and contains non-pgmi files.
// Returns (false, error) if there's an error checking the directory.
func isDirectoryEmpty(path string) (bool, error) {
	// Check if path exists
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		// Directory doesn't exist - consider it "empty" (safe to create)
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check directory: %w", err)
	}

	// Check if it's actually a directory
	if !info.IsDir() {
		return false, fmt.Errorf("path exists but is not a directory")
	}

	// Read directory contents
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, fmt.Errorf("failed to read directory: %w", err)
	}

	for _, entry := range entries {
		if !ManagedFiles[entry.Name()] {
			return false, nil
		}
	}

	return true, nil
}

// buildFileTree creates a visual tree representation of the directory structure.
// Returns a formatted string showing files and directories in tree format.
func BuildFileTree(rootPath string) (string, error) {
	var sb strings.Builder

	// Get absolute path for display
	absPath, err := filepath.Abs(rootPath)
	if err != nil {
		absPath = rootPath
	}

	sb.WriteString(absPath + "/\n")

	// Walk the directory tree
	dirCache := make(map[string][]fs.DirEntry)
	err = filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if path == rootPath {
			return nil
		}

		relPath, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}

		depth := strings.Count(relPath, string(os.PathSeparator))
		indent := strings.Repeat("│   ", depth)

		parentDir := filepath.Dir(path)
		entries, ok := dirCache[parentDir]
		if !ok {
			entries, err = os.ReadDir(parentDir)
			if err != nil {
				return err
			}
			dirCache[parentDir] = entries
		}

		isLast := false
		baseName := filepath.Base(path)
		for i, e := range entries {
			if e.Name() == baseName && i == len(entries)-1 {
				isLast = true
				break
			}
		}

		branch := "├── "
		if isLast {
			branch = "└── "
			if depth > 0 {
				indent = indent[:len(indent)-4] + "    "
			}
		}

		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}

		sb.WriteString(indent + branch + name + "\n")

		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to build file tree: %w", err)
	}

	return sb.String(), nil
}
