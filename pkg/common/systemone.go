package common

import "context"

// SystemOne is the client interface for a System One model — the decision
// counterpart to LLM. Where an LLM continues a conversation, a System One
// model judges one state against a map of typed questions and returns one
// calibrated, typed answer per question: no generated text, no tool calls,
// no streaming. TypeSafe's Jev is the first such model.
//
// The caller keeps control flow: it thresholds the answers and combines them
// in its own code. Implementations live in pkg/providers/protocols.
type SystemOne interface {
	// Evaluate answers every question in req against req.State. Questions in
	// one request are independent and are evaluated in parallel over a single
	// pass of the state, so asking more of them together is far cheaper than
	// splitting them across requests.
	Evaluate(ctx context.Context, req EvaluationRequest) (EvaluationResponse, error)

	// GetCurrentModel returns the model this client names in requests.
	GetCurrentModel() string
}

// State is the content a System One model evaluates: a string, a JSON object,
// or an array of text values. Text only — encode anything else first. Keep
// facts in the state and judgments in the questions, and put everything one
// decision must compare into the same state.
type State any

// QuestionType is the primitive a question asks for.
type QuestionType string

const (
	// QuestionNoul is a yes/no question, answered with a probability.
	QuestionNoul QuestionType = "noul"
	// QuestionChoice picks one option from a fixed set.
	QuestionChoice QuestionType = "choice"
	// QuestionScore rates the state across ordered levels.
	QuestionScore QuestionType = "score"
)

// Question is one typed judgment about a request's state. It should be a
// decision a knowledgeable person makes in seconds; decompose a broad
// judgment into several atomic questions and combine them in code.
//
// Instructions and every criteria description accept a string, a JSON object,
// a JSON array, or null. A structured value's fields are referenced from the
// question text in backticks, the same way a nested state field is
// ("Is the resume for the same person as `potential_duplicate`?").
type Question struct {
	Type         QuestionType `json:"type"`
	Instructions any          `json:"instructions"`
	// Criteria is required for Choice (a map of option name to description)
	// and Score (an ordered array of level descriptions, low to high), and
	// optional for Noul (an object with "true" and "false" descriptions).
	Criteria any `json:"criteria,omitempty"`
}

// NewNoul builds a yes/no question. Ask one thing per Noul — no compound
// conditions — and phrase it positively.
func NewNoul(instructions any) Question {
	return Question{Type: QuestionNoul, Instructions: instructions}
}

// NewNoulWithCriteria builds a yes/no question that also describes what a yes
// and a no mean, for boundaries too subtle to leave to the question alone.
func NewNoulWithCriteria(instructions, yes, no any) Question {
	return Question{
		Type:         QuestionNoul,
		Instructions: instructions,
		Criteria:     map[string]any{"true": yes, "false": no},
	}
}

// NewChoice builds a question that picks one option from criteria, a map of
// option name to description; a nil description means the name speaks for
// itself. Include an "other" or "none of the above" option when the set may
// not cover the input.
func NewChoice(instructions any, criteria map[string]any) Question {
	return Question{Type: QuestionChoice, Instructions: instructions, Criteria: criteria}
}

// NewScore builds a question that rates the state against levels, ordered low
// to high. Each level describes a concrete situation on its own terms rather
// than by comparison to its neighbours, and one Score measures one dimension.
func NewScore(instructions any, levels ...any) Question {
	return Question{Type: QuestionScore, Instructions: instructions, Criteria: levels}
}

// EvaluationRequest is the provider-agnostic input to Evaluate. Question ids
// are the caller's own; they are not sent to the model and come back as the
// answer keys.
type EvaluationRequest struct {
	State     State
	Questions map[string]Question
	// Model overrides the client's default model for this request. Empty
	// means the client's own.
	Model string
}

// Answer is the typed result for one question. Which fields carry meaning
// follows Type: Noul uses Noul alone, Choice uses Choice and Probabilities,
// Score uses Score, Legend and Probabilities. Choice and Score also carry
// Confidence; Noul does not, because its single probability already describes
// the answer completely.
type Answer struct {
	Type QuestionType `json:"type"`

	// Noul is the probability that the answer is yes, from 0 to 1. Threshold
	// it in code: 0.5 when a false yes and a false no cost the same, higher
	// when false positives are expensive.
	Noul float64 `json:"noul"`

	// Choice is the highest-probability option.
	Choice string `json:"choice"`

	// Score is the probability-weighted position across the levels, so it can
	// land between them.
	Score float64 `json:"score"`

	// Legend maps each Score level index back to the criteria entry that
	// described it, so the values are structured whenever the levels were.
	Legend map[string]any `json:"legend"`

	// Probabilities maps every option (Choice) or level index (Score) to its
	// probability. The values sum to 1.
	Probabilities map[string]float64 `json:"probabilities"`

	// Confidence collapses the shape of Probabilities into one value from 0
	// to 1: concentrated on a single outcome is high, spread evenly is low.
	// The answer says what; confidence says whether to act on it. It is
	// derived from Probabilities, so a caller wanting its own measure can
	// ignore it and compute one.
	Confidence float64 `json:"confidence"`
}

// EvaluationResponse holds one answer per question, keyed by the ids the
// request used.
type EvaluationResponse struct {
	// Model is the versioned model id that answered, which is what to log
	// when the request named a moving alias.
	Model   string
	Answers map[string]Answer
	Usage   Usage
}
