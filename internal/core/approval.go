package core

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ApprovalRequestedEvent asks a driver (UI, API endpoint) to approve or deny
// one tool call escalated to AskUser. Respond is idempotent and safe to call
// from any goroutine; only the first call counts. The loop blocks until
// Respond fires, the context is canceled, or ApprovalTimeout elapses — both
// of the latter count as denial.
type ApprovalRequestedEvent struct {
	BaseEvent
	CallID   string `json:"call_id"`
	ToolName string `json:"tool_name"`
	Input    string `json:"input"`
	Reason   string `json:"reason"`
	// Timeout is how long the loop waits for Respond before denying — the
	// driver's countdown. Always > 0 (the event is not emitted otherwise).
	Timeout time.Duration       `json:"timeout"`
	Respond func(approved bool) `json:"-"`
}

// requestApproval emits the ApprovalRequestedEvent for one AskUser call and
// returns the channel its Respond feeds. Nil when approvals are disabled
// (ApprovalTimeout <= 0) — nothing can answer, so nothing is emitted. The
// request fires immediately (during the sequential decision phase); the
// answer is awaited later, in the call's execution goroutine.
func (l *Loop) requestApproval(call ToolCall, reason string) <-chan bool {
	if l.approvalTimeout <= 0 {
		return nil
	}

	ch := make(chan bool, 1)
	var once sync.Once
	respond := func(approved bool) {
		once.Do(func() { ch <- approved })
	}

	l.emit(ApprovalRequestedEvent{
		BaseEvent: NewBaseEvent(EventApprovalRequested, l.id),
		CallID:    call.ID,
		ToolName:  call.Name,
		Input:     call.Input,
		Reason:    reason,
		Timeout:   l.approvalTimeout,
		Respond:   respond,
	})
	return ch
}

// waitApproval blocks on one requestApproval channel. It returns whether the
// call may execute and, when not, the reason fed back to the model as an
// error tool result.
func (l *Loop) waitApproval(ctx context.Context, ch <-chan bool, reason string) (bool, string) {
	if ch == nil {
		// Unattended drivers: nothing can answer, deny immediately.
		return false, "tool call requires approval but no approver is configured"
	}

	timer := time.NewTimer(l.approvalTimeout)
	defer timer.Stop()
	select {
	case approved := <-ch:
		if approved {
			return true, ""
		}
		if reason == "" {
			return false, "tool call denied by user"
		}
		return false, fmt.Sprintf("tool call denied by user (policy: %s)", reason)
	case <-ctx.Done():
		return false, fmt.Sprintf("approval canceled: %v", ctx.Err())
	case <-timer.C:
		return false, fmt.Sprintf("approval timed out after %s", l.approvalTimeout)
	}
}
