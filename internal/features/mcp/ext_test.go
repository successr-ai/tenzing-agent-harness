package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

type echoArgs struct {
	Text string `json:"text"`
}

type addArgs struct {
	A int `json:"a"`
	B int `json:"b"`
}

// newTestExt wires the extension to an in-process MCP server over in-memory
// transports and returns both, connected via SessionStart.
func newTestExt(t *testing.T) (*Ext, *mcp.ServerSession) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo text"},
		func(_ context.Context, _ *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + args.Text}}}, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "add", Description: "add numbers"},
		func(_ context.Context, _ *mcp.CallToolRequest, args addArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "7"}}}, nil, nil
		})

	clientT, serverT := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}

	ext := New(ServerConfig{Name: "test"})
	ext.connect = func(ctx context.Context, _ ServerConfig) (*mcp.ClientSession, error) {
		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
		return client.Connect(ctx, clientT, nil)
	}
	if err := ext.SessionStart(context.Background()); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	t.Cleanup(func() { _ = ext.SessionEnd(context.Background()) })
	return ext, serverSession
}

func TestCurrentToolsPrefixedAndOriginTagged(t *testing.T) {
	ext, _ := newTestExt(t)

	specs := ext.CurrentTools(context.Background())
	if len(specs) != 2 {
		t.Fatalf("tools = %d, want 2", len(specs))
	}
	names := map[string]bool{}
	for _, s := range specs {
		names[s.Definition.Name] = true
		if s.Origin != "mcp:test" {
			t.Errorf("origin = %q, want mcp:test", s.Origin)
		}
		if len(s.Definition.InputSchema) == 0 {
			t.Errorf("%s has empty input schema", s.Definition.Name)
		}
	}
	for _, want := range []string{"mcp__test__echo", "mcp__test__add"} {
		if !names[want] {
			t.Errorf("missing tool %q in %v", want, names)
		}
	}
}

func TestExecuteRoundTrips(t *testing.T) {
	ext, _ := newTestExt(t)

	var echo func(ctx context.Context, call core.ToolCall) core.ToolResult
	for _, s := range ext.CurrentTools(context.Background()) {
		if s.Definition.Name == "mcp__test__echo" {
			echo = s.Execute
		}
	}
	if echo == nil {
		t.Fatal("echo spec not found")
	}

	res := echo(context.Background(), core.ToolCall{ID: "c1", Name: "mcp__test__echo", Input: `{"text":"hello"}`})
	if res.IsError {
		t.Fatalf("Execute errored: %s", res.Output)
	}
	if !strings.Contains(res.Output, "echo: hello") {
		t.Errorf("output = %q, want echoed text", res.Output)
	}
	if res.ToolUseID != "c1" {
		t.Errorf("ToolUseID = %q, want c1", res.ToolUseID)
	}
}

func TestDeadServerServesNoToolsWithoutPanic(t *testing.T) {
	ext, serverSession := newTestExt(t)

	if got := len(ext.CurrentTools(context.Background())); got != 2 {
		t.Fatalf("tools before death = %d, want 2", got)
	}

	_ = serverSession.Close()
	// bust the list cache so the next call re-polls the dead server
	ext.mu.Lock()
	for _, c := range ext.conns {
		c.mu.Lock()
		c.fetchedAt = time.Time{}
		c.mu.Unlock()
	}
	ext.mu.Unlock()

	if got := len(ext.CurrentTools(context.Background())); got != 0 {
		t.Errorf("tools after server death = %d, want 0", got)
	}
}
