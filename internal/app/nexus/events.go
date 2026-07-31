package nexus

import "github.com/successr-ai/tenzing-agent-harness/internal/core"

// Nexus event types published on the harness event bus. Defined here (not
// in core) so core stays ignorant of nexus.
const (
	EventChannelError  core.EventType = "nexus.channel_error"
	EventChannelStatus core.EventType = "nexus.channel_status"
	EventTrigger       core.EventType = "nexus.trigger"
)

const runnerID = "nexus"

// ChannelErrorEvent fires for each buffered line matching a channel's
// error pattern.
type ChannelErrorEvent struct {
	core.BaseEvent
	Channel string `json:"channel"`
	Text    string `json:"text"`
	Seq     uint64 `json:"seq"`
}

// ChannelStatusEvent fires on source lifecycle changes
// (running/restarting/stopped).
type ChannelStatusEvent struct {
	core.BaseEvent
	Channel string `json:"channel"`
	State   string `json:"state"`
}

// TriggerEvent fires when an error-triggered agent turn actually starts.
type TriggerEvent struct {
	core.BaseEvent
	Channels []string `json:"channels"`
}
