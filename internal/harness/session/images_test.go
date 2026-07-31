package session

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// Images flow event → sidecar blob + TypeImage entry → reconstructed image
// block on load; the JSONL line itself carries only media type + hash.
func TestImagePersistAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, "conv1")
	bus := eventbus.NewEventBus()
	defer bus.Close()
	stop := StartPersister(bus, s, "main-id", nil)
	defer stop()

	raw := []byte("fake-png-bytes")
	b64 := base64.StdEncoding.EncodeToString(raw)
	bus.Emit(core.ImagesAttachedEvent{
		BaseEvent: core.NewBaseEvent(core.EventImagesAttached, "main-id"),
		Images:    []core.ImageData{{MediaType: "image/png", Data: b64}},
	})
	bus.Emit(core.TurnStartedEvent{BaseEvent: core.NewBaseEvent(core.EventTurnStarted, "main-id"), Query: "what is this?"})
	// subagent image events must be filtered out
	bus.Emit(core.ImagesAttachedEvent{
		BaseEvent: core.NewBaseEvent(core.EventImagesAttached, "main-id_child"),
		Images:    []core.ImageData{{MediaType: "image/png", Data: b64}},
	})

	var content string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(s.Path()); err == nil {
			content = string(data)
			if strings.Contains(content, "what is this?") {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	if strings.Contains(content, b64) {
		t.Error("base64 image data leaked into the JSONL (should live in the blob sidecar)")
	}
	if strings.Count(content, `"image"`) != 1 {
		t.Errorf("want exactly one image entry (subagent filtered), got:\n%s", content)
	}

	res, err := Load(dir, s.cwd, "conv1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res == nil || len(res.History) != 2 {
		t.Fatalf("history = %+v, want image message + user message", res)
	}
	img := res.History[0]
	if img.Role != common.RoleUser || len(img.Content) != 1 ||
		img.Content[0].Type != common.ContentTypeImage ||
		img.Content[0].Image == nil ||
		img.Content[0].Image.Data != b64 ||
		img.Content[0].Image.MediaType != "image/png" {
		t.Errorf("reconstructed image message = %+v", img)
	}
	if got := res.History[1].Content[0].Text; got != "what is this?" {
		t.Errorf("user message = %q", got)
	}
}

// A deleted blob degrades the resume to a placeholder message, not an error.
func TestImageLoadMissingBlobPlaceholder(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, "conv1")

	raw := []byte("bytes")
	sha := s.SaveBlob(raw)
	if sha == "" {
		t.Fatal("SaveBlob failed")
	}
	s.Append(Entry{Type: TypeImage, Time: time.Now(), MediaType: "image/png", Blob: sha})
	s.Close()

	if err := os.RemoveAll(filepath.Join(filepath.Dir(s.Path()), "blobs")); err != nil {
		t.Fatal(err)
	}

	res, err := Load(dir, s.cwd, "conv1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(res.History) != 1 {
		t.Fatalf("history = %+v, want one placeholder message", res.History)
	}
	text := res.History[0].Content[0].Text
	if !strings.Contains(text, "no longer available") || !strings.Contains(text, "image/png") {
		t.Errorf("placeholder = %q", text)
	}
}

// SaveBlob is content-addressed: same bytes, same hash, one file.
func TestSaveBlobContentAddressed(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, "conv1")
	s.Append(Entry{Type: TypeUser, Time: time.Now(), Text: "seed"}) // creates the session dir

	sha1 := s.SaveBlob([]byte("same"))
	sha2 := s.SaveBlob([]byte("same"))
	if sha1 == "" || sha1 != sha2 {
		t.Fatalf("hashes = %q / %q, want equal and non-empty", sha1, sha2)
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(s.Path()), "blobs"))
	if err != nil || len(entries) != 1 {
		t.Errorf("blob dir entries = %v (err %v), want exactly 1", entries, err)
	}
}
