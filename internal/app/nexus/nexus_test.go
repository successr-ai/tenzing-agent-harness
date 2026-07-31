package nexus

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

func newTestNexus(t *testing.T, cfgs []ChannelConfig, emit func(core.Event), notify func(string)) *Nexus {
	t.Helper()
	// apply defaults the way LoadConfig does
	for i := range cfgs {
		if cfgs[i].ErrorPattern == "" {
			cfgs[i].ErrorPattern = DefaultErrorPattern
		}
		if cfgs[i].BufferSize == 0 {
			cfgs[i].BufferSize = 10
		}
	}
	n, err := New(Config{Channels: cfgs}, emit, notify)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNexusIngestReadSearch(t *testing.T) {
	n := newTestNexus(t, []ChannelConfig{{Name: "a", Type: TypeWebhook}}, nil, nil)

	n.Ingest("a", "hello world")
	n.Ingest("a", "error: kaboom")
	n.Ingest("a", "goodbye")

	all, err := n.Read("a", 10, false)
	if err != nil || len(all) != 3 {
		t.Fatalf("Read all = %d entries (err %v), want 3", len(all), err)
	}

	errs, err := n.Read("a", 10, true)
	if err != nil || len(errs) != 1 || errs[0].Text != "error: kaboom" {
		t.Fatalf("Read errorsOnly = %v (err %v)", errs, err)
	}

	found, err := n.Search("a", "kab.om", 10)
	if err != nil || len(found) != 1 {
		t.Fatalf("Search = %v (err %v), want 1 match", found, err)
	}

	if _, err := n.Read("nope", 10, false); err == nil {
		t.Error("Read unknown channel should error")
	}
	if _, err := n.Search("a", "(bad", 10); err == nil {
		t.Error("Search bad regex should error")
	}
	if err := n.Ingest("nope", "x"); err == nil {
		t.Error("Ingest unknown channel should error")
	}
}

func TestNexusErrorEmitsEventAndNotifies(t *testing.T) {
	var mu sync.Mutex
	var emitted []core.Event
	var notified []string

	n := newTestNexus(t, []ChannelConfig{{Name: "a", Type: TypeWebhook}},
		func(e core.Event) { mu.Lock(); emitted = append(emitted, e); mu.Unlock() },
		func(ch string) { mu.Lock(); notified = append(notified, ch); mu.Unlock() },
	)

	n.Ingest("a", "all fine")
	n.Ingest("a", "ERROR: bad")

	mu.Lock()
	defer mu.Unlock()
	if len(notified) != 1 || notified[0] != "a" {
		t.Errorf("notified = %v, want [a]", notified)
	}
	var errEvents int
	for _, e := range emitted {
		if ce, ok := e.(ChannelErrorEvent); ok {
			errEvents++
			if ce.Channel != "a" || ce.Text != "ERROR: bad" {
				t.Errorf("ChannelErrorEvent = %+v", ce)
			}
		}
	}
	if errEvents != 1 {
		t.Errorf("ChannelErrorEvent count = %d, want 1", errEvents)
	}
}

func TestNexusTriggerFalseDoesNotNotify(t *testing.T) {
	f := false
	var notified []string
	var mu sync.Mutex
	n := newTestNexus(t, []ChannelConfig{{Name: "a", Type: TypeWebhook, Trigger: &f}},
		nil,
		func(ch string) { mu.Lock(); notified = append(notified, ch); mu.Unlock() },
	)
	n.Ingest("a", "ERROR: bad")
	mu.Lock()
	defer mu.Unlock()
	if len(notified) != 0 {
		t.Errorf("trigger:false channel notified = %v, want none", notified)
	}
}

func TestNexusChannelInfos(t *testing.T) {
	n := newTestNexus(t, []ChannelConfig{
		{Name: "b", Type: TypeWebhook},
		{Name: "a", Type: TypeWebhook},
	}, nil, nil)
	n.Ingest("b", "error: x")

	infos := n.ChannelInfos()
	if len(infos) != 2 {
		t.Fatalf("infos = %d, want 2", len(infos))
	}
	// config order preserved
	if infos[0].Name != "b" || infos[1].Name != "a" {
		t.Errorf("order = [%s %s], want [b a]", infos[0].Name, infos[1].Name)
	}
	if infos[0].Count != 1 || infos[0].ErrorCount != 1 {
		t.Errorf("b counts = (%d,%d), want (1,1)", infos[0].Count, infos[0].ErrorCount)
	}
}

func TestNexusStartStopFileTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	os.WriteFile(path, []byte(""), 0644)

	n := newTestNexus(t, []ChannelConfig{{Name: "f", Type: TypeFileTail, Path: path}}, nil, nil)
	n.Start(t.Context())

	// status becomes running
	if !waitFor(t, 2*time.Second, func() bool {
		return n.ChannelInfos()[0].Status == "running"
	}) {
		t.Fatalf("status = %s, want running", n.ChannelInfos()[0].Status)
	}

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString("tailed-line\n")
	f.Close()

	if !waitFor(t, 2*time.Second, func() bool {
		entries, _ := n.Read("f", 10, false)
		return len(entries) == 1
	}) {
		t.Fatal("file-tail entry not ingested via nexus")
	}

	n.Stop() // must return (waits for goroutines)
	if got := n.ChannelInfos()[0].Status; got != "stopped" {
		t.Errorf("status after Stop = %s, want stopped", got)
	}
}
