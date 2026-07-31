package nexustools

import (
	"context"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

func seededNexus(t *testing.T) *nexus.Nexus {
	t.Helper()
	n, err := nexus.New(nexus.Config{Channels: []nexus.ChannelConfig{
		{Name: "api", Type: nexus.TypeWebhook, ErrorPattern: nexus.DefaultErrorPattern, BufferSize: 10},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.Ingest("api", "starting up")
	n.Ingest("api", "error: db connection refused")
	n.Ingest("api", "retrying")
	return n
}

func exec(t *testing.T, d tooldef.Definition, input string) core.ToolResult {
	t.Helper()
	res, err := d.Execute(context.Background(), tooldef.ExecutionContext{Arguments: []string{input}})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	return res
}

func TestListChannels(t *testing.T) {
	tool := NewListChannelsTool(seededNexus(t))
	if tool.Name() != "list_channels" {
		t.Errorf("Name = %q", tool.Name())
	}
	res := exec(t, tool, "{}")
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	for _, want := range []string{"api", "webhook", `"count":3`, `"error_count":1`} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output missing %q: %s", want, res.Output)
		}
	}
}

func TestReadChannel(t *testing.T) {
	tool := NewReadChannelTool(seededNexus(t))

	tests := []struct {
		name      string
		input     string
		wantErr   bool
		wantLines []string
		skipLines []string
	}{
		{"all entries", `{"name":"api"}`, false,
			[]string{"starting up", "db connection refused", "retrying"}, nil},
		{"last 1", `{"name":"api","last_n":1}`, false,
			[]string{"retrying"}, []string{"starting up"}},
		{"errors only", `{"name":"api","errors_only":true}`, false,
			[]string{"db connection refused"}, []string{"retrying"}},
		{"unknown channel", `{"name":"nope"}`, true, nil, nil},
		{"missing name", `{}`, true, nil, nil},
		{"bad json", `{{{`, true, nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := exec(t, tool, tt.input)
			if res.IsError != tt.wantErr {
				t.Fatalf("IsError = %v, want %v (output: %s)", res.IsError, tt.wantErr, res.Output)
			}
			for _, w := range tt.wantLines {
				if !strings.Contains(res.Output, w) {
					t.Errorf("output missing %q: %s", w, res.Output)
				}
			}
			for _, s := range tt.skipLines {
				if strings.Contains(res.Output, s) {
					t.Errorf("output should not contain %q: %s", s, res.Output)
				}
			}
		})
	}
}

func TestSearchChannel(t *testing.T) {
	tool := NewSearchChannelTool(seededNexus(t))

	tests := []struct {
		name    string
		input   string
		wantErr bool
		want    string
	}{
		{"match", `{"name":"api","pattern":"db conn.*refused"}`, false, "db connection refused"},
		{"no match ok", `{"name":"api","pattern":"zebra"}`, false, "0 matching"},
		{"bad regex", `{"name":"api","pattern":"(unclosed"}`, true, ""},
		{"missing pattern", `{"name":"api"}`, true, ""},
		{"unknown channel", `{"name":"nope","pattern":"x"}`, true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := exec(t, tool, tt.input)
			if res.IsError != tt.wantErr {
				t.Fatalf("IsError = %v, want %v (output: %s)", res.IsError, tt.wantErr, res.Output)
			}
			if tt.want != "" && !strings.Contains(res.Output, tt.want) {
				t.Errorf("output missing %q: %s", tt.want, res.Output)
			}
		})
	}
}
