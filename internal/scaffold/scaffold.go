package scaffold

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

//go:embed all:templates
var templatesFS embed.FS

// Skills every template ships. They live outside templates/ because each
// directory there is a template.
//
//go:embed all:skills
var sharedSkillsFS embed.FS

const sharedSkillsDir = "skills"

// scaffoldSuffix is dropped from a template file's name when it is written. A
// file named go.mod would make its directory another module, which go:embed
// leaves out without a word.
const scaffoldSuffix = ".scaffold"

// GetTemplatesFS returns the embedded templates filesystem for testing purposes.
// This allows tests to access embedded templates without filesystem I/O.
func GetTemplatesFS() embed.FS {
	return templatesFS
}

// Scaffolder handles project initialization from templates
type Scaffolder struct {
	verbose     bool
	pgmiVersion string
}

// NewScaffolder creates a new Scaffolder instance
func NewScaffolder(verbose bool) *Scaffolder {
	return &Scaffolder{
		verbose: verbose,
	}
}

// WithVersion records the pgmi version that scaffolds the project. It is
// written into deploy.sql so a user can tell, after a template fix, whether
// their files predate it.
func (s *Scaffolder) WithVersion(v string) *Scaffolder {
	s.pgmiVersion = v
	return s
}

// ScaffoldedLine is the first line pgmi init writes into deploy.sql.
func ScaffoldedLine(version, templateName string) string {
	return fmt.Sprintf("-- Scaffolded by pgmi %s from the %s template.", version, templateName)
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
		return fmt.Errorf("%w: target directory '%s' is not empty\n\npgmi init requires an empty directory to avoid overwriting existing files.\n\nOptions:\n• Choose a different location\n• Remove existing files manually\n• Use a new directory name", pgmi.ErrUsage, targetPath)
	}

	// Create target directory if it doesn't exist
	if err := os.MkdirAll(targetPath, 0755); err != nil {
		return fmt.Errorf("failed to create project directory: %w", err)
	}

	s.logVerbose("Creating project '%s' at %s with template '%s'", projectName, targetPath, templateName)

	// Copy template files
	if err := s.copyTemplateFiles(templatesFS, templatePath, targetPath, projectName, templateName); err != nil {
		return fmt.Errorf("failed to copy template files: %w", err)
	}
	if !opinionFree[templateName] {
		if err := s.copySharedSkills(sharedSkillsFS, sharedSkillsDir, targetPath); err != nil {
			return fmt.Errorf("failed to copy shared skills: %w", err)
		}
	}

	s.logVerbose("Project created successfully")
	return nil
}

// buildArtifacts are what a gateway's tests leave in a checkout. go:embed
// all: takes them along, so a binary built from that tree would write them
// into every new project.
var buildArtifacts = map[string]bool{
	"__pycache__": true, "node_modules": true, "target": true, "bin": true, "obj": true, ".venv": true,
}

func isBuildArtifact(d fs.DirEntry) bool {
	if d.IsDir() {
		return buildArtifacts[d.Name()]
	}
	return strings.HasSuffix(d.Name(), ".pyc")
}

// skipArtifact returns fs.SkipDir for an artifact directory, and reports
// whether the walk should pass over the entry.
func skipArtifact(d fs.DirEntry) (bool, error) {
	if !isBuildArtifact(d) {
		return false, nil
	}
	if d.IsDir() {
		return true, fs.SkipDir
	}
	return true, nil
}

// copyTemplateFiles recursively copies files from the template tree to target directory
func (s *Scaffolder) copyTemplateFiles(fsys fs.FS, templatePath, targetPath, projectName, templateName string) error {
	return fs.WalkDir(fsys, templatePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skip, err := skipArtifact(d); skip {
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

		targetFilePath := filepath.Join(targetPath, strings.TrimSuffix(relPath, scaffoldSuffix))

		if d.IsDir() {
			// Create directory
			s.logVerbose("Creating directory: %s", relPath)
			return os.MkdirAll(targetFilePath, 0755)
		}

		// Read file from embedded FS
		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("failed to read template file %s: %w", path, err)
		}

		// Process template variables
		processedContent := s.processTemplate(string(content), projectName, relPath)
		if relPath == "deploy.sql" {
			version := s.pgmiVersion
			if version == "" {
				version = "dev"
			}
			processedContent = ScaffoldedLine(version, templateName) + "\n" + processedContent
		}

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

// copySharedSkills writes the shared skills into .agents/skills/. A template
// skill of the same name is refused, so neither silently replaces the other.
func (s *Scaffolder) copySharedSkills(fsys fs.FS, root, targetPath string) error {
	skillsRoot := filepath.Join(targetPath, ".agents", "skills")
	return fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		if skip, err := skipArtifact(d); skip {
			return err
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		target := filepath.Join(skillsRoot, relPath)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("the template already ships %s, which is also a shared skill", filepath.Join(".agents", "skills", relPath))
		}
		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		s.logVerbose("Creating file: %s", filepath.Join(".agents", "skills", relPath))
		return os.WriteFile(target, content, 0644)
	})
}

// processTemplate replaces template variables in content
func (s *Scaffolder) processTemplate(content, projectName, filePath string) string {
	name := projectName
	if strings.HasSuffix(filePath, ".json") {
		escaped, _ := json.Marshal(name)
		name = string(escaped[1 : len(escaped)-1])
	}
	content = strings.ReplaceAll(content, "{{PROJECT_NAME}}", name)
	return content
}

func (s *Scaffolder) logVerbose(format string, args ...any) {
	if s.verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] "+format+"\n", args...)
	}
}

var templateSummaries = map[string]string{
	"minimal":  "A deploy.sql loop, hello_world.sql and a .pgmiignore; nothing else",
	"basic":    "Two layers: run-once tables, re-run views and functions",
	"advanced": "Reference app: SQL-native REST/RPC/MCP, multi-tenant RLS, audit",
}

// opinionFree templates ship nothing beyond their own files: no agent skills.
var opinionFree = map[string]bool{"minimal": true}

// TemplateSummary is the one-line description every surface shows for a
// template: the init wizard, `pgmi templates list` and `pgmi init --help`.
func TemplateSummary(name string) string {
	return templateSummaries[name]
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
	return slices.Contains(templates, name)
}

// ManagedFiles are files that pgmi itself creates/manages,
// and should not block project initialization.
var ManagedFiles = map[string]bool{
	"pgmi.yaml": true,
	".env":      true,
}

// BlockingEntries names what in dir stops a scaffold: everything except the
// pgmi-managed files and .git (a repository's directory, or the file of a
// worktree or submodule), which no template file can overwrite. A directory
// that cannot be read blocks nothing; the caller creates or reports it.
func BlockingEntries(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var blocking []string
	for _, entry := range entries {
		if !ManagedFiles[entry.Name()] && entry.Name() != ".git" {
			blocking = append(blocking, entry.Name())
		}
	}
	return blocking
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

	if _, err := os.ReadDir(path); err != nil {
		return false, fmt.Errorf("failed to read directory: %w", err)
	}
	return len(BlockingEntries(path)) == 0, nil
}

// countSkills counts the directories under dir that hold a SKILL.md.
func countSkills(dir string) int {
	entries, _ := os.ReadDir(dir)
	n := 0
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); e.IsDir() && err == nil {
			n++
		}
	}
	return n
}

// BuildFileTree draws the directory as a tree; .agents/skills/ is one line
// with its skill count, since a project carries a dozen or more.
func BuildFileTree(rootPath string) (string, error) {
	var sb strings.Builder

	// Get absolute path for display
	absPath, err := filepath.Abs(rootPath)
	if err != nil {
		absPath = rootPath
	}

	sb.WriteString(absPath + "/\n")

	dirCache := make(map[string][]fs.DirEntry)
	var levelIsLast []bool

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

		for len(levelIsLast) <= depth {
			levelIsLast = append(levelIsLast, false)
		}
		levelIsLast[depth] = isLast

		var indent strings.Builder
		for i := range depth {
			if levelIsLast[i] {
				indent.WriteString("    ")
			} else {
				indent.WriteString("│   ")
			}
		}

		branch := "├── "
		if isLast {
			branch = "└── "
		}

		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}

		if entry.IsDir() && relPath == filepath.Join(".agents", "skills") {
			fmt.Fprintf(&sb, "%s%s%s  (%d skills)\n", indent.String(), branch, name, countSkills(path))
			return fs.SkipDir
		}

		sb.WriteString(indent.String() + branch + name + "\n")

		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to build file tree: %w", err)
	}

	return sb.String(), nil
}
