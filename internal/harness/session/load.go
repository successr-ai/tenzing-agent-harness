package session

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/todo"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// keepRecentOnCompaction mirrors the compressor's keep-last-verbatim count:
// on a compaction entry, replay restarts from the summary plus this many
// most-recent reconstructed messages (the retained tail).
const keepRecentOnCompaction = 6

// replayToolOutputMax caps each tool output replayed into the resumed
// history. The full output stays in the session file; the model saw it once
// already, so a truncated replay preserves recall without re-paying the
// full context cost.
const replayToolOutputMax = 4000

// LoadResult is a conversation reconstructed from a session file.
type LoadResult struct {
	// History is the conversation replayed as provider-neutral messages,
	// ready to seed the agent context.
	History []common.Message
	// Tasks is the latest todo snapshot, nil when none was persisted.
	Tasks []todo.Task
	// Thinking is the last persisted reasoning toggle, nil when never set.
	Thinking *bool
	// Path is the session file the result came from.
	Path string
}

// Load reconstructs the latest session for conversationID under dir. It
// returns (nil, nil) when no session file exists, and an error only for a
// file that exists but cannot be parsed.
func Load(dir, cwd, conversationID string) (*LoadResult, error) {
	if dir == "" {
		return nil, nil
	}
	path := findLatest(dir, cwd, conversationID)
	if path == "" {
		return nil, nil
	}
	entries, _, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("load session %s: %w", path, err)
	}

	var history []common.Message
	var tasks []todo.Task
	var thinking *bool
	for _, e := range entries {
		switch e.Type {
		case TypeUser, TypeSteering:
			history = append(history, common.NewUserMessage(e.Text))
		case TypeImage:
			// Reconstruct from the sidecar blob; a missing blob degrades to
			// a text placeholder rather than failing the resume.
			raw, err := readBlob(path, e.Blob)
			if err != nil {
				history = append(history, common.NewUserMessage(
					fmt.Sprintf("[an image (%s) was attached here but is no longer available]", e.MediaType)))
				continue
			}
			history = append(history, common.Message{
				Role:    common.RoleUser,
				Content: []common.ContentBlock{common.NewImageContent(e.MediaType, base64.StdEncoding.EncodeToString(raw))},
			})
		case TypeAssistant:
			history = append(history, common.NewAssistantMessage(e.Text))
		case TypeToolResult:
			out := e.Output
			if len(out) > replayToolOutputMax {
				out = out[:replayToolOutputMax] + "\n...[truncated on replay]"
			}
			label := "result"
			if e.IsError {
				label = "error"
			}
			history = append(history, common.NewUserMessage(
				fmt.Sprintf("[tool %s %s]\n%s", e.Tool, label, out)))
		case TypeCompaction:
			// Restart from the summary, keeping the retained tail verbatim.
			tail := history
			if len(tail) > keepRecentOnCompaction {
				tail = tail[len(tail)-keepRecentOnCompaction:]
			}
			history = append([]common.Message{
				common.NewUserMessage("[Context summary from previous conversation]\n\n" + e.Summary),
				common.NewAssistantMessage("Understood. I have the full context from our previous work."),
			}, tail...)
		case TypeTodo:
			tasks = e.Tasks
		case TypeThinking:
			on := e.Text == "on"
			thinking = &on
		}
	}

	return &LoadResult{History: history, Tasks: tasks, Thinking: thinking, Path: path}, nil
}

// readFile parses a session file into its entries and header. Unparseable
// lines fail the whole read except a torn final line (crash mid-write),
// which is skipped — the file stays loadable.
func readFile(path string) ([]Entry, Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, Header{}, err
	}
	defer f.Close()

	var header Header
	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	lineNo := 0
	var pendingErr error
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		// A parse error only counts if lines follow it; a bad last line is
		// a torn append from a crash and is dropped.
		if pendingErr != nil {
			return nil, Header{}, pendingErr
		}
		if lineNo == 1 {
			if err := json.Unmarshal(line, &header); err != nil || header.Type != TypeHeader {
				return nil, Header{}, fmt.Errorf("line 1 is not a session header: %v", err)
			}
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			pendingErr = fmt.Errorf("line %d: %w", lineNo, err)
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, Header{}, err
	}
	return entries, header, nil
}
