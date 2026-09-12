package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	srverrors "github.com/tab58/huma-http-server/errors"
	"github.com/tab58/huma-http-server/router"

	"github.com/successr-ai/tenzing-agent-harness/api/turnqueue"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func (s *Server) handleQuery(_ context.Context, _ router.MapAuthInfo, in *queryInput) (*statusOutput, error) {
	query := strings.TrimSpace(in.Body.Query)
	if query == "" {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "empty query")
	}
	images, err := validateImages(in.Body.Images)
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, err.Error())
	}
	// Capability pre-flight: reject before the turn starts (the turn itself
	// runs async, so its errors can only surface on the SSE stream).
	if len(images) > 0 && !s.harness.SupportsVision() {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest,
			fmt.Sprintf("model %q does not support image input", s.harness.GetCurrentModel()))
	}
	status := s.turns.Submit(turnqueue.Request{Query: query, Images: images})
	if status == turnqueue.Rejected {
		return nil, srverrors.Wrap(srverrors.ErrConflict, "server shutting down")
	}
	out := &statusOutput{}
	out.Body.Status = string(status)
	return out, nil
}

// validateImages checks media types and base64 payloads at the trust
// boundary, returning provider-ready image sources.
func validateImages(in []imageInput) ([]common.ImageSource, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]common.ImageSource, len(in))
	for i, img := range in {
		if !strings.HasPrefix(img.MediaType, "image/") {
			return nil, fmt.Errorf("images[%d]: media_type %q is not an image MIME type", i, img.MediaType)
		}
		if img.Data == "" {
			return nil, fmt.Errorf("images[%d]: empty data", i)
		}
		if _, err := base64.StdEncoding.DecodeString(img.Data); err != nil {
			return nil, fmt.Errorf("images[%d]: data is not valid base64: %v", i, err)
		}
		out[i] = common.ImageSource{MediaType: img.MediaType, Data: img.Data}
	}
	return out, nil
}

func (s *Server) handleSteer(_ context.Context, _ router.MapAuthInfo, in *steerInput) (*statusOutput, error) {
	msg := strings.TrimSpace(in.Body.Message)
	if msg == "" {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "empty message")
	}
	if running, _ := s.turns.State(); !running {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "nothing running — use /query")
	}
	if err := s.harness.Steer(msg); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrConflict, err.Error())
	}
	out := &statusOutput{}
	out.Body.Status = "steering"
	return out, nil
}

func (s *Server) handleState(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*stateOutput, error) {
	running, queued := s.turns.State()

	out := &stateOutput{}
	out.Body.State = "idle"
	if running {
		out.Body.State = "running"
	}
	out.Body.LoopState = s.harness.LoopState()
	out.Body.Queued = queued
	out.Body.ConversationID = s.harness.ConversationID()
	out.Body.Model = s.harness.GetCurrentModel()
	out.Body.Vision = s.harness.SupportsVision()
	out.Body.ContextWindow = contextWindow(s.harness.CurrentModel())
	out.Body.Cwd = s.cfg.Cwd
	out.Body.Home, _ = os.UserHomeDir() // empty on failure: the UI just skips ~-shortening
	out.Body.Tools = len(s.harness.ToolDefinitions())
	return out, nil
}

// handleCancel cancels the in-flight turn AND drops any queued follow-ups:
// a user cancelling wants the agent to stop, not to watch the queue start
// the next turn.
func (s *Server) handleCancel(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*statusOutput, error) {
	dropped, ok := s.turns.Cancel()
	if !ok {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "nothing running")
	}
	out := &statusOutput{}
	out.Body.Status = "cancelled"
	if dropped > 0 {
		out.Body.Status = fmt.Sprintf("cancelled (%d queued queries dropped)", dropped)
	}
	return out, nil
}

func (s *Server) handleInfo(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*infoOutput, error) {
	out := &infoOutput{}
	out.Body.Tools = len(s.harness.ToolDefinitions())
	return out, nil
}

// StartNexusTurn is the trigger wake callback: builds an investigation
// prompt from recent channel errors and starts a turn. Returns false when
// the agent is busy (trigger keeps the channels pending) or no nexus is
// configured.
func (s *Server) StartNexusTurn(channels []string) bool {
	if s.cfg.Nexus == nil {
		return false
	}
	if !s.turns.Start(s.nexusPrompt(channels)) {
		return false
	}
	s.cfg.Bus.Emit(nexus.TriggerEvent{
		BaseEvent: core.NewBaseEvent(nexus.EventTrigger, "nexus"),
		Channels:  channels,
	})
	return true
}

// nexusPrompt renders the investigation prompt for erroring channels.
func (s *Server) nexusPrompt(channels []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Error detected in channel(s) %s.\n", strings.Join(channels, ", "))
	for _, name := range channels {
		entries, err := s.cfg.Nexus.Read(name, 5, true)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "\nRecent errors from %q:\n", name)
		for _, e := range entries {
			fmt.Fprintf(&b, "  [%d] %s\n", e.Seq, e.Text)
		}
	}
	b.WriteString("\nUse the read_channel and search_channel tools for more context. Investigate the root cause and report what you find.")
	return b.String()
}

// contextWindow reports the window the model actually runs at: the
// default window when the model declares one (Ollama's num_ctx can sit
// well below the architecture's maximum), otherwise the full size. Zero
// means unknown — the UI hides its context gauge rather than dividing by
// a guess.
func contextWindow(m common.Model) int {
	if m == nil {
		return 0
	}
	if n := m.GetDefaultContextWindow(); n > 0 {
		return n
	}
	return m.GetContextWindowSize()
}
