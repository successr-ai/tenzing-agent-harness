package api

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/api/turnqueue"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness/session"
)

// seedSession writes a fixture session for the server's cwd into dir.
func seedSession(t *testing.T, dir, id string, entries ...session.Entry) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	s := session.NewStore(dir, cwd, id, "fixture-model", time.Now)
	for _, e := range entries {
		s.Append(e)
	}
	s.Close()
}

func TestSessionEndpoints(t *testing.T) {
	dir := t.TempDir()
	seedSession(t, dir, "old-conv",
		session.Entry{Type: session.TypeUser, Text: "old question"},
		session.Entry{Type: session.TypeAssistant, Text: "old answer", Model: "fixture-model"},
	)

	api := newTestServer(t, &answerAgent{}, harness.WithSessionDir(dir))

	t.Run("list includes fixture and flags active", func(t *testing.T) {
		out, err := api.handleSessionsList(context.Background(), nil, nil)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		byID := map[string]sessionInfo{}
		for _, s := range out.Body.Sessions {
			byID[s.ConversationID] = s
		}
		old, ok := byID["old-conv"]
		if !ok {
			t.Fatalf("fixture session missing from list: %+v", out.Body.Sessions)
		}
		if old.Entries != 2 || old.Model != "fixture-model" || old.Active {
			t.Errorf("fixture metadata wrong: %+v", old)
		}
	})

	t.Run("rename persists into listing", func(t *testing.T) {
		in := &sessionRenameInput{ID: "old-conv"}
		in.Body.Name = "the good one"
		if _, err := api.handleSessionRename(context.Background(), nil, in); err != nil {
			t.Fatalf("rename: %v", err)
		}
		out, _ := api.handleSessionsList(context.Background(), nil, nil)
		var found bool
		for _, s := range out.Body.Sessions {
			if s.ConversationID == "old-conv" && s.Name == "the good one" {
				found = true
			}
		}
		if !found {
			t.Error("renamed session not listed with its name")
		}
	})

	t.Run("delete active conversation rejected", func(t *testing.T) {
		in := &sessionIDInput{ID: api.harness.ConversationID()}
		if _, err := api.handleSessionDelete(context.Background(), nil, in); err == nil {
			t.Fatal("deleting the active conversation should 409")
		}
	})

	t.Run("delete fixture removes it", func(t *testing.T) {
		in := &sessionIDInput{ID: "old-conv"}
		if _, err := api.handleSessionDelete(context.Background(), nil, in); err != nil {
			t.Fatalf("delete: %v", err)
		}
		out, _ := api.handleSessionsList(context.Background(), nil, nil)
		for _, s := range out.Body.Sessions {
			if s.ConversationID == "old-conv" {
				t.Error("deleted session still listed")
			}
		}
	})

	t.Run("delete unknown id is not found", func(t *testing.T) {
		in := &sessionIDInput{ID: "never-existed"}
		if _, err := api.handleSessionDelete(context.Background(), nil, in); err == nil {
			t.Fatal("deleting unknown session should fail")
		}
	})
}

func TestMessagesEndpoint(t *testing.T) {
	api := newTestServer(t, &answerAgent{})

	if got := submit(api, "what is the magic word?"); got != turnqueue.Started {
		t.Fatalf("query status = %q", got)
	}
	// wait for the turn to finish and the persister to flush the entries
	waitFor(t, "turn to complete and persist", func() bool {
		out, err := api.handleMessages(context.Background(), nil, nil)
		return err == nil && len(out.Body.Messages) >= 2
	})

	out, err := api.handleMessages(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if out.Body.ConversationID != api.harness.ConversationID() {
		t.Errorf("conversation_id = %q", out.Body.ConversationID)
	}
	msgs := out.Body.Messages
	if msgs[0].Role != "user" || msgs[0].Text != "what is the magic word?" {
		t.Errorf("first message = %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Text != "the-answer" {
		t.Errorf("second message = %+v", msgs[1])
	}
}

func TestSessionEndpointsDisabled(t *testing.T) {
	api := newTestServer(t, &answerAgent{}, harness.WithSessionDisabled())
	if _, err := api.handleSessionsList(context.Background(), nil, nil); err == nil {
		t.Fatal("sessions list should fail when persistence disabled")
	}
	if _, err := api.handleMessages(context.Background(), nil, nil); err == nil {
		t.Fatal("messages should fail when persistence disabled")
	}
}
