package main

import (
	"strings"
	"testing"
	"time"

	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
)

// --http-timeout replaces every provider's http_timeout, --provider entries
// included; left unset, each provider keeps its own.
func TestBuildDepsHTTPTimeout(t *testing.T) {
	own := cfgfile.Duration(45 * time.Second)
	providers := []cfgfile.Provider{{Name: "local", Type: "ollama", URL: "http://localhost:11434", HTTPTimeout: &own}}
	models := cfgfile.ModelsSection{LLM: []cfgfile.ModelEntry{
		{Name: "a", Provider: "local", ModelName: "glm-5.3"},
		{Name: "b", Provider: "flagged", ModelName: "glm-5.3"},
	}}
	flagged := []string{`{"name":"flagged","type":"ollama","url":"http://box:11434"}`}
	zero, ninety := time.Duration(0), 90*time.Second

	tests := []struct {
		name  string
		flag  *time.Duration
		wantA time.Duration
		wantB time.Duration
	}{
		{"flag unset keeps each provider's own", nil, 45 * time.Second, 0},
		{"flag overrides every provider", &ninety, 90 * time.Second, 90 * time.Second},
		{"explicit zero turns them all off", &zero, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := buildDeps(providers, models, flagged, tt.flag)
			if err != nil {
				t.Fatal(err)
			}
			for alias, want := range map[string]time.Duration{"a": tt.wantA, "b": tt.wantB} {
				rm, err := d.resolve(alias)
				if err != nil {
					t.Fatal(err)
				}
				if got := rm.Provider.HTTPTimeout.Value(); got != want {
					t.Errorf("%s: http timeout = %v, want %v", alias, got, want)
				}
			}
		})
	}
	if got := own.Value(); got != 45*time.Second {
		t.Errorf("override mutated the caller's provider: %v", got)
	}
}

func TestBuildDepsRejectsNegativeHTTPTimeout(t *testing.T) {
	neg := -time.Second
	_, err := buildDeps(nil, cfgfile.ModelsSection{}, nil, &neg)
	if err == nil || !strings.Contains(err.Error(), "--http-timeout") {
		t.Fatalf("err = %v, want a --http-timeout error", err)
	}
}
