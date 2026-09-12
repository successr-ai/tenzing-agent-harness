package api

import (
	"context"
	"strings"
	"time"

	srverrors "github.com/tab58/huma-http-server/errors"
	"github.com/tab58/huma-http-server/router"

	"github.com/successr-ai/tenzing-agent-harness/internal/harness/session"
)

// --- Session management (F4) ---

// sessionDir returns the harness's session directory, or an error when
// persistence is disabled.
func (s *Server) sessionDir() (dir, cwd string, err error) {
	dir, cwd = s.harness.SessionInfo()
	if dir == "" {
		return "", "", srverrors.Wrap(srverrors.ErrBadRequest, "session persistence is disabled")
	}
	return dir, cwd, nil
}

func (s *Server) handleSessionsList(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*sessionsOutput, error) {
	dir, cwd, err := s.sessionDir()
	if err != nil {
		return nil, err
	}
	infos, err := session.List(dir, cwd)
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrInternalServerError, err.Error())
	}
	active := s.harness.ConversationID()
	out := &sessionsOutput{}
	out.Body.Sessions = make([]sessionInfo, len(infos))
	for i, in := range infos {
		out.Body.Sessions[i] = sessionInfo{
			ConversationID: in.ConversationID,
			Name:           in.Name,
			Model:          in.Model,
			Created:        in.Created.Format(time.RFC3339),
			Modified:       in.Modified.Format(time.RFC3339),
			Entries:        in.Entries,
			Active:         in.ConversationID == active,
		}
	}
	return out, nil
}

func (s *Server) handleSessionDelete(_ context.Context, _ router.MapAuthInfo, in *sessionIDInput) (*statusOutput, error) {
	dir, cwd, err := s.sessionDir()
	if err != nil {
		return nil, err
	}
	// The live store holds an open handle on the active session; deleting
	// it would orphan every subsequent write.
	if in.ID == s.harness.ConversationID() {
		return nil, srverrors.Wrap(srverrors.ErrConflict, "cannot delete the active conversation")
	}
	if err := session.Delete(dir, cwd, in.ID); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrNotFound, err.Error())
	}
	out := &statusOutput{}
	out.Body.Status = "deleted"
	return out, nil
}

func (s *Server) handleSessionRename(_ context.Context, _ router.MapAuthInfo, in *sessionRenameInput) (*statusOutput, error) {
	dir, cwd, err := s.sessionDir()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Body.Name)
	if name == "" {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "empty name")
	}
	if err := session.Rename(dir, cwd, in.ID, name); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrNotFound, err.Error())
	}
	out := &statusOutput{}
	out.Body.Status = "renamed"
	return out, nil
}

func (s *Server) handleMessages(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*messagesOutput, error) {
	dir, cwd, err := s.sessionDir()
	if err != nil {
		return nil, err
	}
	id := s.harness.ConversationID()
	res, err := session.Load(dir, cwd, id)
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrInternalServerError, err.Error())
	}
	out := &messagesOutput{}
	out.Body.ConversationID = id
	out.Body.Messages = []messageSummary{}
	if res == nil {
		return out, nil
	}
	for _, m := range res.History {
		var text strings.Builder
		for _, b := range m.Content {
			text.WriteString(b.Text)
		}
		out.Body.Messages = append(out.Body.Messages, messageSummary{
			Role: string(m.Role),
			Text: text.String(),
		})
	}
	return out, nil
}
