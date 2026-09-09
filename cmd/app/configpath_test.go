package main

import (
	"os"
	"path/filepath"
	"testing"
)

// resolveConfigPath's fallback chain: --config > TENZING_CONFIG > ./tenzing.yaml
// > <UserConfigDir>/tenzing/tenzing.yaml.
func TestResolveConfigPath(t *testing.T) {
	writeUserConfig := func(t *testing.T) string {
		t.Helper()
		dir := userConfigDir()
		if dir == "" {
			t.Fatal("userConfigDir() is empty")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "tenzing.yaml")
		if err := os.WriteFile(p, []byte("model: ollama/x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	tests := []struct {
		name         string
		flagValue    string
		flagChanged  bool
		env          string
		localConfig  bool
		userConfig   bool
		wantPath     func(local, user string) string
		wantExplicit bool
	}{
		{
			name: "flag wins over everything", flagValue: "custom.yaml", flagChanged: true,
			env: "env.yaml", localConfig: true, userConfig: true,
			wantPath:     func(local, user string) string { return "custom.yaml" },
			wantExplicit: true,
		},
		{
			name: "env wins over local and user", env: "env.yaml",
			localConfig: true, userConfig: true,
			wantPath:     func(local, user string) string { return "env.yaml" },
			wantExplicit: true,
		},
		{
			name: "local config wins over user config", localConfig: true, userConfig: true,
			wantPath:     func(local, user string) string { return defaultConfigPath },
			wantExplicit: false,
		},
		{
			name: "falls back to user config when no local", userConfig: true,
			wantPath:     func(local, user string) string { return user },
			wantExplicit: false,
		},
		{
			name:         "no config anywhere returns the ambient default",
			wantPath:     func(local, user string) string { return defaultConfigPath },
			wantExplicit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := t.TempDir()
			t.Chdir(cwd)
			redirectUserConfig(t)
			t.Setenv("TENZING_CONFIG", tt.env)

			if tt.localConfig {
				if err := os.WriteFile(filepath.Join(cwd, defaultConfigPath), []byte("model: ollama/y\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var user string
			if tt.userConfig {
				user = writeUserConfig(t)
			}

			path, explicit := resolveConfigPath(tt.flagValue, tt.flagChanged)
			if want := tt.wantPath(defaultConfigPath, user); path != want {
				t.Errorf("path = %q, want %q", path, want)
			}
			if explicit != tt.wantExplicit {
				t.Errorf("explicit = %v, want %v", explicit, tt.wantExplicit)
			}
		})
	}
}

// redirectUserConfig points os.UserConfigDir() at a temp directory, so a
// probe of the per-user config never finds the developer's own files. HOME
// covers macOS (~/Library/Application Support) and the XDG fallback; the
// empty XDG_CONFIG_HOME keeps a set variable from winning on Linux.
func redirectUserConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
}
