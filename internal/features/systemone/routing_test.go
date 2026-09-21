package systemone

import (
	"context"
	"errors"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func twoCandidates() []Candidate {
	return []Candidate{
		{Name: "main-model", Description: "Frontier. Refactors, debugging, design decisions."},
		{Name: "fast-model", Description: "Cheap and quick. Lookups, one-line edits."},
	}
}

// newRoutingExt wires routing alone, recording what the router was asked to
// switch to.
func newRoutingExt(j common.SystemOne, cfg RoutingConfig, routed *[]string, routeErr error) *Ext {
	ext := New(Config{
		Client:  j,
		Gate:    GateConfig{Disabled: true},
		Advisor: AdvisorConfig{Disabled: true},
		Routing: cfg,
	})
	ext.SetRouter(func(_ context.Context, alias string) error {
		if routeErr != nil {
			return routeErr
		}
		*routed = append(*routed, alias)
		return nil
	})
	return ext
}

func TestRouting(t *testing.T) {
	tests := []struct {
		name    string
		answer  common.Answer
		current string
		minConf float64
		wantTo  []string
	}{
		{"a confident choice switches the model", choice("fast-model", 0.9), "main-model", 0, []string{"fast-model"}},
		{"a low-confidence choice is ignored", choice("fast-model", 0.2), "main-model", 0.5, nil},
		{"choosing the model already serving is a no-op", choice("main-model", 0.9), "main-model", 0, nil},
		{"an alias that is not a candidate is ignored", choice("ghost-model", 0.99), "main-model", 0, nil},
		{"an empty choice is ignored", choice("", 0.99), "main-model", 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var routed []string
			j := &fakeJudge{answers: map[string]common.Answer{qRouting: tt.answer}}
			ext := newRoutingExt(j, RoutingConfig{
				Candidates: twoCandidates(), Current: tt.current, MinConfidence: tt.minConf,
			}, &routed, nil)

			if err := ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1}); err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if len(routed) != len(tt.wantTo) {
				t.Fatalf("routed = %v, want %v", routed, tt.wantTo)
			}
			for i := range tt.wantTo {
				if routed[i] != tt.wantTo[i] {
					t.Fatalf("routed = %v, want %v", routed, tt.wantTo)
				}
			}
		})
	}
}

func TestRoutingAsksOnlyOnTheFirstIteration(t *testing.T) {
	var routed []string
	j := &fakeJudge{answers: map[string]common.Answer{qRouting: choice("fast-model", 0.9)}}
	ext := newRoutingExt(j, RoutingConfig{Candidates: twoCandidates(), Current: "main-model"}, &routed, nil)

	for _, iter := range []int{1, 2, 3} {
		if err := ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: iter}); err != nil {
			t.Fatalf("unexpected: %v", err)
		}
	}
	if len(j.reqs) != 1 {
		t.Fatalf("routing is a turn-start judgment; got %d requests", len(j.reqs))
	}
	if len(routed) != 1 || routed[0] != "fast-model" {
		t.Fatalf("routed = %v, want one switch to fast-model", routed)
	}
}

func TestRoutingNeedsTwoCandidates(t *testing.T) {
	var routed []string
	j := &fakeJudge{answers: map[string]common.Answer{}}
	ext := newRoutingExt(j, RoutingConfig{Candidates: twoCandidates()[:1]}, &routed, nil)
	if err := ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(j.reqs) != 0 {
		t.Fatal("one candidate is not a choice; nothing should be asked")
	}
}

func TestRoutingSurvivesASwitchFailure(t *testing.T) {
	var routed []string
	j := &fakeJudge{answers: map[string]common.Answer{qRouting: choice("fast-model", 0.9)}}
	ext := newRoutingExt(j, RoutingConfig{Candidates: twoCandidates(), Current: "main-model"},
		&routed, errors.New("model unavailable"))
	if err := ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1}); err != nil {
		t.Fatalf("a failed switch must not fail the iteration: %v", err)
	}
}

func TestRoutingCriteriaComeFromTheModelDescriptions(t *testing.T) {
	q := routingQuestion(twoCandidates())
	criteria, ok := q.Criteria.(map[string]any)
	if !ok || len(criteria) != 2 {
		t.Fatalf("criteria = %#v, want one entry per candidate", q.Criteria)
	}
	if criteria["fast-model"] != twoCandidates()[1].Description {
		t.Fatalf("criteria must be the declared description verbatim, got %v", criteria["fast-model"])
	}
}
