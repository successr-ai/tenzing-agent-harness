package api

import (
	"context"
	"encoding/json"
	"strings"

	srverrors "github.com/tab58/huma-http-server/errors"
	"github.com/tab58/huma-http-server/router"

	"github.com/successr-ai/tenzing-agent-harness/api/approvals"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/builtins"
)

// handleApprove answers one pending AskUser request. A non-empty `allow` is
// the "allow always" answer: the glob is persisted to the settings file and
// applied to the running session before the call is approved, so matching
// commands stop prompting for the rest of the session.
func (s *Server) handleApprove(_ context.Context, _ router.MapAuthInfo, in *approveInput) (*statusOutput, error) {
	pattern := strings.TrimSpace(in.Body.Allow)
	if in.Body.Allow != "" && pattern == "" {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "allow must not be blank")
	}

	pending, err := s.takePending(in.Body.CallID, pattern)
	if err != nil {
		return nil, err
	}
	if pattern != "" {
		if err := s.persistAllow(pending, pattern); err != nil {
			return nil, err
		}
		s.approvals.Remove(in.Body.CallID)
	}

	approved := in.Body.Approved || pattern != ""
	pending.Respond(approved)

	out := &statusOutput{}
	switch {
	case pattern != "":
		out.Body.Status = "allowed"
	case approved:
		out.Body.Status = "approved"
	default:
		out.Body.Status = "denied"
	}
	return out, nil
}

// takePending looks up the pending call. A plain answer removes it now; an
// `allow` answer leaves it pending until the glob is persisted, so a failed
// write can be retried.
func (s *Server) takePending(callID, pattern string) (approvals.Pending, error) {
	var (
		pending approvals.Pending
		ok      bool
	)
	if pattern == "" {
		pending, ok = s.approvals.Take(callID)
	} else {
		pending, ok = s.approvals.Get(callID)
	}
	if !ok {
		return approvals.Pending{}, srverrors.Wrap(srverrors.ErrBadRequest, "no pending approval for call_id")
	}
	return pending, nil
}

// persistAllow validates and writes an "allow always" glob for a bash call.
func (s *Server) persistAllow(pending approvals.Pending, pattern string) error {
	if s.cfg.BashAllow == nil {
		return srverrors.Wrap(srverrors.ErrBadRequest, "no settings file configured for allow")
	}
	if !strings.EqualFold(pending.Tool, "bash") {
		return srverrors.Wrap(srverrors.ErrBadRequest, "allow applies to bash calls only")
	}
	return s.cfg.BashAllow.Add(pattern)
}

// handlePreview reports the diff a pending Edit/Write approval would produce.
// Read-only: the request stays pending either way. A call that cannot be
// previewed (wrong tool, unreadable file, old_string not found) reports the
// reason in `error` rather than failing the request — the user still has to
// approve or deny.
func (s *Server) handlePreview(_ context.Context, _ router.MapAuthInfo, in *previewInput) (*previewOutput, error) {
	pending, ok := s.approvals.Get(in.Body.CallID)
	if !ok {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "no pending approval for call_id")
	}

	out := &previewOutput{}
	d, previewable, err := builtins.PreviewDiff(s.cfg.Cwd, pending.Tool, pending.Input)
	switch {
	case !previewable:
		out.Body.Error = "no preview for " + pending.Tool
	case err != nil:
		out.Body.Error = err.Error()
	default:
		out.Body.Diff, out.Body.Added, out.Body.Removed, out.Body.Omitted = d.Text, d.Added, d.Removed, d.Omitted
	}
	return out, nil
}

// handleSuggest proposes the "allow always" glob for a pending bash
// approval: a rule for the first expression the live allow list does not
// already cover, so a chained command converges one rule per approval.
// Read-only and advisory — the request stays pending, the glob is editable,
// and a call with nothing to propose reports why in `reason`.
func (s *Server) handleSuggest(_ context.Context, _ router.MapAuthInfo, in *suggestInput) (*suggestOutput, error) {
	pending, ok := s.approvals.Get(in.Body.CallID)
	if !ok {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "no pending approval for call_id")
	}

	out := &suggestOutput{}
	switch {
	case !strings.EqualFold(pending.Tool, "bash"):
		out.Body.Reason = "no glob for " + pending.Tool
	case s.cfg.BashAllow == nil:
		out.Body.Reason = "no settings file configured for allow"
	default:
		var args struct {
			Command string `json:"command"`
		}
		// Unparseable input yields an empty command, which suggests nothing.
		_ = json.Unmarshal([]byte(pending.Input), &args)
		out.Body.Glob, out.Body.Reason = s.cfg.BashAllow.Rules().Suggest(args.Command)
	}
	return out, nil
}
