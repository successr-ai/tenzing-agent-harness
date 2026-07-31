package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/todo"
)

var testNow = func() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) }

func newTestStore(t *testing.T, dir, convID string) *Store {
	t.Helper()
	s := NewStore(dir, "/proj", convID, "test-model", testNow)
	t.Cleanup(s.Close)
	return s
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func TestStoreWritesHeaderAndEntries(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, "conv1")

	s.Append(Entry{Type: TypeUser, Text: "hello"})
	s.Append(Entry{Type: TypeAssistant, Text: "hi", Model: "m"})
	s.Append(Entry{Type: TypeToolResult, Tool: "Read", Input: `{"file_path":"x"}`, Output: "contents"})

	lines := readLines(t, s.Path())
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want header + 3 entries", len(lines))
	}

	var h Header
	if err := json.Unmarshal([]byte(lines[0]), &h); err != nil || h.Type != TypeHeader {
		t.Fatalf("first line not a header: %v %q", err, lines[0])
	}
	if h.Version != Version || h.ConversationID != "conv1" || h.Model != "test-model" || h.Cwd != "/proj" {
		t.Errorf("header = %+v", h)
	}

	var prev string
	for i, line := range lines[1:] {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("entry %d invalid JSON: %v", i, err)
		}
		if e.ID == "" {
			t.Errorf("entry %d missing id", i)
		}
		if e.ParentID != prev {
			t.Errorf("entry %d parent_id = %q, want %q", i, e.ParentID, prev)
		}
		prev = e.ID
	}
}

func TestStoreResumeAppendsToSameFile(t *testing.T) {
	dir := t.TempDir()
	s1 := newTestStore(t, dir, "conv1")
	s1.Append(Entry{Type: TypeUser, Text: "first"})
	s1.Close()

	s2 := newTestStore(t, dir, "conv1")
	s2.Append(Entry{Type: TypeUser, Text: "second"})

	if s1.Path() == "" || s2.Path() != s1.Path() {
		t.Fatalf("resume path = %q, want same as %q", s2.Path(), s1.Path())
	}
	lines := readLines(t, s2.Path())
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want header + 2 entries (one file)", len(lines))
	}
	// parent chain continues across the restart
	var e1, e2 Entry
	json.Unmarshal([]byte(lines[1]), &e1)
	json.Unmarshal([]byte(lines[2]), &e2)
	if e2.ParentID != e1.ID {
		t.Errorf("resumed entry parent = %q, want %q", e2.ParentID, e1.ID)
	}
}

func TestStoreInertWhenNoDir(t *testing.T) {
	s := NewStore("", "/proj", "conv1", "m", testNow)
	s.Append(Entry{Type: TypeUser, Text: "dropped"})
	if p := s.Path(); p != "" {
		t.Errorf("inert store path = %q, want empty", p)
	}
}

func TestStoreConcurrentAppends(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, "conv1")

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			s.Append(Entry{Type: TypeUser, Text: fmt.Sprintf("msg-%d", i)})
		})
	}
	wg.Wait()

	lines := readLines(t, s.Path())
	if len(lines) != 21 {
		t.Fatalf("lines = %d, want header + 20 entries", len(lines))
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("invalid JSON line: %q", line)
		}
	}
}

func TestLoadReconstructsHistory(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, "conv1")
	s.Append(Entry{Type: TypeUser, Text: "what is in main.go?"})
	s.Append(Entry{Type: TypeToolResult, Tool: "Read", Input: `{"file_path":"main.go"}`, Output: "package main"})
	s.Append(Entry{Type: TypeAssistant, Text: "It declares package main.", Model: "m"})
	s.Append(Entry{Type: TypeSteering, Text: "be brief"})
	s.Append(Entry{Type: TypeTodo, Tasks: []todo.Task{{ID: "t1", Description: "do it", Status: "pending"}}})
	s.Close()

	res, err := Load(dir, "/proj", "conv1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res == nil {
		t.Fatal("Load returned nil for existing session")
	}
	if len(res.History) != 4 {
		t.Fatalf("history = %d messages, want 4", len(res.History))
	}
	roles := []string{"user", "user", "assistant", "user"}
	for i, m := range res.History {
		if string(m.Role) != roles[i] {
			t.Errorf("message %d role = %q, want %q", i, m.Role, roles[i])
		}
	}
	if !strings.Contains(res.History[1].Content[0].Text, "[tool Read result]") {
		t.Errorf("tool result not labeled: %q", res.History[1].Content[0].Text)
	}
	if len(res.Tasks) != 1 || res.Tasks[0].ID != "t1" {
		t.Errorf("tasks = %+v, want restored t1", res.Tasks)
	}
}

func TestLoadCompactionRestartsFromSummary(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, "conv1")
	for i := range 10 {
		s.Append(Entry{Type: TypeUser, Text: fmt.Sprintf("old-%d", i)})
	}
	s.Append(Entry{Type: TypeCompaction, Summary: "THE-SUMMARY"})
	s.Append(Entry{Type: TypeUser, Text: "after compaction"})
	s.Close()

	res, err := Load(dir, "/proj", "conv1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// summary exchange (2) + retained tail (6) + post-compaction (1)
	if len(res.History) != 9 {
		t.Fatalf("history = %d messages, want 9", len(res.History))
	}
	if !strings.Contains(res.History[0].Content[0].Text, "THE-SUMMARY") {
		t.Errorf("first message should carry the summary: %q", res.History[0].Content[0].Text)
	}
	if got := res.History[2].Content[0].Text; got != "old-4" {
		t.Errorf("tail starts at %q, want old-4 (last 6 kept)", got)
	}
	if got := res.History[8].Content[0].Text; got != "after compaction" {
		t.Errorf("last message = %q", got)
	}
}

func TestLoadTruncatesHugeToolOutputs(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, "conv1")
	s.Append(Entry{Type: TypeToolResult, Tool: "Read", Output: strings.Repeat("x", replayToolOutputMax+100)})
	s.Close()

	res, err := Load(dir, "/proj", "conv1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	text := res.History[0].Content[0].Text
	if !strings.Contains(text, "[truncated on replay]") {
		t.Error("oversize tool output not truncated on replay")
	}
	if len(text) > replayToolOutputMax+200 {
		t.Errorf("replayed output len = %d, want capped", len(text))
	}
}

// Model-change entries are informational; thinking entries re-apply (last
// wins). Neither leaks into the reconstructed history.
func TestLoadSettingsEntries(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, "conv1")
	s.Append(Entry{Type: TypeUser, Text: "hi"})
	s.Append(Entry{Type: TypeModelChange, Model: "new-model", Text: "old-model"})
	s.Append(Entry{Type: TypeThinking, Text: "on"})
	s.Append(Entry{Type: TypeThinking, Text: "off"})
	s.Close()

	res, err := Load(dir, "/proj", "conv1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(res.History) != 1 {
		t.Fatalf("history = %d messages, want 1 (settings entries skipped)", len(res.History))
	}
	if res.Thinking == nil || *res.Thinking != false {
		t.Errorf("thinking = %v, want false (last entry wins)", res.Thinking)
	}
}

func TestLoadMissingSessionReturnsNil(t *testing.T) {
	res, err := Load(t.TempDir(), "/proj", "nope")
	if err != nil || res != nil {
		t.Fatalf("Load missing = (%v, %v), want (nil, nil)", res, err)
	}
}

func TestLoadSkipsTornFinalLine(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, "conv1")
	s.Append(Entry{Type: TypeUser, Text: "intact"})
	path := s.Path()
	s.Close()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"id":"dead","type":"user","tex`) // crash mid-write
	f.Close()

	res, err := Load(dir, "/proj", "conv1")
	if err != nil {
		t.Fatalf("Load with torn tail: %v", err)
	}
	if len(res.History) != 1 {
		t.Fatalf("history = %d, want 1 (torn line dropped)", len(res.History))
	}
}

func TestLoadRejectsCorruptMiddleLine(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, cwdHash("/proj"))
	os.MkdirAll(sub, 0o755)
	content := `{"type":"session","version":1,"conversation_id":"conv1"}
{"id":"a","type":"user","broken
{"id":"b","type":"user","text":"later"}
`
	os.WriteFile(filepath.Join(sub, "20260725T000000Z_conv1.jsonl"), []byte(content), 0o644)

	if _, err := Load(dir, "/proj", "conv1"); err == nil {
		t.Fatal("corrupt middle line should fail the load")
	}
}
