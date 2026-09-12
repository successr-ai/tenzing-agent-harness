package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveProjectTrust(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.json")
	project := filepath.Join(dir, "repo")

	tests := []struct {
		name       string
		seed       func(t *testing.T)
		envDefault string
		wantTrust  bool
		wantSource string
	}{
		{"no file default skip", nil, "skip", false, "default"},
		{"no file env trust", nil, "trust", true, "env"},
		{"no file empty env", nil, "", false, "default"},
		{
			"persisted trusted wins over env skip",
			func(t *testing.T) {
				if err := SetProjectTrust(path, project, true, time.Now()); err != nil {
					t.Fatal(err)
				}
			},
			"skip", true, "persisted",
		},
		{
			"persisted untrusted wins over env trust",
			func(t *testing.T) {
				if err := SetProjectTrust(path, project, false, time.Now()); err != nil {
					t.Fatal(err)
				}
			},
			"trust", false, "persisted",
		},
		{
			"corrupt file untrusted",
			func(t *testing.T) {
				if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			"trust", false, "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Remove(path)
			if tt.seed != nil {
				tt.seed(t)
			}
			trusted, source := ResolveProjectTrust(path, project, tt.envDefault)
			if trusted != tt.wantTrust || source != tt.wantSource {
				t.Errorf("ResolveProjectTrust = (%v, %q), want (%v, %q)", trusted, source, tt.wantTrust, tt.wantSource)
			}
		})
	}
}

// Decisions survive rewrite: setting one dir does not drop others, and the
// file round-trips (the restart-survival property).
func TestSetProjectTrustPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.json")

	if err := SetProjectTrust(path, "/a", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SetProjectTrust(path, "/b", false, time.Now()); err != nil {
		t.Fatal(err)
	}

	m, err := LoadTrustFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 entries, got %d: %#v", len(m), m)
	}
	if !m["/a"].Trusted || m["/b"].Trusted {
		t.Errorf("wrong decisions: %#v", m)
	}
	if _, err := time.Parse(time.RFC3339, m["/a"].Decided); err != nil {
		t.Errorf("decided timestamp not RFC3339: %q", m["/a"].Decided)
	}
}

func TestSetProjectTrustOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")
	if err := SetProjectTrust(path, "/a", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SetProjectTrust(path, "/a", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	trusted, source := ResolveProjectTrust(path, "/a", "trust")
	if trusted || source != "persisted" {
		t.Errorf("expected persisted revocation, got (%v, %q)", trusted, source)
	}
}
