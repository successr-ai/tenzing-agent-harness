package api

import "github.com/successr-ai/tenzing-agent-harness/api/costs"

// Request and response bodies for the typed (huma) JSON routes. Field docs
// feed the OpenAPI spec.

type queryInput struct {
	Body struct {
		Query  string       `json:"query" doc:"Prompt to run the agent with"`
		Images []imageInput `json:"images,omitempty" doc:"Images attached to the query (vision-capable models only)"`
	}
}

type imageInput struct {
	MediaType string `json:"media_type" doc:"Image MIME type, e.g. image/png"`
	Data      string `json:"data" doc:"Base64-encoded image bytes (no data: URI prefix)"`
}

type statusOutput struct {
	Body struct {
		Status string `json:"status" doc:"Result of the request"`
	}
}

type infoOutput struct {
	Body struct {
		Tools int `json:"tools" doc:"Number of registered tools"`
	}
}

type steerInput struct {
	Body struct {
		Message string `json:"message" doc:"User message to inject into the running turn at the next tool boundary"`
	}
}

type stateOutput struct {
	Body struct {
		State          string `json:"state" doc:"running or idle"`
		LoopState      string `json:"loop_state" doc:"Main runner FSM state"`
		Queued         int    `json:"queued" doc:"Follow-up queries waiting for the current turn to end"`
		ConversationID string `json:"conversation_id" doc:"Main agent conversation ID (resume handle)"`
		Model          string `json:"model" doc:"Active model"`
		Vision         bool   `json:"vision" doc:"True when the active model accepts image input"`
		ContextWindow  int    `json:"context_window" doc:"Active model's usable context window in tokens, 0 when unknown"`
		Cwd            string `json:"cwd" doc:"Working directory, for shortening tool-call paths in the UI"`
		Home           string `json:"home" doc:"User home directory, for ~-shortening tool-call paths in the UI"`
		Tools          int    `json:"tools" doc:"Number of registered tools"`
	}
}

type sessionsOutput struct {
	Body struct {
		Sessions []sessionInfo `json:"sessions" doc:"Sessions recorded for this working directory, newest first"`
	}
}

type sessionInfo struct {
	ConversationID string `json:"conversation_id"`
	Name           string `json:"name,omitempty" doc:"User-assigned name, empty when never renamed"`
	Model          string `json:"model"`
	Created        string `json:"created"`
	Modified       string `json:"modified"`
	Entries        int    `json:"entries"`
	Active         bool   `json:"active" doc:"True for the currently running conversation"`
}

type sessionIDInput struct {
	ID string `path:"id" doc:"Conversation ID"`
}

type sessionRenameInput struct {
	ID   string `path:"id" doc:"Conversation ID"`
	Body struct {
		Name string `json:"name" doc:"New session name"`
	}
}

type messagesOutput struct {
	Body struct {
		ConversationID string           `json:"conversation_id"`
		Messages       []messageSummary `json:"messages" doc:"Conversation history reconstructed from the session log"`
	}
}

type messageSummary struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type resumeInput struct {
	Body struct {
		ConversationID string `json:"conversation_id" doc:"Recorded session to load into the running harness"`
	}
}

type compactInput struct {
	Body struct {
		Instructions string `json:"instructions,omitempty" doc:"Optional steering for the summary"`
	}
}

type thinkingInput struct {
	Body struct {
		Enabled bool `json:"enabled" doc:"Turn model reasoning on or off"`
	}
}

type trustInput struct {
	Body struct {
		Trusted bool `json:"trusted" doc:"Whether project-local config in the server's working directory may be loaded"`
	}
}

type trustOutput struct {
	Body struct {
		Cwd     string `json:"cwd" doc:"Directory the decision applies to"`
		Trusted bool   `json:"trusted" doc:"Current trust decision"`
		Source  string `json:"source" doc:"Where the decision came from: persisted, env, default, or error"`
		Note    string `json:"note,omitempty" doc:"Additional context"`
	}
}

type modelInput struct {
	Body struct {
		Model string `json:"model" doc:"Model ref as provider/model-name"`
	}
}

type modelsOutput struct {
	Body struct {
		Current string   `json:"current" doc:"Active model name"`
		Models  []string `json:"models" doc:"Resolvable provider/model-name refs"`
	}
}

type statsOutput struct {
	Body costs.Stats
}

type approveInput struct {
	Body struct {
		CallID   string `json:"call_id" doc:"Tool-call ID from the approval_requested event"`
		Approved bool   `json:"approved" doc:"true to run the tool, false to deny"`
		Allow    string `json:"allow,omitempty" doc:"bash glob to add to the settings file's allow list; implies approved, bash calls only"`
	}
}

type previewInput struct {
	Body struct {
		CallID string `json:"call_id" doc:"Tool-call ID from the approval_requested event"`
	}
}

type previewOutput struct {
	Body struct {
		Diff    string `json:"diff,omitempty" doc:"Unified diff the pending call would produce"`
		Added   int    `json:"added"`
		Removed int    `json:"removed"`
		Omitted string `json:"omitted,omitempty" doc:"Why the diff body is absent: too large, binary, ..."`
		Error   string `json:"error,omitempty" doc:"Why no diff could be computed; the call is still answerable"`
	}
}

type suggestInput struct {
	Body struct {
		CallID string `json:"call_id" doc:"Tool-call ID from the approval_requested event"`
	}
}

type suggestOutput struct {
	Body struct {
		Glob   string `json:"glob,omitempty" doc:"Allow glob proposed for the pending bash call"`
		Reason string `json:"reason,omitempty" doc:"Why no glob is proposed"`
	}
}
