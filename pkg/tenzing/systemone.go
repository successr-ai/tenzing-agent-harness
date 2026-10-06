package tenzing

import (
	"context"
	"fmt"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	s1client "github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/systemone"
)

// SystemOneCall is one request to a System One model (TypeSafe's Jev). Every
// field is required: there is no default endpoint or model.
type SystemOneCall struct {
	APIKey string
	// URL is the full endpoint, used exactly as given — nothing is appended,
	// e.g. https://openrouter.ai/api/alpha/decisions.
	URL   string
	Model string
	// State is what the questions are asked about: a string, a JSON object,
	// or an array of text values.
	State common.State
	// Questions are keyed by caller-chosen ids, which key the answers too.
	Questions map[string]Question
	// HTTPTimeout bounds each HTTP attempt end to end; zero means none.
	// A timed-out attempt is retried, so size it above the slowest answer.
	HTTPTimeout time.Duration
}

// System One request and answer types, and the question constructors.
type (
	Question          = common.Question
	Answer            = common.Answer
	SystemOneResponse = common.EvaluationResponse
)

var (
	NewNoul             = common.NewNoul
	NewNoulWithCriteria = common.NewNoulWithCriteria
	NewChoice           = common.NewChoice
	NewScore            = common.NewScore
)

// CallSystemOne sends one request to a System One model and returns one typed
// answer per question. Transient failures (408, 429, 5xx) are retried. It
// builds a client per call; for repeated calls, or to fake the model in
// tests, hold a common.SystemOne from systemone.NewClient instead.
func CallSystemOne(ctx context.Context, c SystemOneCall) (SystemOneResponse, error) {
	client, err := s1client.NewClient(
		s1client.WithAPIKey(c.APIKey),
		s1client.WithURL(c.URL),
		s1client.WithModel(c.Model),
		s1client.WithHTTPTimeout(c.HTTPTimeout))
	if err != nil {
		return SystemOneResponse{}, fmt.Errorf("CallSystemOne: %w", err)
	}
	resp, err := client.Evaluate(ctx, common.EvaluationRequest{State: c.State, Questions: c.Questions})
	if err != nil {
		return SystemOneResponse{}, fmt.Errorf("CallSystemOne: %w", err)
	}
	return resp, nil
}
