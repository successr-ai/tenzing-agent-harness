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

	states := ext.callStates([]*core.ToolCallContext{tcc}, nil, []string{"user: go"})
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
	ext := New(Config{Client: j, Gate: GateConfig{Disabled: true}, RecentMessages: 4})
	ext.SetClassifier(func(string) bool { return false })
	history := []common.Message{common.NewUserMessage("fix the bug")}
	ext.SetMessages(func(context.Context) ([]common.Message, error) { return history, nil })

	if err := ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if err := ext.OnToolBatch(context.Background(), []*core.ToolCallContext{call("c1", "advisor"), call("c2", "edit")}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	history = append(history,
		common.Message{Role: common.RoleAssistant, Content: []common.ContentBlock{
			{Type: common.ContentTypeText, Text: "The advisor agrees; editing now."},
			common.NewToolUseContent("t1", "edit", []byte(`{}`)),
		}},
		common.Message{Role: common.RoleUser, Content: []common.ContentBlock{
			common.NewToolResultContent("t1", "edit", "edited x.go"),
		}},
	)
	if err := ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 2}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	state, ok := j.reqs[1].State.(turnState)
	if !ok {
		t.Fatalf("unexpected state shape: %#v", j.reqs[1].State)
	}
	if state.Request != "fix the bug" {
		t.Fatalf("request = %q, want the turn's opening message", state.Request)
	}
	if len(state.AssistantMessages) != 1 || state.AssistantMessages[0] != "The advisor agrees; editing now." {
		t.Fatalf("assistant messages = %q, want the prose only — no tool calls or results", state.AssistantMessages)
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
	_ = ext.OnToolBatch(context.Background(), []*core.ToolCallContext{call("c1", "advisor")})
	_ = ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1}) // a new turn

	state := j.reqs[1].State.(turnState)
	if state.AdvisorConsults != 0 {
		t.Fatalf("a new turn starts with no consults, got %d", state.AdvisorConsults)
	}
}

// The tail's N slots go to what the agent said: a message that only called
// tools takes none, so a long run of calls cannot push the prose out.
func TestAssistantMessagesSkipToolOnlyTurns(t *testing.T) {
	toolOnly := common.Message{Role: common.RoleAssistant, Content: []common.ContentBlock{
		common.NewToolUseContent("t", "read", []byte(`{}`)),
	}}
	history := []common.Message{
		common.NewUserMessage("go"),
		common.NewAssistantMessage("one"),
		toolOnly,
		common.NewAssistantMessage("two"),
		toolOnly, toolOnly, toolOnly,
		common.NewAssistantMessage("three"),
		toolOnly,
	}
	ext := New(Config{Client: &fakeJudge{}, RecentMessages: 2})
	ext.SetMessages(func(context.Context) ([]common.Message, error) { return history, nil })
	got := strings.Join(ext.assistantMessages(context.Background()), "|")
	if got != "two|three" {
		t.Fatalf("assistant messages = %q, want the last two with text, oldest first", got)
	}
}

// The whole point of the path facts: they reach the state the model judges.
func TestCallStateCarriesPathFacts(t *testing.T) {
	work := t.TempDir()
	ext := New(Config{Client: &fakeJudge{}, WorkingDir: work, Advisor: AdvisorConfig{Disabled: true}})
	ext.SetShellSplitter(func(cmd string) []string { return strings.Fields(cmd) })

	tcc := call("c1", "bash")
	tcc.Call.Input = `{"command":"rm -rf /etc/passwd"}`
	state, ok := ext.callStates([]*core.ToolCallContext{tcc}, map[string][]pathFact{"c1": ext.pathsFor(*tcc.Call)}, nil)["c1"].(callState)
	if !ok {
		t.Fatalf("unexpected state shape")
	}
	if len(state.ToolCall.Paths) != 1 {
		t.Fatalf("paths = %+v, want the one operand", state.ToolCall.Paths)
	}
	got := state.ToolCall.Paths[0]
	if !got.OutsideWorkingDirectory || got.InTempDirectory {
		t.Fatalf("/etc/passwd = %+v, want outside and not temp", got)
	}
}

// The turn's request stays in every tail. A rolling window loses it after a
// few tool calls — observed live on a retry, where the tail held two calls, a
// result and a denial, and the question "is this outside what the user asked
// for?" was judged against nothing (0.43); with the request pinned back in,
// 0.14.
func TestRequestIsPinnedIntoTheTail(t *testing.T) {
	history := []common.Message{
		common.NewUserMessage("Delete the backup directory."),
	}
	ext := New(Config{Client: &fakeJudge{answers: map[string]common.Answer{}}, RecentMessages: 3, Advisor: AdvisorConfig{Disabled: true}})
	ext.SetMessages(func(context.Context) ([]common.Message, error) { return history, nil })

	// Iteration 1: the store holds the request; the hook records it.
	if err := ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	// The turn runs on and the request rolls out of the last-3 window.
	history = append(history,
		common.NewAssistantMessage("[called ls]"),
		common.Message{Role: common.RoleTool, Content: []common.ContentBlock{common.NewTextContent("backup/ hello.txt")}},
		common.NewAssistantMessage("[called bash]"),
		common.Message{Role: common.RoleTool, Content: []common.ContentBlock{common.NewTextContent("approval timed out")}},
	)
	got := ext.recentMessages(context.Background())
	if len(got) != 3 {
		t.Fatalf("tail = %d lines, want the window size (3): %v", len(got), got)
	}
	if got[0] != "user: Delete the backup directory." {
		t.Fatalf("tail[0] = %q, want the pinned request", got[0])
	}
	if got[2] != "tool: approval timed out" {
		t.Fatalf("tail must keep the newest messages after the request: %v", got)
	}
}

// When the request is still inside the window it is not duplicated.
func TestRequestNotDuplicatedWhenStillInTheTail(t *testing.T) {
	history := []common.Message{common.NewUserMessage("go"), common.NewAssistantMessage("ok")}
	ext := New(Config{Client: &fakeJudge{answers: map[string]common.Answer{}}, RecentMessages: 4, Advisor: AdvisorConfig{Disabled: true}})
	ext.SetMessages(func(context.Context) ([]common.Message, error) { return history, nil })
	_ = ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1})
	got := ext.recentMessages(context.Background())
	if len(got) != 2 || got[0] != "user: go" {
		t.Fatalf("tail = %v, want the two messages once", got)
	}
}

// A new turn re-pins: the previous turn's request must not leak forward.
func TestRequestRepinnedEachTurn(t *testing.T) {
	history := []common.Message{common.NewUserMessage("first task")}
	ext := New(Config{Client: &fakeJudge{answers: map[string]common.Answer{}}, RecentMessages: 1, Advisor: AdvisorConfig{Disabled: true}})
	ext.SetMessages(func(context.Context) ([]common.Message, error) { return history, nil })
	_ = ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1})
	history = append(history, common.NewAssistantMessage("done"), common.NewUserMessage("second task"))
	_ = ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1})
	got := ext.recentMessages(context.Background())
	if len(got) != 1 || got[0] != "user: second task" {
		t.Fatalf("tail = %v, want only the new turn's request", got)
	}
}
