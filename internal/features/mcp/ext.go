// Package mcp mounts external MCP servers as a core.DynamicToolSource:
// each configured server's tools appear as "mcp__<server>__<tool>" with
// Origin "mcp:<server>", re-listed at each turn boundary (with a short
// cache). Connections are opened on SessionStart and closed on SessionEnd.
//
// Tool-list change notifications (listChanged subscription) are a follow-on;
// v1 polls ListTools through the CurrentTools cache below.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// toolListCacheTTL bounds how often CurrentTools re-polls a server's tool
// list. BeginTurn calls CurrentTools once per turn; the cache keeps rapid
// turns from hammering slow servers.
const toolListCacheTTL = 30 * time.Second

// ServerConfig describes one stdio-transport MCP server. SSE/HTTP transports
// are a follow-on.
type ServerConfig struct {
	Name    string
	Command string
	Args    []string
	Env     []string // appended to the current process environment
}

var (
	_ core.Extension         = (*Ext)(nil)
	_ core.SessionStartHook  = (*Ext)(nil)
	_ core.SessionEndHook    = (*Ext)(nil)
	_ core.DynamicToolSource = (*Ext)(nil)
)

type Ext struct {
	configs []ServerConfig
	// connect is the transport seam: tests swap it for an in-memory pair.
	connect func(ctx context.Context, cfg ServerConfig) (*mcp.ClientSession, error)

	mu    sync.Mutex
	conns []*serverConn
}

type serverConn struct {
	name    string
	session *mcp.ClientSession

	mu        sync.Mutex
	cached    []core.ToolSpec
	fetchedAt time.Time
}

func New(servers ...ServerConfig) *Ext {
	return &Ext{configs: servers, connect: connectStdio}
}

func connectStdio(ctx context.Context, cfg ServerConfig) (*mcp.ClientSession, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = append(os.Environ(), cfg.Env...)
	client := mcp.NewClient(&mcp.Implementation{Name: "tenzing-agent-harness", Version: "v1"}, nil)
	return client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
}

func (e *Ext) Name() string { return "mcp" }

// SessionStart connects every configured server. A dead server must not kill
// the harness: connection failures log a warning and that server serves zero
// tools. // ponytail: deliberate deviation from load-bearing SessionStart.
func (e *Ext) SessionStart(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, cfg := range e.configs {
		session, err := e.connect(ctx, cfg)
		if err != nil {
			slog.Warn("mcp server connect failed; serving no tools from it", "server", cfg.Name, "error", err)
			continue
		}
		slog.Info("mcp server connected", "server", cfg.Name)
		e.conns = append(e.conns, &serverConn{name: cfg.Name, session: session})
	}
	return nil
}

func (e *Ext) SessionEnd(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.conns {
		if err := c.session.Close(); err != nil {
			slog.Warn("mcp server close failed", "server", c.name, "error", err)
		}
	}
	e.conns = nil
	return nil
}

// CurrentTools returns every connected server's tools, prefixed and
// origin-tagged. Lists are cached per server for toolListCacheTTL; a listing
// failure (dead server) serves zero tools from that server, never a panic.
func (e *Ext) CurrentTools(ctx context.Context) []core.ToolSpec {
	e.mu.Lock()
	conns := append([]*serverConn{}, e.conns...)
	e.mu.Unlock()

	var specs []core.ToolSpec
	for _, c := range conns {
		specs = append(specs, c.currentTools(ctx)...)
	}
	return specs
}

func (c *serverConn) currentTools(ctx context.Context) []core.ToolSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.fetchedAt) < toolListCacheTTL {
		return c.cached
	}

	res, err := c.session.ListTools(ctx, nil)
	if err != nil {
		slog.Warn("mcp list tools failed; serving no tools from server", "server", c.name, "error", err)
		c.cached = nil
		c.fetchedAt = time.Now()
		return nil
	}

	specs := make([]core.ToolSpec, 0, len(res.Tools))
	for _, tool := range res.Tools {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			slog.Warn("mcp tool schema marshal failed; skipping tool", "server", c.name, "tool", tool.Name, "error", err)
			continue
		}
		specs = append(specs, core.ToolSpec{
			Definition: core.ProviderToolDefinition{
				Name:        fmt.Sprintf("mcp__%s__%s", c.name, tool.Name),
				Description: tool.Description,
				InputSchema: schema,
			},
			Origin:  "mcp:" + c.name,
			Execute: c.executeFn(tool.Name),
		})
	}
	c.cached = specs
	c.fetchedAt = time.Now()
	return specs
}

func (c *serverConn) executeFn(toolName string) func(ctx context.Context, call core.ToolCall) core.ToolResult {
	return func(ctx context.Context, call core.ToolCall) core.ToolResult {
		args := map[string]any{}
		if strings.TrimSpace(call.Input) != "" {
			if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
				return core.ToolResult{ToolUseID: call.ID, Output: fmt.Sprintf("invalid tool arguments: %v", err), IsError: true}
			}
		}
		res, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: args})
		if err != nil {
			return core.ToolResult{ToolUseID: call.ID, Output: fmt.Sprintf("mcp call failed: %v", err), IsError: true}
		}
		var out strings.Builder
		for _, content := range res.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				if out.Len() > 0 {
					out.WriteString("\n")
				}
				out.WriteString(text.Text)
			}
		}
		return core.ToolResult{ToolUseID: call.ID, Output: out.String(), IsError: res.IsError}
	}
}
