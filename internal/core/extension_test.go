package core

import (
	"context"
	"errors"
	"testing"
)

type fakeExt struct {
	name        string
	beforeErr   error
	callErr     error
	resultErr   error
	afterErr    error
	sawBefore   int
	sawCall     int
	sawResult   int
	sawAfter    int
	setDecision Decision
	setReason   string
	transform   string
}

func (f *fakeExt) Name() string { return f.name }
func (f *fakeExt) BeforeIteration(ctx context.Context, tc *TurnContext) error {
	f.sawBefore++
	tc.Reminders = append(tc.Reminders, "reminder-"+f.name)
	return f.beforeErr
}
func (f *fakeExt) OnToolCall(ctx context.Context, tcc *ToolCallContext) error {
	f.sawCall++
	if f.setDecision != Allow {
		tcc.Decision = f.setDecision
		tcc.Reason = f.setReason
	}
	return f.callErr
}
func (f *fakeExt) OnToolResult(ctx context.Context, trc *ToolResultContext) error {
	f.sawResult++
	if f.transform != "" {
		trc.Result.Output = f.transform
	}
	return f.resultErr
}
func (f *fakeExt) AfterTurn(ctx context.Context, tr *TurnResult) error {
	f.sawAfter++
	return f.afterErr
}

// nameOnlyExt implements no hooks — must register into zero buckets.
type nameOnlyExt struct{}

func (nameOnlyExt) Name() string { return "bare" }

func TestExtensionsBucketing(t *testing.T) {
	full := &fakeExt{name: "full"}
	exts := NewExtensions(full, nameOnlyExt{})
	tc := &TurnContext{}
	if err := exts.RunBeforeIteration(context.Background(), tc); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if full.sawBefore != 1 || len(tc.Reminders) != 1 {
		t.Fatalf("hook not run or reminder missing: %+v", tc)
	}
}

func TestPreHookErrorBlocks(t *testing.T) {
	failing := &fakeExt{name: "f", callErr: errors.New("boom")}
	after := &fakeExt{name: "after"}
	exts := NewExtensions(failing, after)
	tcc := &ToolCallContext{Call: &ToolCall{Name: "bash"}}
	err := exts.RunToolCall(context.Background(), tcc)
	if err == nil {
		t.Fatal("pre-hook error must propagate")
	}
	if after.sawCall != 0 {
		t.Fatal("later hooks must not run after a pre-hook error")
	}
}

func TestPostHookErrorDegrades(t *testing.T) {
	failing := &fakeExt{name: "f", resultErr: errors.New("boom"), transform: "mangled"}
	exts := NewExtensions(failing)
	r := &ToolResult{Output: "original"}
	exts.RunToolResult(context.Background(), &ToolResultContext{Result: r})
	if r.Output != "original" {
		t.Fatalf("failed transform must roll back, got %q", r.Output)
	}
	tr := &TurnResult{}
	failing.afterErr = errors.New("boom")
	exts.RunAfterTurn(context.Background(), tr) // must not panic, returns nothing
	if failing.sawAfter != 1 {
		t.Fatal("AfterTurn must still have run")
	}
}

func TestDecisionOrdering(t *testing.T) {
	deny := &fakeExt{name: "policy", setDecision: Deny, setReason: "nope"}
	late := &fakeExt{name: "late"}
	exts := NewExtensions(deny, late)
	tcc := &ToolCallContext{Call: &ToolCall{Name: "bash"}}
	if err := exts.RunToolCall(context.Background(), tcc); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if tcc.Decision != Deny || tcc.Reason != "nope" {
		t.Fatalf("decision lost: %+v", tcc)
	}
	if late.sawCall != 1 {
		t.Fatal("later hooks still run on Deny (they may escalate, never de-escalate)")
	}
}

func TestPostHookErrorRollsBackMetadataMutation(t *testing.T) {
	failing := &metadataMutatingExt{}
	exts := NewExtensions(failing)
	r := &ToolResult{Output: "original", Metadata: map[string]string{"key": "clean"}}
	exts.RunToolResult(context.Background(), &ToolResultContext{Result: r})
	if r.Metadata["key"] != "clean" {
		t.Fatalf("in-place metadata mutation must roll back on hook error, got %q", r.Metadata["key"])
	}
}

type metadataMutatingExt struct{}

func (metadataMutatingExt) Name() string { return "meta-mutator" }
func (metadataMutatingExt) OnToolResult(_ context.Context, trc *ToolResultContext) error {
	trc.Result.Metadata["key"] = "dirty"
	return errors.New("boom")
}

type lowerExt struct{}

func (lowerExt) Name() string { return "lowerer" }
func (lowerExt) OnToolCall(_ context.Context, tcc *ToolCallContext) error {
	tcc.Decision = Allow // attempt to de-escalate
	return nil
}

func TestDecisionCannotBeLowered(t *testing.T) {
	deny := &fakeExt{name: "policy", setDecision: Deny, setReason: "nope"}
	exts := NewExtensions(deny, lowerExt{})
	tcc := &ToolCallContext{Call: &ToolCall{Name: "bash"}}
	if err := exts.RunToolCall(context.Background(), tcc); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if tcc.Decision != Deny {
		t.Fatalf("de-escalation must be restored to Deny, got %v", tcc.Decision)
	}
}

// batchExt records the batch it saw and applies one decision per call by
// name, so a test can escalate some calls of an issue list and not others.
type batchExt struct {
	name     string
	err      error
	saw      [][]string // one entry per OnToolBatch call, tool names in issue order
	decide   map[string]Decision
	reason   string
	sawCalls int // per-call OnToolCall invocations, when also a ToolCallHook
}

func (b *batchExt) Name() string { return b.name }
func (b *batchExt) OnToolBatch(_ context.Context, batch []*ToolCallContext) error {
	names := make([]string, len(batch))
	for i, tcc := range batch {
		names[i] = tcc.Call.Name
		if d, ok := b.decide[tcc.Call.Name]; ok {
			tcc.Decision = d
			tcc.Reason = b.reason
		}
	}
	b.saw = append(b.saw, names)
	return b.err
}

func TestToolBatchHookSeesWholeBatchInIssueOrder(t *testing.T) {
	b := &batchExt{name: "judge", decide: map[string]Decision{"write": AskUser}, reason: "risky"}
	exts := NewExtensions(b)
	batch := []*ToolCallContext{
		{Call: &ToolCall{Name: "read"}},
		{Call: &ToolCall{Name: "write"}},
	}
	if err := exts.RunToolBatch(context.Background(), batch); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(b.saw) != 1 || len(b.saw[0]) != 2 || b.saw[0][0] != "read" || b.saw[0][1] != "write" {
		t.Fatalf("batch not delivered whole, in issue order: %v", b.saw)
	}
	if batch[0].Decision != Allow {
		t.Fatalf("untouched call must keep its decision, got %v", batch[0].Decision)
	}
	if batch[1].Decision != AskUser || batch[1].Reason != "risky" {
		t.Fatalf("escalation lost: %+v", batch[1])
	}
}

// batchLowerExt attempts to de-escalate every call of the batch.
type batchLowerExt struct{}

func (batchLowerExt) Name() string { return "batch-lowerer" }
func (batchLowerExt) OnToolBatch(_ context.Context, batch []*ToolCallContext) error {
	for _, tcc := range batch {
		tcc.Decision = Allow
	}
	return nil
}

func TestToolBatchDecisionCannotBeLowered(t *testing.T) {
	exts := NewExtensions(batchLowerExt{})
	batch := []*ToolCallContext{{Call: &ToolCall{Name: "bash"}, Decision: Deny, Reason: "nope"}}
	if err := exts.RunToolBatch(context.Background(), batch); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if batch[0].Decision != Deny {
		t.Fatalf("de-escalation must be restored, got %v", batch[0].Decision)
	}
}

func TestToolBatchPreHookErrorBlocks(t *testing.T) {
	failing := &batchExt{name: "f", err: errors.New("boom")}
	later := &batchExt{name: "later"}
	exts := NewExtensions(failing, later)
	batch := []*ToolCallContext{{Call: &ToolCall{Name: "bash"}}}
	if err := exts.RunToolBatch(context.Background(), batch); err == nil {
		t.Fatal("pre-hook error must propagate")
	}
	if len(later.saw) != 0 {
		t.Fatal("later batch hooks must not run after a pre-hook error")
	}
}
