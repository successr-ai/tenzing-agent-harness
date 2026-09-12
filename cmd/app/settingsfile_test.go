package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/app"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
)

// resolveSettingsPath's fallback chain: --settings > TENZING_SETTINGS >
// ./settings.json > <UserConfigDir>/tenzing/settings.json.
func TestResolveSettingsPath(t *testing.T) {
	writeUserSettings := func(t *testing.T) string {
		t.Helper()
		dir := userConfigDir()
		if dir == "" {
			t.Fatal("userConfigDir() is empty")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, defaultSettingsPath)
		if err := os.WriteFile(p, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	tests := []struct {
		name          string
		flagValue     string
		flagChanged   bool
		env           string
		localSettings bool
		userSettings  bool
		wantPath      func(user string) string
		wantExplicit  bool
	}{
		{
			name: "flag wins over everything", flagValue: "custom.json", flagChanged: true,
			env: "env.json", localSettings: true, userSettings: true,
			wantPath:     func(string) string { return "custom.json" },
			wantExplicit: true,
		},
		{
			name: "env wins over local and user", env: "env.json",
			localSettings: true, userSettings: true,
			wantPath:     func(string) string { return "env.json" },
			wantExplicit: true,
		},
		{
			name: "local settings win over user settings", localSettings: true, userSettings: true,
			wantPath: func(string) string { return defaultSettingsPath },
		},
		{
			name: "falls back to user settings when no local", userSettings: true,
			wantPath: func(user string) string { return user },
		},
		{
			name:     "no settings anywhere returns the ambient default",
			wantPath: func(string) string { return defaultSettingsPath },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			redirectUserConfig(t)
			t.Setenv("TENZING_SETTINGS", tt.env)

			if tt.localSettings {
				if err := os.WriteFile(defaultSettingsPath, []byte(`{}`), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var user string
			if tt.userSettings {
				user = writeUserSettings(t)
			}

			path, explicit := resolveSettingsPath(tt.flagValue, tt.flagChanged)
			if want := tt.wantPath(user); path != want {
				t.Errorf("path = %q, want %q", path, want)
			}
			if explicit != tt.wantExplicit {
				t.Errorf("explicit = %v, want %v", explicit, tt.wantExplicit)
			}
		})
	}
}

// settings.json and tenzing.yaml share one per-user directory: whatever
// os.UserConfigDir() resolves to, plus "tenzing".
func TestUserSettingsPathSharesConfigDir(t *testing.T) {
	redirectUserConfig(t)

	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(base, "tenzing", defaultSettingsPath)
	if got := userSettingsPath(); got != want {
		t.Errorf("userSettingsPath() = %q, want %q", got, want)
	}
	if got, want := filepath.Dir(userSettingsPath()), filepath.Dir(userConfigPath()); got != want {
		t.Errorf("settings dir = %q, config dir = %q; want the same", got, want)
	}
}

func TestLoadSettingsFile(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), defaultSettingsPath)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("reads the bash section", func(t *testing.T) {
		p := write(t, `{"permissions":{"bash":{"allow":["ls *"],"deny":["rm -rf *"]}}}`)
		rules, err := loadSettingsFile(p, false)
		if err != nil {
			t.Fatal(err)
		}
		if rules == nil {
			t.Fatal("rules = nil")
		}
		allow, deny := rules.Lists()
		if len(allow) != 1 || allow[0] != "ls *" || len(deny) != 1 || deny[0] != "rm -rf *" {
			t.Fatalf("allow = %v, deny = %v", allow, deny)
		}
	})

	t.Run("other tools and other top-level keys are ignored", func(t *testing.T) {
		rules, err := loadSettingsFile(write(t, `{"permissions":{"write":{"allow":["*"]}},"other":1}`), false)
		if err != nil {
			t.Fatal(err)
		}
		if rules != nil {
			t.Fatalf("rules = %+v, want nil", rules)
		}
	})

	t.Run("absent probed file yields no rules", func(t *testing.T) {
		rules, err := loadSettingsFile(filepath.Join(t.TempDir(), "nope.json"), false)
		if err != nil || rules != nil {
			t.Fatalf("rules = %+v, err = %v; want nil, nil", rules, err)
		}
	})

	t.Run("absent explicit file is an error", func(t *testing.T) {
		if _, err := loadSettingsFile(filepath.Join(t.TempDir(), "nope.json"), true); err == nil {
			t.Fatal("want an error for a missing --settings path")
		}
	})

	t.Run("malformed JSON is an error", func(t *testing.T) {
		if _, err := loadSettingsFile(write(t, `{"bash":`), false); err == nil {
			t.Fatal("want an error for malformed JSON")
		}
	})
}

// applyBashRules layers onto tenzing.yaml's policy, or the default when it
// named no permissions: section.
func TestApplyBashRules(t *testing.T) {
	rules := permissions.NewBashRules([]string{"ls *"}, nil)

	t.Run("nil rules still yield an empty live policy and a store", func(t *testing.T) {
		cfg := &cliConfig{}
		applyBashRules(cfg, "settings.json", nil)
		if cfg.PermissionPolicy == nil || cfg.PermissionPolicy.Bash == nil {
			t.Fatal("want empty bash rules attached")
		}
		if _, ok := cfg.PermissionPolicy.Bash.Verdict("ls -la"); ok {
			t.Error("want no verdict from empty rules")
		}
		if cfg.BashAllow == nil || cfg.BashAllow.Path() != "settings.json" {
			t.Errorf("store = %+v", cfg.BashAllow)
		}
	})

	t.Run("layers onto the default policy", func(t *testing.T) {
		cfg := &cliConfig{}
		applyBashRules(cfg, "settings.json", rules)
		if cfg.PermissionPolicy == nil || cfg.PermissionPolicy.Bash != rules {
			t.Fatalf("policy = %+v", cfg.PermissionPolicy)
		}
		if len(cfg.PermissionPolicy.Ask) == 0 {
			t.Error("want the default policy's Ask list preserved")
		}
	})

	t.Run("layers onto a configured policy", func(t *testing.T) {
		cfg := &cliConfig{PermissionPolicy: &permissions.Policy{Deny: []string{"write"}}}
		applyBashRules(cfg, "settings.json", rules)
		if cfg.PermissionPolicy.Bash != rules {
			t.Fatal("want the bash rules attached")
		}
		if len(cfg.PermissionPolicy.Deny) != 1 || cfg.PermissionPolicy.Deny[0] != "write" {
			t.Errorf("want the configured Deny list preserved, got %v", cfg.PermissionPolicy.Deny)
		}
	})
}

// Tool names in the permissions map are matched case-insensitively, and a
// rewrite keeps the spelling the file already used rather than adding a
// second entry differing only in case.
func TestSettingsToolNameCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"permissions":{"Bash":{"allow":["ls *"],"deny":[]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	rules, err := loadSettingsFile(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if rules == nil {
		t.Fatal(`"Bash" key was ignored; want it read as the bash rules`)
	}
	if allow, _ := rules.Lists(); len(allow) != 1 || allow[0] != "ls *" {
		t.Fatalf("allow = %v", allow)
	}

	store := app.NewBashAllowStore(path, rules)
	if err := store.Add("head *"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Permissions map[string]json.RawMessage `json:"permissions"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Permissions) != 1 {
		t.Fatalf("permissions = %v; want the original key reused, not a duplicate", doc.Permissions)
	}
	if _, ok := doc.Permissions["Bash"]; !ok {
		t.Errorf(`permissions keys = %v; want "Bash" kept`, doc.Permissions)
	}
	if !strings.Contains(string(data), "head *") {
		t.Errorf("settings = %s; want the new glob persisted", data)
	}
}
