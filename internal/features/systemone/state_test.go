package systemone

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func conversation() []common.Message {
	return []common.Message{
		common.NewUserMessage("first"),
		common.NewAssistantMessage("second"),
		common.NewUserMessage("third"),
	}
}

func TestRecentMessagesTakesTheTail(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want []string
	}{
		{"zero sends nothing", 0, nil},
		{"one takes the last message", 1, []string{"user: third"}},
		{"more than there are takes them all", 10, []string{"user: first", "assistant: second", "user: third"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := New(Config{Client: &fakeJudge{}, RecentMessages: tt.n})
			ext.SetMessages(func(context.Context) ([]common.Message, error) { return conversation(), nil })
			got := ext.recentMessages(context.Background())
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestRecentMessagesDegradesToNothing(t *testing.T) {
	ext := New(Config{Client: &fakeJudge{}, RecentMessages: 4})
	if got := ext.recentMessages(context.Background()); got != nil {
		t.Fatalf("an unbound source yields nothing, got %v", got)
	}
	ext.SetMessages(func(context.Context) ([]common.Message, error) {
		return nil, errors.New("store unavailable")
	})
	if got := ext.recentMessages(context.Background()); got != nil {
		t.Fatalf("a failing source yields nothing, got %v", got)
	}
}

func TestRenderMessageFlattensBlocks(t *testing.T) {
	msg := common.Message{Role: common.RoleAssistant, Content: []common.ContentBlock{
		common.NewTextContent("looking"),
		common.NewThinkingContent("secret reasoning"),
		common.NewToolUseContent("t1", "read", []byte(`{}`)),
	}}
	got := renderMessage(msg)
	if !strings.Contains(got, "assistant: looking") || !strings.Contains(got, "[called read]") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "secret reasoning") {
		t.Fatalf("chain-of-thought must not travel: %q", got)
	}
}

func TestLongTextIsTruncated(t *testing.T) {
	long := strings.Repeat("x", maxMessageChars+500)
	got := renderMessage(common.NewUserMessage(long))
	if len(got) > maxMessageChars+len("user: ")+len("…[truncated]") {
		t.Fatalf("message not truncated, length %d", len(got))
	}
	if !strings.HasSuffix(got, "[truncated]") {
		t.Fatal("truncation must be visible to the model")
	}
}

func TestCallStateCarriesWhatTheQuestionsName(t *testing.T) {
	ext := New(Config{Client: &fakeJudge{}, Advisor: AdvisorConfig{Disabled: true}})
	ext.SetClassifier(func(name string) bool { return name == "read" })
	tcc := call("c1", "read")
	tcc.Decision = core.AskUser

	states := ext.callStates([]*core.ToolCallContext{tcc}, []string{"user: go"})
	state, ok := states["c1"].(callState)
	if !ok {
		t.Fatalf("state must be keyed by call id: %#v", states)
	}
	if state.ToolCall.Name != "read" || !state.ToolCall.ReadOnly {
		t.Fatalf("tool facts missing: %+v", state.ToolCall)
	}
	if state.ToolCall.Decision != "ask the user" {
		t.Fatalf("the policy's own decision must travel, got %q", state.ToolCall.Decision)
	}
	if len(state.RecentMessages) != 1 {
		t.Fatalf("the tail must ride along: %+v", state.RecentMessages)
	}
}

func TestTurnStateReportsWhatTheAgentHasBeenDoing(t *testing.T) {
	j := &fakeJudge{answers: map[string]common.Answer{qAdvisor: noul(0.1)}}
	ext := New(Config{Client: j, Gate: GateConfig{Disabled: true}})
	ext.SetClassifier(func(string) bool { return false })

	if err := ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if err := ext.OnToolBatch(context.Background(), []*core.ToolCallContext{call("c1", "advisor"), call("c2", "edit")}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if err := ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 2}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	state, ok := j.reqs[1].State.(turnState)
	if !ok {
		t.Fatalf("unexpected state shape: %#v", j.reqs[1].State)
	}
	if len(state.RecentTools) != 2 || state.RecentTools[0] != "advisor" {
		t.Fatalf("tool log = %v, want the turn's calls in order", state.RecentTools)
	}
	if state.AdvisorConsults != 1 {
		t.Fatalf("consults = %d, want 1", state.AdvisorConsults)
	}
	if state.Iteration != 2 {
		t.Fatalf("iteration = %d, want 2", state.Iteration)
	}
}

func TestTurnStateResetsEachTurn(t *testing.T) {
	j := &fakeJudge{answers: map[string]common.Answer{qAdvisor: noul(0.1)}}
	ext := New(Config{Client: j, Gate: GateConfig{Disabled: true}})
	ext.SetClassifier(func(string) bool { return false })

	_ = ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1})
	_ = ext.OnToolBatch(context.Background(), []*core.ToolCallContext{call("c1", "edit")})
	_ = ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1}) // a new turn

	state := j.reqs[1].State.(turnState)
	if len(state.RecentTools) != 0 {
		t.Fatalf("a new turn starts with a clean log, got %v", state.RecentTools)
	}
}

func TestRecentToolsAreBounded(t *testing.T) {
	ext := New(Config{Client: &fakeJudge{}})
	batch := make([]*core.ToolCallContext, maxRecentTools+5)
	for i := range batch {
		batch[i] = call("c", "read")
	}
	ext.observe(batch)
	if len(ext.recentTools) != maxRecentTools {
		t.Fatalf("tool log = %d entries, want it capped at %d", len(ext.recentTools), maxRecentTools)
	}
}
