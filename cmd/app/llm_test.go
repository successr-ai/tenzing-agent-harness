package main

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
)

// TestRootCmdWiresLLMOverrides proves RunE copies --base-url/--api-key into
// the process-wide llm cache before dispatch, so every client built after
// startup sees the overrides.
func TestRootCmdWiresLLMOverrides(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantBaseURL string
		wantAPIKey  string
	}{
		{"both flags passed", []string{"-p", "hi", "--base-url", "https://openrouter.ai/api/v1", "--api-key", "sk-test"}, "https://openrouter.ai/api/v1", "sk-test"},
		{"neither flag passed", []string{"-p", "hi"}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origPrint := runPrintFn
			origBaseURL, origAPIKey := llms.baseURL, llms.apiKey
			t.Cleanup(func() {
				runPrintFn = origPrint
				llms.baseURL, llms.apiKey = origBaseURL, origAPIKey
			})
			runPrintFn = func(_ context.Context, _ *cliConfig, _, _ io.Writer, _ ...harness.HarnessOption) error {
				return nil
			}

			cmd := newRootCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tt.args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute: %v", err)
			}

			if llms.baseURL != tt.wantBaseURL {
				t.Errorf("llms.baseURL = %q, want %q", llms.baseURL, tt.wantBaseURL)
			}
			if llms.apiKey != tt.wantAPIKey {
				t.Errorf("llms.apiKey = %q, want %q", llms.apiKey, tt.wantAPIKey)
			}
		})
	}
}
