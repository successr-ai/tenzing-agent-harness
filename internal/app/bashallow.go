package app

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions/shell"
)

// BashKey is the only tool name settings.json's permissions map honours.
const BashKey = "bash"

// LookupTool finds the permissions entry for a tool, matching the map's keys
// against the name case-insensitively. key is the spelling the file actually
// used, so a rewrite can keep it rather than adding a second entry that
// differs only in case.
func LookupTool[T any](m map[string]T, tool string) (key string, v T, ok bool) {
	for k, val := range m {
		if strings.EqualFold(k, tool) {
			return k, val, true
		}
	}
	return tool, v, false
}

// BashAllowStore appends a glob to the live bash allow list and persists it
// to the settings file it came from. Writes go to disk first: a failed write
// leaves the session's rules untouched rather than silently diverging.
type BashAllowStore struct {
	mu    sync.Mutex // serializes read-modify-write of the file
	path  string
	rules *permissions.BashRules
}

// NewBashAllowStore binds the live rules to the settings file at path.
func NewBashAllowStore(path string, rules *permissions.BashRules) *BashAllowStore {
	return &BashAllowStore{path: path, rules: rules}
}

// Path is the settings file writes go to.
func (s *BashAllowStore) Path() string { return s.path }

// Rules is the live rule set the store applies additions to.
func (s *BashAllowStore) Rules() *permissions.BashRules { return s.rules }

// Add persists pattern to the settings file's bash allow list, then applies
// it to the running session. Session-only grants (AllowPatternSession) are
// not in Lists() and so never reach the file.
func (s *BashAllowStore) Add(pattern string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	allow, deny := s.rules.Lists()
	if !slices.Contains(allow, pattern) {
		allow = append(allow, pattern)
	}
	catAllow, catDeny := s.rules.Categories()
	section := bashSection{
		Allow:      allow,
		Deny:       deny,
		Categories: categoriesSection{Allow: classNames(catAllow), Deny: classNames(catDeny)},
		Classify:   s.rules.Classify(),
	}
	if err := writeBashSection(s.path, section); err != nil {
		return err
	}
	s.rules.AllowPattern(pattern)
	return nil
}

// classNames renders a class set as the settings-file list; empty for read.
func classNames(c shell.Class) []string {
	if c.IsRead() {
		return []string{}
	}
	return strings.Split(c.String(), ",")
}

// writeBashSection rewrites permissions.bash in the settings file, leaving
// every other top-level key and every other tool under permissions as they
// were. An absent file is created.
func writeBashSection(path string, section bashSection) error {
	doc := map[string]json.RawMessage{}
	switch data, err := os.ReadFile(path); {
	case err == nil:
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("settings %s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("settings %s: %w", path, err)
	}

	perms := map[string]json.RawMessage{}
	if raw, ok := doc["permissions"]; ok {
		if err := json.Unmarshal(raw, &perms); err != nil {
			return fmt.Errorf("settings %s: permissions: %w", path, err)
		}
	}

	if section.Classify == nil {
		section.Classify = map[string]string{}
	}
	raw, err := json.Marshal(section)
	if err != nil {
		return fmt.Errorf("settings %s: %w", path, err)
	}
	key, _, _ := LookupTool(perms, BashKey)
	perms[key] = raw
	if doc["permissions"], err = json.Marshal(perms); err != nil {
		return fmt.Errorf("settings %s: %w", path, err)
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("settings %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("settings %s: %w", path, err)
	}
	return nil
}

// bashSection is the written shape of permissions.bash. Reading goes through
// permissions.BashRules' own decoder; only writing needs this.
type bashSection struct {
	Allow      []string          `json:"allow"`
	Deny       []string          `json:"deny"`
	Categories categoriesSection `json:"categories"`
	Classify   map[string]string `json:"classify"`
}

type categoriesSection struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}
