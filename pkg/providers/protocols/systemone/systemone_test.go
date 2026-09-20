package systemone

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/ratelimit"
)

const answersJSON = `{
  "model": "jev-1.13.0",
  "answers": {
    "is_urgent": {"type": "noul", "noul": 0.95},
    "department": {
      "type": "choice",
      "choice": "billing",
      "probabilities": {"billing": 0.88, "technical": 0.12, "sales": 0.0},
      "confidence": 0.81
    },
    "frustration": {
      "type": "score",
      "score": 1.05,
      "legend": {"0": "Calm", "1": "Frustrated", "2": "Very angry"},
      "probabilities": {"0": 0.0, "1": 0.95, "2": 0.05},
      "confidence": 0.92
    }
  },
  "usage": {"input_tokens": 296, "output_tokens": 20}
}`

func TestEvaluateRequestAndResponse(t *testing.T) {
	var got request
	var auth, contentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != evaluatePath {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, evaluatePath)
		}
		auth = r.Header.Get("Authorization")
		contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, answersJSON)
	}))
	defer srv.Close()

	client, err := NewClient(WithAPIKey("k"), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := client.Evaluate(context.Background(), common.EvaluationRequest{
		State: map[string]any{"message": "Help! My payouts have been failing for 3 days."},
		Questions: map[string]common.Question{
			"is_urgent": common.NewNoulWithCriteria("Does this convey urgency?",
				"Explicitly time-sensitive", "No urgency expressed"),
			"department": common.NewChoice("Which team should handle this?", map[string]any{
				"billing":   "Payments, invoicing, refunds",
				"technical": "Bugs, outages, integrations",
				"sales":     nil,
			}),
			"frustration": common.NewScore("How frustrated is the customer?", "Calm", "Frustrated", "Very angry"),
		},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if auth != "Bearer k" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer k")
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	if got.Model != DefaultModel {
		t.Errorf("request model = %q, want %q", got.Model, DefaultModel)
	}
	if q := got.Questions["frustration"]; q.Type != common.QuestionScore {
		t.Errorf("frustration type = %q, want %q", q.Type, common.QuestionScore)
	}
	if levels, ok := got.Questions["frustration"].Criteria.([]any); !ok || len(levels) != 3 {
		t.Errorf("score criteria = %#v, want 3 ordered levels", got.Questions["frustration"].Criteria)
	}
	if crit, ok := got.Questions["is_urgent"].Criteria.(map[string]any); !ok || crit["true"] != "Explicitly time-sensitive" {
		t.Errorf("noul criteria = %#v, want true/false descriptions", got.Questions["is_urgent"].Criteria)
	}

	if resp.Model != "jev-1.13.0" {
		t.Errorf("response model = %q, want jev-1.13.0", resp.Model)
	}
	if n := resp.Answers["is_urgent"].Noul; n != 0.95 {
		t.Errorf("noul = %v, want 0.95", n)
	}
	if c := resp.Answers["department"]; c.Choice != "billing" || c.Confidence != 0.81 || c.Probabilities["billing"] != 0.88 {
		t.Errorf("choice answer = %+v", c)
	}
	if s := resp.Answers["frustration"]; s.Score != 1.05 || s.Legend["1"] != "Frustrated" || s.Probabilities["1"] != 0.95 {
		t.Errorf("score answer = %+v", s)
	}
	if resp.Usage.InputTokens != 296 {
		t.Errorf("input tokens = %d, want 296", resp.Usage.InputTokens)
	}
}

func TestEvaluateErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		attempts   int // expected HTTP attempts
		wantStatus int
	}{
		{name: "validation error is not retried", status: 422, body: `{"error":"bad question"}`, attempts: 1, wantStatus: 422},
		{name: "unauthorized is not retried", status: 401, body: `{"error":"bad key"}`, attempts: 1, wantStatus: 401},
		{name: "rate limit is retried", status: 429, body: `{"error":"slow down"}`, attempts: 2, wantStatus: 429},
		{name: "overloaded is retried", status: 529, body: `{"error":"overloaded"}`, attempts: 3, wantStatus: 529},
		{name: "server error is retried", status: 500, body: `{"error":"boom"}`, attempts: 2, wantStatus: 500},
		{name: "timeout is retried", status: 408, body: `{"error":"timeout"}`, attempts: 2, wantStatus: 408},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			client, err := NewClient(WithAPIKey("k"), WithBaseURL(srv.URL),
				WithRetryBackoff(ratelimit.RetryBackoff{
					MaxRetries:  tt.attempts,
					BaseBackoff: time.Millisecond,
					MaxBackoff:  time.Millisecond,
				}))
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			_, err = client.Evaluate(context.Background(), common.EvaluationRequest{
				State:     "hi",
				Questions: map[string]common.Question{"q": common.NewNoul("Is this a greeting?")},
			})
			if err == nil {
				t.Fatal("Evaluate: want error, got nil")
			}
			var apiErr *common.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error %v is not a *common.APIError", err)
			}
			if apiErr.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", apiErr.StatusCode, tt.wantStatus)
			}
			if calls != tt.attempts {
				t.Errorf("HTTP attempts = %d, want %d", calls, tt.attempts)
			}
		})
	}
}

func TestNewClientRequiresAPIKeyAndEvaluateRequiresQuestions(t *testing.T) {
	if _, err := NewClient(); err == nil {
		t.Error("NewClient without an API key: want error, got nil")
	}
	client, err := NewClient(WithAPIKey("k"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Evaluate(context.Background(), common.EvaluationRequest{State: "hi"}); err == nil {
		t.Error("Evaluate with no questions: want error, got nil")
	}
}

func TestRequestModelOverridesClientModel(t *testing.T) {
	tests := []struct {
		name        string
		clientModel string
		reqModel    string
		want        string
	}{
		{name: "default", want: DefaultModel},
		{name: "client option", clientModel: "jev-preview", want: "jev-preview"},
		{name: "request wins", clientModel: "jev-preview", reqModel: "jev-1.13.0", want: "jev-1.13.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got request
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(body, &got); err != nil {
					t.Errorf("decoding request body: %v", err)
				}
				io.WriteString(w, `{"model":"jev-1.13.0","answers":{},"usage":{}}`)
			}))
			defer srv.Close()

			opts := []ClientOption{WithAPIKey("k"), WithBaseURL(srv.URL)}
			if tt.clientModel != "" {
				opts = append(opts, WithModel(tt.clientModel))
			}
			client, err := NewClient(opts...)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if tt.clientModel != "" && client.GetCurrentModel() != tt.clientModel {
				t.Errorf("GetCurrentModel = %q, want %q", client.GetCurrentModel(), tt.clientModel)
			}

			if _, err := client.Evaluate(context.Background(), common.EvaluationRequest{
				State:     "hi",
				Model:     tt.reqModel,
				Questions: map[string]common.Question{"q": common.NewNoul("Is this a greeting?")},
			}); err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if got.Model != tt.want {
				t.Errorf("request model = %q, want %q", got.Model, tt.want)
			}
		})
	}
}

// Score levels may be JSON objects, so the legend echoing them back is not
// always keyed to strings.
func TestStructuredScoreLevelsRoundTrip(t *testing.T) {
	const structured = `{
      "model": "jev-1.13.0",
      "answers": {
        "pr_scope": {
          "type": "score",
          "score": 0.4,
          "legend": {
            "0": {"summary": "One change, clearly stated", "signals": ["A single fix"]},
            "1": {"summary": "Several independent changes", "signals": ["Two or more unrelated fixes"]}
          },
          "probabilities": {"0": 0.6, "1": 0.4},
          "confidence": 0.2
        }
      },
      "usage": {}
    }`

	var got request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		io.WriteString(w, structured)
	}))
	defer srv.Close()

	client, err := NewClient(WithAPIKey("k"), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := client.Evaluate(context.Background(), common.EvaluationRequest{
		State: "Fixed the null check. Also refactored the retry loop.",
		Questions: map[string]common.Question{
			"pr_scope": common.NewScore(
				map[string]any{
					"question": "How focused is this pull request description on a single change?",
					"note":     "Judge the number of independent changes.",
				},
				map[string]any{"summary": "One change, clearly stated", "signals": []any{"A single fix"}},
				map[string]any{"summary": "Several independent changes", "signals": []any{"Two or more unrelated fixes"}},
			),
		},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	levels, ok := got.Questions["pr_scope"].Criteria.([]any)
	if !ok || len(levels) != 2 {
		t.Fatalf("score criteria = %#v, want 2 structured levels", got.Questions["pr_scope"].Criteria)
	}
	if first, ok := levels[0].(map[string]any); !ok || first["summary"] != "One change, clearly stated" {
		t.Errorf("first level = %#v, want the structured level object", levels[0])
	}
	if instructions, ok := got.Questions["pr_scope"].Instructions.(map[string]any); !ok || instructions["note"] == nil {
		t.Errorf("instructions = %#v, want the structured object", got.Questions["pr_scope"].Instructions)
	}

	legend, ok := resp.Answers["pr_scope"].Legend["1"].(map[string]any)
	if !ok {
		t.Fatalf("legend[1] = %#v, want the structured level echoed back", resp.Answers["pr_scope"].Legend["1"])
	}
	if legend["summary"] != "Several independent changes" {
		t.Errorf("legend[1].summary = %v", legend["summary"])
	}
}
