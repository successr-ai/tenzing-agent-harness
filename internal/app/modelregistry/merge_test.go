package modelregistry

import (
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
)

func TestMergeProviderFlags(t *testing.T) {
	file := []config.Provider{
		{Name: "local", Type: "ollama", URL: "http://localhost:11434"},
		{Name: "claude", Type: "anthropic", URL: "https://api.anthropic.com"},
	}

	t.Run("no flags leaves the file list alone", func(t *testing.T) {
		got, err := MergeProviderFlags(file, nil)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("got %d providers, want 2", len(got))
		}
	})

	t.Run("matching name replaces in place", func(t *testing.T) {
		got, err := MergeProviderFlags(file,
			[]string{`{"name":"local","type":"ollama","url":"http://box:11434","api_key":"sk-x"}`})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d providers, want 2 (replace, not append)", len(got))
		}
		if got[0].Name != "local" || got[0].URL != "http://box:11434" || got[0].APIKey != "sk-x" {
			t.Errorf("not replaced in place: %+v", got[0])
		}
		// The file list is the caller's; merging must not scribble on it.
		if file[0].URL != "http://localhost:11434" {
			t.Errorf("file list mutated: %+v", file[0])
		}
	})

	t.Run("omitted type defaults to openai_compat", func(t *testing.T) {
		got, err := MergeProviderFlags(file,
			[]string{`{"name":"groq","url":"https://api.groq.com/openai/v1"}`})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if got[2].Type != config.DefaultProviderType {
			t.Errorf("flag provider type = %q, want the default", got[2].Type)
		}
	})

	t.Run("new name is appended", func(t *testing.T) {
		got, err := MergeProviderFlags(file,
			[]string{`{"name":"cloud","type":"ollama","url":"https://ollama.com/"}`})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if len(got) != 3 || got[2].Name != "cloud" {
			t.Errorf("not appended: %+v", got)
		}
	})

	t.Run("invalid flags rejected", func(t *testing.T) {
		tests := []struct {
			name    string
			flag    string
			wantErr string
		}{
			{"malformed", `{not json`, "--provider[0]"},
			{"no name", `{"type":"ollama","url":"http://x"}`, "name is required"},
			{"unknown type", `{"name":"n","type":"llamafile","url":"http://x"}`, "unknown type"},
			{"no url on compat", `{"name":"n"}`, "url is required for openai_compat"},
			{"vendor name is not a type", `{"name":"n","type":"openrouter","url":"http://x"}`, "unknown type"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := MergeProviderFlags(file, []string{tt.flag})
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
			})
		}
	})
}