package systemone

import (
	"fmt"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// Every question the harness asks a System One model, and the default
// threshold each answer is read against. They live together because a
// threshold is tuned to one exact wording: change the words and the number is
// no longer calibrated. Config may override the numbers; the wording needs a
// code change and a review.
const (
	// DefaultGateAsk is the probability above which a tool call is escalated
	// to AskUser. Below the deny threshold, so a call can be questioned
	// without being refused.
	DefaultGateAsk = 0.6
	// DefaultGateDeny is the probability above which an out-of-scope call is
	// refused outright. High: a false deny costs the turn.
	DefaultGateDeny = 0.9
	// DefaultAdvisorConsult is the probability above which the executor is
	// told to consult its advisor before acting again.
	DefaultAdvisorConsult = 0.7
	// DefaultRoutingConfidence is the confidence below which a routing choice
	// is ignored and the configured model keeps serving.
	DefaultRoutingConfidence = 0.5
)

// Question ids. Batch A's are fixed; batch B's are per call, built by
// scopeID/irreversibleID from the call id so answers map back to their call.
const (
	qAdvisor = "needs_advisor"
	qRouting = "model"
)

func scopeID(callID string) string        { return callID + "#out_of_scope" }
func irreversibleID(callID string) string { return callID + "#irreversible" }

// advisorQuestion asks whether the executor should stop and consult. The
// criteria are the advisor's own rules, restated as observable situations:
// asking "is it stuck" alone leaves Jev to guess what stuck looks like here.
func advisorQuestion() common.Question {
	return common.NewNoulWithCriteria(
		"Should this agent consult its advisor before its next action?",
		map[string]any{
			"summary": "Yes — the agent is at a decision point an advisor should see first.",
			"signals": []any{
				"It is about to commit to an approach: writing, editing, or starting an implementation.",
				"It is stuck: the same error or the same failing check keeps recurring.",
				"It is reversing a decision it already made this turn.",
				"It is about to declare a non-trivial task done.",
			},
		},
		map[string]any{
			"summary": "No — the agent is orienting or executing a settled plan.",
			"signals": []any{
				"It is reading, searching, or listing to find out what is there.",
				"It is carrying out a plan it has already consulted about, without surprises.",
				"It has just consulted, and nothing has contradicted that advice since.",
			},
		},
	)
}

// routingQuestion asks which declared model should serve this request. Each
// option's description is the model's own `description:` from the config, so
// the criteria are written by whoever declared the model.
func routingQuestion(candidates []Candidate) common.Question {
	criteria := make(map[string]any, len(candidates))
	for _, c := range candidates {
		criteria[c.Name] = c.Description
	}
	return common.NewChoice(
		"Which of these models should handle the user's latest request in `recent_messages`? "+
			"Judge the request as written, not the work that might follow from it.",
		criteria)
}

// scopeQuestion and irreversibleQuestion are two atomic nouls per pending
// call rather than one compound "is this dangerous", which conflates a call
// the user did not ask for with a call that cannot be undone. They carry
// different costs and the harness combines them in code; inside one batch the
// second question is near-free.
func scopeQuestion() common.Question {
	return common.NewNoulWithCriteria(
		"Is the tool call in `tool_call` outside what the user asked for in `recent_messages`?",
		"Yes — it touches something the user did not ask about, or does something they did not request.",
		"No — it is a step a competent engineer would take to carry out that request, including "+
			"reading, searching, and inspecting to find out what is there.",
	)
}

func irreversibleQuestion() common.Question {
	return common.NewNoulWithCriteria(
		"Would executing the tool call in `tool_call` destroy work that cannot be recovered?",
		map[string]any{
			"summary": "Yes — the effect cannot be undone from this machine.",
			"signals": []any{
				"Deleting files or directories that are not tracked by version control.",
				"Discarding uncommitted work, or rewriting published history.",
				"Dropping, truncating, or overwriting stored data.",
				"Sending something outward that cannot be recalled: a push, a deploy, a message, a payment.",
			},
		},
		map[string]any{
			"summary": "No — the effect is recoverable or confined.",
			"signals": []any{
				"Reading, searching, or listing anything.",
				"Editing or creating files under version control.",
				"Writing to a scratch or temporary location.",
			},
		},
	)
}

// Reasons shown to the human in the approval prompt and fed back to the model
// as the tool error on a deny. They name the question and the number, so a
// surprising block can be traced to the threshold that produced it.
func scopeReason(p float64, deny bool) string {
	verb := "may be"
	if deny {
		verb = "is"
	}
	return fmt.Sprintf("the decision model judges this call %s outside what you asked for (p=%.2f)", verb, p)
}

func irreversibleReason(p float64) string {
	return fmt.Sprintf("the decision model judges this call irreversible (p=%.2f)", p)
}

const advisorReminder = "Your advisor should see this before you act again. Call `advisor` " +
	"with what you believe, what contradicts it, and the decision you are about to make."

const advisorBlockReason = "Call `advisor` before this. A decision model judged that you are at " +
	"a point your advisor should see first — committing to an approach, stuck, reversing yourself, " +
	"or calling a task done. Read-only orientation is still allowed."
