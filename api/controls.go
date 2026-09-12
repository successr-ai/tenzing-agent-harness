package api

import (
	"context"
	"strings"
	"time"

	srverrors "github.com/tab58/huma-http-server/errors"
	"github.com/tab58/huma-http-server/router"

	"github.com/successr-ai/tenzing-agent-harness/internal/app"
)

// --- Runtime controls: compaction, thinking, model, stats ---

func (s *Server) handleClear(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*statusOutput, error) {
	if err := s.harness.Clear(); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrConflict, err.Error())
	}
	s.costs.Reset()
	out := &statusOutput{}
	out.Body.Status = "context cleared, now " + s.harness.ConversationID()
	return out, nil
}

func (s *Server) handleResume(_ context.Context, _ router.MapAuthInfo, in *resumeInput) (*statusOutput, error) {
	if err := s.harness.Resume(strings.TrimSpace(in.Body.ConversationID)); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrConflict, err.Error())
	}
	s.costs.Reset()
	out := &statusOutput{}
	out.Body.Status = "resumed " + s.harness.ConversationID()
	return out, nil
}

func (s *Server) handleCompact(ctx context.Context, _ router.MapAuthInfo, in *compactInput) (*statusOutput, error) {
	if err := s.harness.Compact(ctx, strings.TrimSpace(in.Body.Instructions)); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrConflict, err.Error())
	}
	out := &statusOutput{}
	out.Body.Status = "compacted"
	return out, nil
}

func (s *Server) handleThinking(_ context.Context, _ router.MapAuthInfo, in *thinkingInput) (*statusOutput, error) {
	if err := s.harness.SetThinking(in.Body.Enabled); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrConflict, err.Error())
	}
	out := &statusOutput{}
	out.Body.Status = "thinking off"
	if in.Body.Enabled {
		out.Body.Status = "thinking on"
	}
	return out, nil
}

func (s *Server) handleModelSet(_ context.Context, _ router.MapAuthInfo, in *modelInput) (*statusOutput, error) {
	if s.cfg.ResolveLLM == nil {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "no model registry configured")
	}
	llm, err := s.cfg.ResolveLLM(strings.TrimSpace(in.Body.Model))
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, err.Error())
	}
	if err := s.harness.SetLLM(llm); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrConflict, err.Error())
	}
	out := &statusOutput{}
	out.Body.Status = "model set to " + llm.GetModel().GetName()
	return out, nil
}

func (s *Server) handleModelsList(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*modelsOutput, error) {
	out := &modelsOutput{}
	out.Body.Current = s.harness.GetCurrentModel()
	if s.cfg.ModelNames != nil {
		out.Body.Models = s.cfg.ModelNames()
	}
	return out, nil
}

func (s *Server) handleStats(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*statsOutput, error) {
	return &statsOutput{Body: s.costs.Stats()}, nil
}

// --- Project trust (F26) ---

// handleTrustGet reports the trust decision for the server's cwd.
func (s *Server) handleTrustGet(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*trustOutput, error) {
	path, err := app.TrustFilePath()
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrInternalServerError, err.Error())
	}
	trusted, source := app.ResolveProjectTrust(path, s.cfg.Cwd, s.cfg.TrustEnvDefault)
	out := &trustOutput{}
	out.Body.Cwd = s.cfg.Cwd
	out.Body.Trusted = trusted
	out.Body.Source = source
	return out, nil
}

// handleTrustSet persists a trust decision for the server's cwd. Project
// config files are read at startup, so the decision applies on restart.
func (s *Server) handleTrustSet(_ context.Context, _ router.MapAuthInfo, in *trustInput) (*trustOutput, error) {
	path, err := app.TrustFilePath()
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrInternalServerError, err.Error())
	}
	if err := app.SetProjectTrust(path, s.cfg.Cwd, in.Body.Trusted, time.Now()); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrInternalServerError, err.Error())
	}
	out := &trustOutput{}
	out.Body.Cwd = s.cfg.Cwd
	out.Body.Trusted = in.Body.Trusted
	out.Body.Source = "persisted"
	out.Body.Note = "applies to project config loaded at startup; restart to take effect"
	return out, nil
}
