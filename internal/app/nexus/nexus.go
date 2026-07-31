package nexus

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

const (
	fileTailPollInterval = 500 * time.Millisecond
	commandRestartBase   = time.Second
	commandRestartCap    = 30 * time.Second
)

// Nexus owns all configured channels and their source goroutines.
type Nexus struct {
	channels map[string]*Channel
	order    []string
	emit     func(core.Event)

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// ChannelInfo is a read-only summary of one channel.
type ChannelInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Count      int    `json:"count"`
	ErrorCount int    `json:"error_count"`
}

// New builds a Nexus from a validated config. emit publishes nexus events
// (nil = no events); notify is called with the channel name for each error
// entry on trigger-enabled channels (nil = no trigger).
func New(cfg Config, emit func(core.Event), notify func(channel string)) (*Nexus, error) {
	if emit == nil {
		emit = func(core.Event) {}
	}
	n := &Nexus{
		channels: make(map[string]*Channel, len(cfg.Channels)),
		emit:     emit,
	}

	for _, cc := range cfg.Channels {
		cc := cc
		onError := func(e Entry) {
			n.emit(ChannelErrorEvent{
				BaseEvent: core.NewBaseEvent(EventChannelError, runnerID),
				Channel:   cc.Name,
				Text:      e.Text,
				Seq:       e.Seq,
			})
			if notify != nil && cc.TriggerEnabled() {
				notify(cc.Name)
			}
		}
		ch, err := newChannel(cc, onError)
		if err != nil {
			return nil, err
		}
		n.channels[cc.Name] = ch
		n.order = append(n.order, cc.Name)
	}
	return n, nil
}

// Start launches source goroutines for file-tail and command channels.
// Webhook channels have no goroutine; they become "running" immediately.
func (n *Nexus) Start(ctx context.Context) {
	ctx, n.cancel = context.WithCancel(ctx)

	for _, name := range n.order {
		chRef := n.channels[name]
		statusFunc := func(s string) {
			chRef.setStatus(s)
			n.emit(ChannelStatusEvent{
				BaseEvent: core.NewBaseEvent(EventChannelStatus, runnerID),
				Channel:   chRef.cfg.Name,
				State:     s,
			})
		}

		switch chRef.cfg.Type {
		case TypeFileTail:
			n.wg.Add(1)
			go func(ch *Channel, status func(string)) {
				defer n.wg.Done()
				runFileTail(ctx, ch.cfg.Path, fileTailPollInterval, func(s string) { ch.Ingest(s) }, status)
			}(chRef, statusFunc)
		case TypeCommand:
			n.wg.Add(1)
			go func(ch *Channel, status func(string)) {
				defer n.wg.Done()
				runCommand(ctx, ch.cfg.Cmd, commandRestartBase, commandRestartCap, func(s string) { ch.Ingest(s) }, status)
			}(chRef, statusFunc)
		case TypeWebhook:
			statusFunc("running")
		}
	}
}

// Stop cancels all source goroutines and waits for them. Webhook channels
// are marked stopped.
func (n *Nexus) Stop() {
	if n.cancel != nil {
		n.cancel()
	}
	n.wg.Wait()
	for _, name := range n.order {
		ch := n.channels[name]
		if ch.cfg.Type == TypeWebhook {
			ch.setStatus("stopped")
		}
	}
}

// Ingest appends one message to the named channel. Used by the webhook
// handler and tests.
func (n *Nexus) Ingest(name, text string) error {
	ch, ok := n.channels[name]
	if !ok {
		return fmt.Errorf("unknown channel %q", name)
	}
	ch.Ingest(text)
	return nil
}

// ChannelInfos returns a summary of every channel in config order.
func (n *Nexus) ChannelInfos() []ChannelInfo {
	out := make([]ChannelInfo, 0, len(n.order))
	for _, name := range n.order {
		ch := n.channels[name]
		total, errs := ch.ring.Counts()
		out = append(out, ChannelInfo{
			Name:       ch.cfg.Name,
			Type:       ch.cfg.Type,
			Status:     ch.Status(),
			Count:      total,
			ErrorCount: errs,
		})
	}
	return out
}

// Read returns up to lastN most recent entries (oldest→newest), optionally
// errors only.
func (n *Nexus) Read(name string, lastN int, errorsOnly bool) ([]Entry, error) {
	ch, ok := n.channels[name]
	if !ok {
		return nil, fmt.Errorf("unknown channel %q", name)
	}
	return ch.ring.Last(lastN, errorsOnly), nil
}

// Search returns the most recent `limit` buffered entries matching the
// regex pattern, oldest→newest.
func (n *Nexus) Search(name, pattern string, limit int) ([]Entry, error) {
	ch, ok := n.channels[name]
	if !ok {
		return nil, fmt.Errorf("unknown channel %q", name)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	all := ch.ring.Snapshot()
	matches := make([]Entry, 0)
	for _, e := range all {
		if re.MatchString(e.Text) {
			matches = append(matches, e)
		}
	}
	if limit > 0 && len(matches) > limit {
		matches = matches[len(matches)-limit:]
	}
	return matches, nil
}
