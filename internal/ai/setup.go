package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// SkillDirName is the directory written under a skills root (.claude/skills/).
const SkillDirName = "pgmi"

// ModulePath is the Go module path used in the install fallback.
const ModulePath = "github.com/vvka-141/pgmi"

const (
	coreMarker    = "{{CORE}}"
	managedMarker = "<!-- pgmi:managed"
)

// Stamp records which pgmi build generated a file, so a later `check`/`setup`
// can tell whether the file is current and whether a user edited it.
type Stamp struct {
	Version string
	Source  string
}

// PlannedFile is a file an adapter wants to materialize, with its path relative
// to the assistant's target root (e.g. .claude/skills/).
type PlannedFile struct {
	RelPath string
	Content string
}

// Adapter renders the shared pgmi guidance into one assistant's file layout.
// Adding a second assistant (e.g. AGENTS.md for Codex) is a new Adapter over the
// same core content, not a rewrite.
type Adapter interface {
	Name() string
	Files(core string, stamp Stamp) ([]PlannedFile, error)
}

// SupportedAssistants lists the assistant names AdapterFor accepts.
var SupportedAssistants = []string{
	"claude", "agents", "codex", "opencode", "codex-skills",
	"antigravity", "cursor", "copilot", "windsurf", "cline",
}

// AllCanonicalAssistants lists one entry per distinct output target, used by --all.
// Aliases (codex, opencode → agents) are excluded to avoid writing the same file twice.
var AllCanonicalAssistants = []string{
	"claude", "agents", "codex-skills",
	"antigravity", "cursor", "copilot", "windsurf", "cline",
}

// AdapterFor returns the adapter for an assistant name.
func AdapterFor(name string) (Adapter, error) {
	switch name {
	case "claude":
		return claudeAdapter{}, nil
	case "agents", "codex", "opencode":
		return singleFileAdapter{name: "agents", template: "agents-md.md", relPath: "AGENTS.md"}, nil
	case "codex-skills":
		return codexSkillsAdapter{}, nil
	case "antigravity":
		return antigravityAdapter{}, nil
	case "cursor":
		return singleFileAdapter{name: "cursor", template: "cursor-rules.md", relPath: "rules/pgmi.mdc"}, nil
	case "copilot":
		return singleFileAdapter{name: "copilot", template: "agents-md.md", relPath: "copilot-instructions.md"}, nil
	case "windsurf":
		return singleFileAdapter{name: "windsurf", template: "agents-md.md", relPath: "rules/pgmi.md"}, nil
	case "cline":
		return singleFileAdapter{name: "cline", template: "agents-md.md", relPath: "pgmi.md"}, nil
	default:
		return nil, fmt.Errorf("unsupported assistant %q (supported: %s)", name, strings.Join(SupportedAssistants, ", "))
	}
}

// GenerateSetup returns the files an assistant adapter would write for the given
// build stamp, each carrying a managed stamp.
func GenerateSetup(assistant string, stamp Stamp) ([]PlannedFile, error) {
	adapter, err := AdapterFor(assistant)
	if err != nil {
		return nil, err
	}

	core, err := readContent("content/setup/core.md")
	if err != nil {
		return nil, err
	}

	files, err := adapter.Files(core, stamp)
	if err != nil {
		return nil, err
	}

	for i := range files {
		files[i].Content = RenderManaged(files[i].Content, stamp)
	}
	return files, nil
}

type codexSkillsAdapter struct{}

func (codexSkillsAdapter) Name() string { return "codex-skills" }

func (codexSkillsAdapter) Files(core string, stamp Stamp) ([]PlannedFile, error) {
	return skillSetupFiles(core)
}

// skillSetupFiles builds the SKILL.md wrapper plus one depth file per skill.
// Shared by the Claude and Codex skill adapters, which differ only in Name().
func skillSetupFiles(core string) ([]PlannedFile, error) {
	wrapper, err := readContent("content/setup/claude-skill.md")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(wrapper, coreMarker) {
		return nil, fmt.Errorf("claude-skill.md is missing the %s marker", coreMarker)
	}

	skills, err := ListSkills()
	if err != nil {
		return nil, fmt.Errorf("list skills for depth files: %w", err)
	}

	var depthSection strings.Builder
	depthSection.WriteString("\n\n## Depth files\n\n")
	depthSection.WriteString("Load the relevant file from this skill directory when working in that area:\n\n")

	var depthFiles []PlannedFile
	for _, s := range skills {
		content, readErr := contentFS.ReadFile(s.FilePath)
		if readErr != nil {
			continue
		}
		depthFiles = append(depthFiles, PlannedFile{
			RelPath: SkillDirName + "/" + s.Name + ".md",
			Content: string(content),
		})
		desc := s.Description
		if desc == "" {
			desc = s.Name
		}
		fmt.Fprintf(&depthSection, "- `${CLAUDE_SKILL_DIR}/%s.md` — %s\n", s.Name, desc)
	}

	body := strings.ReplaceAll(wrapper, coreMarker, strings.TrimSpace(core))
	body += depthSection.String()

	files := []PlannedFile{{RelPath: SkillDirName + "/SKILL.md", Content: body}}
	files = append(files, depthFiles...)
	return files, nil
}

type singleFileAdapter struct {
	name     string
	template string
	relPath  string
}

func (a singleFileAdapter) Name() string { return a.name }

func (a singleFileAdapter) Files(core string, stamp Stamp) ([]PlannedFile, error) {
	wrapper, err := readContent("content/setup/" + a.template)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(wrapper, coreMarker) {
		return nil, fmt.Errorf("%s is missing the %s marker", a.template, coreMarker)
	}

	body := strings.ReplaceAll(wrapper, coreMarker, strings.TrimSpace(core))
	return []PlannedFile{
		{RelPath: a.relPath, Content: body},
	}, nil
}

type antigravityAdapter struct{}

func (antigravityAdapter) Name() string { return "antigravity" }

func (antigravityAdapter) Files(core string, stamp Stamp) ([]PlannedFile, error) {
	return skillSetupFiles(core)
}

type claudeAdapter struct{}

func (claudeAdapter) Name() string { return "claude" }

func (claudeAdapter) Files(core string, stamp Stamp) ([]PlannedFile, error) {
	return skillSetupFiles(core)
}

func readContent(path string) (string, error) {
	content, err := contentFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", path, err)
	}
	return string(content), nil
}

// ManagedFile is the result of inspecting a materialized file.
type ManagedFile struct {
	Body     string // content above the managed stamp, normalized
	Stamp    Stamp
	HasStamp bool
	valid    bool // stamp present and checksum matches the rest of the file
}

// Edited reports whether a user hand-edited the file since pgmi wrote it: the
// recorded checksum no longer covers the on-disk content, or there is no stamp
// at all (pgmi did not write the file in a recognizable form).
func (m ManagedFile) Edited() bool {
	return !m.valid
}

const checksumKey = "checksum: "

// ParseManaged splits a file into its body and managed stamp and verifies the
// recorded checksum against the whole file (everything except the checksum
// value itself), so edits anywhere in the file are detected.
func ParseManaged(content string) ManagedFile {
	i := strings.Index(content, managedMarker)
	if i < 0 {
		return ManagedFile{Body: normalizeBody(content)}
	}
	head, block := content[:i], content[i:]
	recorded := stampField(block, checksumKey)
	m := ManagedFile{
		Body:     normalizeBody(head),
		HasStamp: true,
		Stamp: Stamp{
			Version: stampField(block, "version:"),
			Source:  stampField(block, "source:"),
		},
	}
	// Blank only the checksum value inside the stamp block (not the body), then
	// hash the whole file. Scoping the substitution to the block prevents body
	// prose that mimics the stamp shape from confusing verification, while still
	// covering any content appended after the stamp.
	bare := head + strings.Replace(block, checksumKey+recorded, checksumKey, 1)
	m.valid = recorded != "" && checksum(bare) == recorded
	return m
}

// RenderManaged normalizes the body and appends the managed stamp block, with
// the checksum computed over the entire file (with an empty checksum value).
// It is the inverse of ParseManaged.
func RenderManaged(body string, stamp Stamp) string {
	bare := stampedForm(normalizeBody(body), stamp)
	return strings.Replace(bare, checksumKey+"\n", checksumKey+checksum(bare)+"\n", 1)
}

// stampedForm builds the file with an empty checksum value. The checksum is
// computed over exactly this form, so render and verify agree.
func stampedForm(normalizedBody string, stamp Stamp) string {
	return normalizedBody + "\n" + managedMarker + "\n" +
		"version: " + stamp.Version + "\n" +
		"source: " + stamp.Source + "\n" +
		checksumKey + "\n-->\n"
}

func normalizeBody(s string) string {
	return strings.TrimRight(s, "\n") + "\n"
}

func stampField(block, key string) string {
	for line := range strings.SplitSeq(block, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, key); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func checksum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
