package skills

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// pluginSkills is one enabled plugin's contribution: the namespace its skills
// are registered under and the directories each holding a SKILL.md.
type pluginSkills struct {
	namespace string
	dirs      []string
}

// RegisterPluginDir registers the skills of every enabled plugin found under a
// Claude config directory (the one holding settings.json and plugins/).
// Plugin skills are namespaced "<plugin>:<skill>" so they cannot collide with
// standalone skills or with each other.
func (r *Registry) RegisterPluginDir(claudeDir string) {
	for _, p := range discoverPlugins(expandTilde(claudeDir)) {
		for _, dir := range p.dirs {
			r.registerSkill(dir, p.namespace)
		}
	}
}

// discoverPlugins resolves enabled plugins to their skill directories. Every
// step is best-effort: a missing or malformed file means "no plugins", never a
// failure, since a harness must still start without Claude Code installed.
func discoverPlugins(claudeDir string) []pluginSkills {
	enabled := enabledPlugins(filepath.Join(claudeDir, "settings.json"))
	if len(enabled) == 0 {
		return nil
	}
	installs := installedPlugins(filepath.Join(claudeDir, "plugins", "installed_plugins.json"))

	var out []pluginSkills
	for _, key := range enabled {
		root := installs[key]
		if root == "" {
			slog.Warn("plugin enabled but not installed; skipping", "plugin", key)
			continue
		}
		name, declared := pluginManifest(filepath.Join(root, ".claude-plugin", "plugin.json"))
		if name == "" {
			// Fall back to the plugin half of "<plugin>@<marketplace>".
			name, _, _ = strings.Cut(key, "@")
		}
		dirs := declared
		if len(dirs) == 0 {
			dirs = walkSkillDirs(filepath.Join(root, "skills"))
		} else {
			for i, d := range dirs {
				dirs[i] = filepath.Join(root, filepath.Clean(d))
			}
		}
		if len(dirs) == 0 {
			continue
		}
		out = append(out, pluginSkills{namespace: name, dirs: dirs})
	}
	return out
}

// enabledPlugins returns the "<plugin>@<marketplace>" keys switched on in
// settings.json, sorted so registration order is stable across runs.
func enabledPlugins(path string) []string {
	var settings struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if err := readJSON(path, &settings); err != nil {
		return nil
	}
	keys := make([]string, 0, len(settings.EnabledPlugins))
	for key, on := range settings.EnabledPlugins {
		if on {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// installedPlugins maps each plugin key to its install root. A key may carry
// several install records (one per scope); the first with a path wins.
func installedPlugins(path string) map[string]string {
	var file struct {
		Plugins map[string][]struct {
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if err := readJSON(path, &file); err != nil {
		return nil
	}
	roots := make(map[string]string, len(file.Plugins))
	for key, entries := range file.Plugins {
		for _, e := range entries {
			if e.InstallPath != "" {
				roots[key] = e.InstallPath
				break
			}
		}
	}
	return roots
}

// pluginManifest reads the plugin's namespace and, when present, its explicit
// skills allowlist. A plugin that ships more skill directories than it
// declares means the extras deliberately, so the list is authoritative.
func pluginManifest(path string) (name string, skills []string) {
	var manifest struct {
		Name   string   `json:"name"`
		Skills []string `json:"skills"`
	}
	if err := readJSON(path, &manifest); err != nil {
		return "", nil
	}
	return manifest.Name, manifest.Skills
}

// walkSkillDirs finds every directory under root holding a SKILL.md. Plugins
// nest skills under category folders, so this cannot be a single-level scan.
func walkSkillDirs(root string) []string {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "SKILL.md" {
			return nil //nolint:nilerr // an unreadable subtree skips, it does not abort
		}
		dirs = append(dirs, filepath.Dir(path))
		return nil
	})
	sort.Strings(dirs)
	return dirs
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		slog.Warn("plugin metadata is not valid JSON; ignoring", "path", path, "error", err)
		return err
	}
	return nil
}
