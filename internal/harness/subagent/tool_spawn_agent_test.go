package subagent

import (
	"context"
	"errors"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

type stubFactory struct {
	result string
	err    error
}

func (f *stubFactory) SpawnAgent(_ context.Context, task string, ctx string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.result, nil
}

func TestSpawnAgentToolSchema(t *testing.T) {
	tool := NewSpawnAgentTool(&stubFactory{})
	if tool.Name() != "spawn_agent" {
		t.Fatalf("Name() = %q, want %q", tool.Name(), "spawn_agent")
	}
	schema := tool.Schema()
	if _, ok := schema.Properties["task"]; !ok {
		t.Fatal("schema missing 'task' property")
	}
	if _, ok := schema.Properties["context"]; !ok {
		t.Fatal("schema missing 'context' property")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "task" {
		t.Fatalf("Required = %v, want [task]", schema.Required)
	}
}

func TestSpawnAgentToolReturnsAnswer(t *testing.T) {
	tool := NewSpawnAgentTool(&stubFactory{result: "task completed"})
	result, err := tool.Execute(context.Background(), tooldef.ExecutionContext{
		Arguments: []string{`{"task":"write tests","context":"auth module"}`},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", result.Output)
	}
	if result.Output != "task completed" {
		t.Fatalf("got %q, want %q", result.Output, "task completed")
	}
}

func TestSpawnAgentToolFactoryError(t *testing.T) {
	tool := NewSpawnAgentTool(&stubFactory{err: errors.New("exceeded max iterations (30)")})
	result, err := tool.Execute(context.Background(), tooldef.ExecutionContext{
		Arguments: []string{`{"task":"do something"}`},
	})
	if err != nil {
		t.Fatalf("Execute should not return Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}
}

func TestSpawnAgentToolEmptyTask(t *testing.T) {
	tool := NewSpawnAgentTool(&stubFactory{})
	result, err := tool.Execute(context.Background(), tooldef.ExecutionContext{
		Arguments: []string{`{"task":""}`},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for empty task")
	}
}

func TestSpawnAgentToolMissingArgs(t *testing.T) {
	tool := NewSpawnAgentTool(&stubFactory{})
	result, err := tool.Execute(context.Background(), tooldef.ExecutionContext{
		Arguments: []string{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for missing args")
	}
}

func TestSpawnAgentToolNoContext(t *testing.T) {
	called := false
	factory := &capturingFactory{fn: func(task, ctx string) {
		called = true
		if ctx != "" {
			t.Fatalf("expected empty context, got %q", ctx)
		}
	}}
	tool := NewSpawnAgentTool(factory)
	_, _ = tool.Execute(context.Background(), tooldef.ExecutionContext{
		Arguments: []string{`{"task":"do it"}`},
	})
	if !called {
		t.Fatal("factory was not called")
	}
}

type capturingFactory struct {
	fn func(task, ctx string)
}

func (f *capturingFactory) SpawnAgent(_ context.Context, task string, ctx string) (string, error) {
	f.fn(task, ctx)
	return "ok", nil
}
