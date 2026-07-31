package blackboard

import (
	"context"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

func TestREPLToolExecute(t *testing.T) {
	bb := newTestBlackboard(t)
	tool := NewREPLTool(bb, "main")

	tests := []struct {
		name      string
		args      []string
		wantErr   bool
		wantInOut string
	}{
		{
			name:    "missing arguments",
			args:    nil,
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			args:    []string{"{not json"},
			wantErr: true,
		},
		{
			name:    "empty code",
			args:    []string{`{"code": ""}`},
			wantErr: true,
		},
		{
			name:      "happy path",
			args:      []string{`{"code": "print(2 + 2)"}`},
			wantInOut: "4",
		},
		{
			name:      "python exception is model-visible output, not tool error",
			args:      []string{`{"code": "raise RuntimeError('kaput')"}`},
			wantInOut: "[Python Error]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := tool.Execute(context.Background(), tooldef.ExecutionContext{Arguments: tt.args})
			if err != nil {
				t.Fatalf("Execute returned Go error: %v", err)
			}
			if res.IsError != tt.wantErr {
				t.Errorf("IsError = %v, want %v (output: %s)", res.IsError, tt.wantErr, res.Output)
			}
			if tt.wantInOut != "" && !strings.Contains(res.Output, tt.wantInOut) {
				t.Errorf("output %q missing %q", res.Output, tt.wantInOut)
			}
		})
	}
}

func TestREPLToolTruncatesLongOutput(t *testing.T) {
	bb := newTestBlackboard(t)
	tool := NewREPLTool(bb, "main")

	res, err := tool.Execute(context.Background(), tooldef.ExecutionContext{
		Arguments: []string{`{"code": "print('y' * 10000)"}`},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "chars omitted") {
		t.Errorf("expected truncation marker, got %d chars", len(res.Output))
	}
	if len(res.Output) > bb.HeadChars()+bb.TailChars()+200 {
		t.Errorf("output not truncated: %d chars", len(res.Output))
	}
}

func TestREPLToolDescriptionNamesOwnSlot(t *testing.T) {
	bb := New(Config{WorkingDir: t.TempDir()})
	tool := NewREPLTool(bb, "a3")
	if !strings.Contains(tool.Description(), "bb['a3']") {
		t.Errorf("description must name the agent's own slot: %s", tool.Description())
	}
	if tool.Name() != "repl" {
		t.Errorf("Name() = %q, want repl", tool.Name())
	}
}
