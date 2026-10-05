package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/app/modelregistry"
	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// recordingLLMs remembers the model each Get resolved to.
type recordingLLMs struct {
	llm  common.LLM
	seen []modelregistry.ResolvedModel
}

func (f *recordingLLMs) Get(rm modelregistry.ResolvedModel) (common.LLM, error) {
	f.seen = append(f.seen, rm)
	return f.llm, nil
}

// fakeJudge answers every question yes and remembers the resolved model.
type fakeJudge struct {
	seen []modelregistry.ResolvedSystemOne
	req  common.EvaluationRequest
}

func (f *fakeJudge) GetSystemOne(rs modelregistry.ResolvedSystemOne) (common.SystemOne, error) {
	f.seen = append(f.seen, rs)
	return f, nil
}

func (f *fakeJudge) Evaluate(_ context.Context, req common.EvaluationRequest) (common.EvaluationResponse, error) {
	f.req = req
	answers := map[string]common.Answer{}
	for id := range req.Questions {
		answers[id] = common.Answer{Type: common.QuestionNoul, Noul: 0.9}
	}
	return common.EvaluationResponse{Model: "jev-1.13.0", Answers: answers, Usage: common.Usage{InputTokens: 7}}, nil
}

func (f *fakeJudge) GetCurrentModel() string { return "jev" }

func singleTestDeps(t *testing.T) (*deps, *recordingLLMs, *fakeJudge) {
	t.Helper()
	reg, err := modelregistry.Build([]cfgfile.Provider{
		{Name: "compat", Type: "openai_compat", URL: "http://localhost:1/v1"},
		{Name: "ts", Type: cfgfile.SystemOneProviderType},
	}, cfgfile.ModelsSection{
		LLM:       []cfgfile.ModelEntry{{Name: "main", Provider: "compat", ModelName: "gpt-x"}},
		SystemOne: []cfgfile.SystemOneEntry{{Name: "jev", Provider: "ts", ModelName: "jev-latest"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	llms := &recordingLLMs{llm: &scriptedLLM{answer: "42"}}
	judge := &fakeJudge{}
	return &deps{models: reg, llms: llms, judges: judge}, llms, judge
}

func TestRunSingle(t *testing.T) {
	file := cfgfile.File{Model: "main", SystemOneModel: "jev"}
	evalInput := `{"state":"rm -rf /","questions":{"bad":{"type":"noul","instructions":"Is this destructive?"}}}`

	for _, tt := range []struct {
		name, input, wantOut, wantErr, wantWire string
		opts                                    singleOpts
	}{
		{name: "llm default model", opts: singleOpts{modelType: "llm"}, input: "hi", wantOut: "42\n", wantWire: "gpt-x"},
		{name: "llm provider+wire id", opts: singleOpts{modelType: "llm", provider: "compat", model: "other-model"}, input: "hi", wantOut: "42\n", wantWire: "other-model"},
		{name: "systemone default", opts: singleOpts{modelType: "systemone"}, input: evalInput, wantOut: `"noul": 0.9`, wantWire: "jev-latest"},
		{name: "systemone provider+wire id", opts: singleOpts{modelType: "systemone", provider: "ts", model: "jev-preview"}, input: evalInput, wantOut: `"input_tokens": 7`, wantWire: "jev-preview"},
		{name: "systemone on chat provider", opts: singleOpts{modelType: "systemone", provider: "compat", model: "x"}, input: evalInput, wantErr: "not systemone"},
		{name: "systemone bad json", opts: singleOpts{modelType: "systemone"}, input: "hi", wantErr: "systemone input must be"},
		{name: "systemone no questions", opts: singleOpts{modelType: "systemone"}, input: `{"state":"x"}`, wantErr: "at least one entry"},
		{name: "provider without model", opts: singleOpts{modelType: "llm", provider: "compat"}, input: "hi", wantErr: "--provider requires --model"},
		{name: "bad model type", opts: singleOpts{modelType: "image"}, input: "hi", wantErr: "--model-type must be"},
		{name: "empty input", opts: singleOpts{modelType: "llm"}, input: "  \n", wantErr: "empty input"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, llms, judge := singleTestDeps(t)
			var out bytes.Buffer
			err := runSingle(context.Background(), d, file, tt.opts, tt.input, &out)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("out = %q, want containing %q", out.String(), tt.wantOut)
			}
			var wire string
			if tt.opts.modelType == "llm" {
				wire = llms.seen[0].Def.Name
			} else {
				wire = judge.seen[0].Name
				if !json.Valid(out.Bytes()) {
					t.Errorf("output is not JSON: %s", out.String())
				}
			}
			if wire != tt.wantWire {
				t.Errorf("wire model = %q, want %q", wire, tt.wantWire)
			}
		})
	}
}

// The llm-only flags reach the request, and --stream splits answer (stdout)
// from reasoning (stderr).
func TestRunSingleLLMFlags(t *testing.T) {
	file := cfgfile.File{Model: "main", SystemOneModel: "jev"}
	off := false

	t.Run("sync carries system prompt and thinking", func(t *testing.T) {
		d, llms, _ := singleTestDeps(t)
		var out bytes.Buffer
		o := singleOpts{modelType: "llm", systemPrompt: "Be terse.", thinking: &off}
		if err := runSingle(context.Background(), d, file, o, "hi", &out); err != nil {
			t.Fatal(err)
		}
		req := llms.llm.(*scriptedLLM).requests()[0]
		if req.System != "Be terse." || req.Think == nil || *req.Think {
			t.Errorf("req System=%q Think=%v", req.System, req.Think)
		}
	})

	t.Run("stream", func(t *testing.T) {
		d, llms, _ := singleTestDeps(t)
		llms.llm = &scriptedLLM{answer: "42", thinking: "hmm"}
		var out, errOut bytes.Buffer
		o := singleOpts{modelType: "llm", stream: true, stderr: &errOut}
		if err := runSingle(context.Background(), d, file, o, "hi", &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != "42\n" || errOut.String() != "hmm\n" {
			t.Errorf("stdout=%q stderr=%q", out.String(), errOut.String())
		}
	})

	t.Run("systemone rejects llm flags", func(t *testing.T) {
		d, _, _ := singleTestDeps(t)
		o := singleOpts{modelType: "systemone", stream: true}
		err := runSingle(context.Background(), d, file, o, `{"state":"x","questions":{"q":{"type":"noul","instructions":"?"}}}`, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "llm only") {
			t.Fatalf("err = %v", err)
		}
	})
}
