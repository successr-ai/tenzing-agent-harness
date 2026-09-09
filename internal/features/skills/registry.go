package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Definition struct {
	Name        string
	Description string
	path        string
}

type Registry struct {
	skills map[string]Definition
}

func NewRegistry() *Registry {
	return &Registry{
		skills: make(map[string]Definition),
	}
}

func (r *Registry) RegisterSkillDir(skillDir string) {
	r.discoverDir(expandTilde(skillDir))
}

// expandTilde resolves a leading "~/" against the user's home directory.
// Paths without the prefix are returned unchanged, as is the input when the
// home directory cannot be determined.
func expandTilde(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	return path
}

func (r *Registry) Discover() map[string]Definition {
	result := make(map[string]Definition, len(r.skills))
	maps.Copy(result, r.skills)
	return result
}

func (r *Registry) GetSkillMap() map[string]string {
	result := make(map[string]string, len(r.skills))
	for _, def := range r.skills {
		result[def.Name] = def.Description
	}
	return result
}

func (r *Registry) Load(name string) (string, error) {
	def, ok := r.skills[name]
	if !ok {
		return "", fmt.Errorf("skill %q not found", name)
	}
	data, err := os.ReadFile(def.path)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", name, err)
	}
	return fmt.Sprintf("=== SKILL: %s ===\n%s", name, string(data)), nil
}

// discoverDir scans a single skills directory and registers every valid
// skill found. Unreadable directories are skipped silently.
func (r *Registry) discoverDir(skillsDir string) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		r.registerSkill(filepath.Join(skillsDir, entry.Name()), "")
	}
}

// registerSkill reads dir/SKILL.md and registers it under the DIRECTORY name,
// matching Claude Code — the frontmatter name is validated but does not name
// the skill, so a file whose name drifts from its folder still loads under the
// id users see on disk. A non-empty namespace is prefixed as
// "<namespace>:<dir>", which is how plugin skills stay distinct from
// standalone ones.
func (r *Registry) registerSkill(dir, namespace string) {
	path := filepath.Join(dir, "SKILL.md")
	declared, desc, err := parseFrontmatter(path)
	if err != nil {
		// A directory without a SKILL.md simply is not a skill; anything
		// else is a skill the user meant to have and silently lost.
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("skill discovery failed; skipping", "path", path, "error", err)
		}
		return
	}
	name := filepath.Base(dir)
	if declared != name {
		slog.Warn("skill frontmatter name differs from its directory; using the directory",
			"path", path, "declared", declared, "name", name)
	}
	if namespace != "" {
		name = namespace + ":" + name
	}
	r.skills[name] = Definition{
		Name:        name,
		Description: desc,
		path:        path,
	}
}

func parseFrontmatter(path string) (name string, description string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	content := string(data)

	if !strings.HasPrefix(content, "---") {
		return "", "", fmt.Errorf("no frontmatter")
	}
	end := strings.Index(content[3:], "\n---")
	if end == -1 {
		return "", "", fmt.Errorf("unclosed frontmatter")
	}
	fm := content[3 : end+3]

	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
		// Hand-written skills routinely carry an unquoted description holding
		// ": ", which YAML reads as a nested mapping and rejects. Recover the
		// fields by hand rather than dropping the skill.
		meta.Name, meta.Description = scrapeFrontmatter(fm)
		if meta.Name == "" {
			return "", "", fmt.Errorf("parse frontmatter: %w", err)
		}
		slog.Warn("skill frontmatter is not valid YAML; recovered by line scrape",
			"path", path, "skill", meta.Name, "error", err)
	}
	if meta.Name == "" {
		return "", "", fmt.Errorf("missing name")
	}
	return meta.Name, meta.Description, nil
}

// scrapeFrontmatter recovers top-level name/description from frontmatter YAML
// rejects. Values that still look like broken YAML (flow indicators, block
// scalars) are refused, so a genuinely malformed file stays an error.
func scrapeFrontmatter(fm string) (name, description string) {
	for _, line := range strings.Split(fm, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || key != strings.TrimSpace(key) {
			continue // no key, or indented — a nested field, not a top-level one
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if value == "" || strings.ContainsAny(value[:1], "[]{}&*!|>") {
			continue
		}
		switch key {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	return name, description
}
