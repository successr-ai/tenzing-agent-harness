package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/successr-ai/tenzing-agent-harness/internal/app"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
)

// defaultSettingsPath is probed when neither --settings nor TENZING_SETTINGS
// names a file; missing there is fine (no per-command rules).
const defaultSettingsPath = "settings.json"

// settingsFile is settings.json: per-command rules layered on top of the
// name-level `permissions:` section of tenzing.yaml. `permissions` maps a
// tool name to its rules; only "bash" is honoured today (it is the only tool
// whose input is a command line to glob-match), and other tool keys are
// ignored, as are other top-level keys. Tool names are matched
// case-insensitively, so "Bash" and "bash" name the same tool.
//
//	{"permissions": {"bash": {"allow": ["ls *"], "deny": ["rm -rf *"]}}}
type settingsFile struct {
	Permissions map[string]*permissions.BashRules `json:"permissions"`
}

// userSettingsPath is the per-user fallback probed when there is no
// ./settings.json: <UserConfigDir>/tenzing/settings.json, the same directory
// the per-user tenzing.yaml is probed in. Returns "" when the base dir is
// unavailable.
func userSettingsPath() string {
	dir := userConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, defaultSettingsPath)
}

// resolveSettingsPath picks the settings.json location: --settings flag >
// TENZING_SETTINGS env > ./settings.json > <UserConfigDir>/tenzing/settings.json.
// explicit reports whether the user named the path — a missing file is then
// a startup error instead of a silent skip; the two probed paths stay
// non-explicit.
func resolveSettingsPath(flagValue string, flagChanged bool) (path string, explicit bool) {
	if flagChanged {
		return flagValue, true
	}
	if v := os.Getenv("TENZING_SETTINGS"); v != "" {
		return v, true
	}
	if _, err := os.Stat(defaultSettingsPath); err != nil {
		if user := userSettingsPath(); user != "" {
			if _, err := os.Stat(user); err == nil {
				return user, false
			}
		}
	}
	return defaultSettingsPath, false
}

// loadSettingsFile reads settings.json. An absent non-explicit file yields
// nil rules; a file that exists but is malformed is a hard error — silently
// ignoring a security policy is worse than refusing to start.
func loadSettingsFile(path string, explicit bool) (*permissions.BashRules, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !explicit {
			return nil, nil
		}
		return nil, fmt.Errorf("settings %s: %w", path, err)
	}
	var f settingsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("settings %s: %w", path, err)
	}
	_, rules, _ := app.LookupTool(f.Permissions, app.BashKey)
	return rules, nil
}

// applyBashRules layers settings.json's per-command rules onto whatever
// name-level policy tenzing.yaml produced (or the default, when it named no
// `permissions:` section), and parks a store on cfg so the approval endpoint
// can append to them. rules may be nil (no settings file): the empty rules
// never produce a verdict, so behaviour is unchanged until something is
// added, and the store writes to the path that would have been read.
func applyBashRules(cfg *cliConfig, path string, rules *permissions.BashRules) {
	if rules == nil {
		rules = permissions.NewBashRules(nil, nil)
	}
	p := permissions.DefaultPolicy()
	if cfg.PermissionPolicy != nil {
		p = *cfg.PermissionPolicy
	}
	p.Bash = rules
	cfg.PermissionPolicy = &p
	cfg.BashAllow = app.NewBashAllowStore(path, rules)
}
