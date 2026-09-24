package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

// askUserTool is connect mode's ask_user: the agent puts a question to the
// plane's user and its turn blocks until the answer comes back, which is
// returned to the model as the tool's result. Nothing else in the harness
// can reach a person mid-turn — AskUser is a permission decision, not a
// question.
//
// It reports read-only so asking never trips the advisor's write-gate:
// a question changes nothing.
type askUserTool struct {
	// send puts one request on the wire; the id is what the answer quotes.
	send func(id, question string)
	// prefix makes ids unique across processes: the plane sees every
	// attempt of a node, and a counter alone re-issued ask-1 on each.
	prefix string

	n       atomic.Int64
	mu      sync.Mutex
	pending map[string]chan string
}

var _ tooldef.Definition = (*askUserTool)(nil)

func newAskUserTool(send func(id, question string)) *askUserTool {
	b := make([]byte, 6)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go 1.24+)
	return &askUserTool{send: send, prefix: hex.EncodeToString(b), pending: map[string]chan string{}}
}

func (t *askUserTool) Name() string   { return "ask_user" }
func (t *askUserTool) ReadOnly() bool { return true }

func (t *askUserTool) Description() string {
	return "Ask the user a question and wait for their answer. Use it when you need a decision " +
		"or a fact only the user has; ask one clear question at a time. The answer is returned as this tool's result."
}

func (t *askUserTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{"question": {Type: tooldef.JsonTypeString}},
		Required:   []string{"question"},
	}
}

func (t *askUserTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	var in struct {
		Question string `json:"question"`
	}
	if len(exctx.Arguments) > 0 {
		if err := json.Unmarshal([]byte(exctx.Arguments[0]), &in); err != nil {
			return tooldef.NewToolResult(fmt.Sprintf("invalid input JSON: %v", err), tooldef.WithError()), nil
		}
	}
	if strings.TrimSpace(in.Question) == "" {
		return tooldef.NewToolResult("question is required", tooldef.WithError()), nil
	}

	id := "ask-" + t.prefix + "-" + strconv.FormatInt(t.n.Add(1), 10)
	answer := make(chan string, 1)
	t.mu.Lock()
	t.pending[id] = answer
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
	}()

	t.send(id, in.Question)
	select {
	case text := <-answer:
		return tooldef.NewToolResult("The user answered: " + text), nil
	case <-ctx.Done():
		// The turn was cancelled (or the connection dropped) while waiting.
		return tooldef.NewToolResult("the question was not answered: "+ctx.Err().Error(), tooldef.WithError()), nil
	}
}

// answer delivers the plane's reply to the question it quotes. An unknown
// id — a question whose turn has already ended — is dropped.
func (t *askUserTool) answer(id, text string) {
	t.mu.Lock()
	ch, ok := t.pending[id]
	t.mu.Unlock()
	if ok {
		select {
		case ch <- text:
		default: // already answered
		}
	}
}
