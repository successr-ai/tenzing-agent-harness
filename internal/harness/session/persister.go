package session

import (
	"encoding/base64"
	"log/slog"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/todo"
)

// StartPersister subscribes the store to the event bus and appends an entry
// per relevant main-agent event. Subagent events (different runner IDs) are
// not part of the conversation and are skipped. todoSnapshot, when non-nil,
// is called at each turn end to persist the plan (nil result → no entry).
// The returned stop function unsubscribes and drains: queued entries are
// written before it returns.
func StartPersister(bus *eventbus.EventBus, store *Store, mainRunnerID string, todoSnapshot func() []todo.Task) (stop func()) {
	isMain := func(runnerID string) bool { return runnerID == mainRunnerID }

	return eventbus.StartHooks(bus, eventbus.Hooks{
		OnTurnStarted: func(e core.TurnStartedEvent) {
			if !isMain(e.RunnerID) {
				return
			}
			store.Append(Entry{Type: TypeUser, Time: e.Time, Text: e.Query})
		},
		// Fired by the harness before the turn's TurnStartedEvent; image
		// bytes go to content-addressed sidecar blobs, entries keep only
		// media type + hash.
		OnImagesAttached: func(e core.ImagesAttachedEvent) {
			if !isMain(e.RunnerID) {
				return
			}
			for _, img := range e.Images {
				raw, err := base64.StdEncoding.DecodeString(img.Data)
				if err != nil {
					slog.Warn("session: skip image with invalid base64", "error", err)
					continue
				}
				sha := store.SaveBlob(raw)
				if sha == "" {
					continue
				}
				store.Append(Entry{Type: TypeImage, Time: e.Time, MediaType: img.MediaType, Blob: sha})
			}
		},
		OnLLMResponse: func(e core.LLMResponseEvent) {
			if !isMain(e.RunnerID) || e.Text == "" {
				return
			}
			store.Append(Entry{Type: TypeAssistant, Time: e.Time, Text: e.Text, Model: e.Model})
		},
		OnToolSucceeded: func(e core.ToolSucceededEvent) {
			if !isMain(e.RunnerID) {
				return
			}
			store.Append(Entry{Type: TypeToolResult, Time: e.Time, Tool: e.ToolName, Input: e.Input, Output: e.Output})
		},
		OnToolFailed: func(e core.ToolFailedEvent) {
			if !isMain(e.RunnerID) {
				return
			}
			store.Append(Entry{Type: TypeToolResult, Time: e.Time, Tool: e.ToolName, Input: e.Input, Output: e.Error, IsError: true})
		},
		OnSteeringInjected: func(e core.SteeringInjectedEvent) {
			if !isMain(e.RunnerID) {
				return
			}
			store.Append(Entry{Type: TypeSteering, Time: e.Time, Text: e.Message})
		},
		OnContextCompressed: func(e core.ContextCompressedEvent) {
			if !isMain(e.RunnerID) {
				return
			}
			store.Append(Entry{Type: TypeCompaction, Time: e.Time, Summary: e.Summary})
		},
		OnModelChanged: func(e core.ModelChangedEvent) {
			if !isMain(e.RunnerID) {
				return
			}
			store.Append(Entry{Type: TypeModelChange, Time: e.Time, Model: e.To, Text: e.From})
		},
		OnThinkingChanged: func(e core.ThinkingChangedEvent) {
			if !isMain(e.RunnerID) {
				return
			}
			state := "off"
			if e.Enabled {
				state = "on"
			}
			store.Append(Entry{Type: TypeThinking, Time: e.Time, Text: state})
		},
		OnTurnCompleted: func(e core.TurnCompletedEvent) {
			if !isMain(e.RunnerID) || todoSnapshot == nil {
				return
			}
			if tasks := todoSnapshot(); len(tasks) > 0 {
				store.Append(Entry{Type: TypeTodo, Time: time.Now(), Tasks: tasks})
			}
		},
	})
}
