package todo

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

type TaskPriority string

const (
	PriorityHigh   TaskPriority = "high"
	PriorityMedium TaskPriority = "medium"
	PriorityLow    TaskPriority = "low"
)

type Task struct {
	ID          string       `json:"id"`
	Description string       `json:"description"`
	Status      string       `json:"status"`
	Priority    TaskPriority `json:"priority"`
	DependsOn   []string     `json:"depends_on"`
	Result      string       `json:"result,omitempty"`
}

// TodoFile is an in-memory, per-instance todo store. Each harness or
// subagent constructs its own via NewTodoStore, so concurrent runners in
// one process never share (or clobber) each other's plans.
type TodoFile struct {
	mu      sync.Mutex
	tasks   []Task
	emitter core.Emitter
}

func NewTodoStore() *TodoFile {
	return &TodoFile{}
}

func (f *TodoFile) SetEmitter(e core.Emitter) {
	f.emitter = e
}

func (f *TodoFile) GetTools() []tooldef.Definition {
	return []tooldef.Definition{
		NewTodoWriteTool(f),
		NewTodoCreateTool(f),
		NewTodoUpdateTool(f),
		NewTodoNextTool(f),
		NewTodoReadTool(f),
	}
}

func (f *TodoFile) ReadTasks() ([]Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.load(), nil
}

func (f *TodoFile) WriteTasks(tasks []Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.save(tasks)
	return nil
}

func (f *TodoFile) CreateTask(desc string, dependsOn []string, priority TaskPriority) (Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	tasks := f.load()

	if priority == "" {
		priority = PriorityMedium
	}
	if dependsOn == nil {
		dependsOn = []string{}
	}

	for _, dep := range dependsOn {
		if !taskExists(tasks, dep) {
			return Task{}, fmt.Errorf("dependency %q not found", dep)
		}
	}

	task := Task{
		ID:          randomID(8),
		Description: desc,
		Status:      "pending",
		Priority:    priority,
		DependsOn:   dependsOn,
	}

	tasks = append(tasks, task)
	f.save(tasks)

	if f.emitter != nil {
		f.emitter.Emit(core.TaskCreatedEvent{
			BaseEvent:   core.NewBaseEvent(core.EventTaskCreated, ""),
			TaskID:      task.ID,
			Description: desc,
		})
	}

	return task, nil
}

func (f *TodoFile) UpdateTask(taskID string, status string, result string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	tasks := f.load()

	found := false
	updated := make([]Task, len(tasks))
	for i, t := range tasks {
		if t.ID == taskID || strings.HasPrefix(t.ID, taskID) {
			updated[i] = Task{
				ID:          t.ID,
				Description: t.Description,
				Status:      status,
				Priority:    t.Priority,
				DependsOn:   t.DependsOn,
				Result:      result,
			}
			found = true
		} else {
			updated[i] = t
		}
	}

	if !found {
		return fmt.Errorf("task %q not found in current plan; call TodoRead to see valid task IDs or TodoWrite to create a plan", taskID)
	}

	f.save(updated)

	if f.emitter != nil && status == "done" {
		f.emitter.Emit(core.TaskCompletedEvent{
			BaseEvent: core.NewBaseEvent(core.EventTaskCompleted, ""),
			TaskID:    taskID,
		})
	}

	return nil
}

func (f *TodoFile) NextTask() (Task, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	tasks := f.load()

	doneIDs := make(map[string]struct{})
	for _, t := range tasks {
		if t.Status == "done" {
			doneIDs[t.ID] = struct{}{}
		}
	}

	priorityOrder := map[TaskPriority]int{PriorityHigh: 0, PriorityMedium: 1, PriorityLow: 2}

	var pending []Task
	for _, t := range tasks {
		if t.Status != "pending" {
			continue
		}
		if allDepsDone(t.DependsOn, doneIDs) {
			pending = append(pending, t)
		}
	}

	sort.SliceStable(pending, func(i, j int) bool {
		return priorityOrder[pending[i].Priority] < priorityOrder[pending[j].Priority]
	})

	if len(pending) == 0 {
		return Task{}, false, nil
	}

	return pending[0], true, nil
}

func (f *TodoFile) FormatReminder() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	tasks := f.load()
	if len(tasks) == 0 {
		return ""
	}

	sorted := topoSort(tasks)

	var b strings.Builder
	b.WriteString("<system-reminder>\nCurrent plan:\n")
	for _, t := range sorted {
		fmt.Fprintf(&b, "[%s] [%s] %s", abbreviateID(t.ID), t.Status, t.Description)
		if len(t.DependsOn) > 0 {
			fmt.Fprintf(&b, " (depends: %s)", strings.Join(abbreviateIDs(t.DependsOn), ", "))
		}
		b.WriteString("\n")
	}
	b.WriteString("</system-reminder>")
	return b.String()
}

// --- internal helpers ---

// load returns a deep copy of the task list; callers may mutate it (including
// DependsOn) without affecting the store. Callers must hold f.mu.
func (f *TodoFile) load() []Task {
	return copyTasks(f.tasks)
}

// save replaces the task list with a deep copy of tasks. Callers must hold f.mu.
func (f *TodoFile) save(tasks []Task) {
	f.tasks = copyTasks(tasks)
}

func copyTasks(tasks []Task) []Task {
	out := make([]Task, len(tasks))
	for i, t := range tasks {
		out[i] = t
		out[i].DependsOn = append([]string(nil), t.DependsOn...)
	}
	return out
}

func taskExists(tasks []Task, id string) bool {
	for _, t := range tasks {
		if t.ID == id || strings.HasPrefix(t.ID, id) {
			return true
		}
	}
	return false
}

func allDepsDone(deps []string, doneIDs map[string]struct{}) bool {
	for _, dep := range deps {
		if _, ok := doneIDs[dep]; !ok {
			return false
		}
	}
	return true
}

func topoSort(tasks []Task) []Task {
	idIndex := make(map[string]int, len(tasks))
	for i, t := range tasks {
		idIndex[t.ID] = i
	}

	visited := make(map[string]bool, len(tasks))
	var result []Task
	var visit func(id string)
	visit = func(id string) {
		if visited[id] {
			return
		}
		visited[id] = true
		idx, ok := idIndex[id]
		if !ok {
			return
		}
		for _, dep := range tasks[idx].DependsOn {
			visit(dep)
		}
		result = append(result, tasks[idx])
	}

	for _, t := range tasks {
		visit(t.ID)
	}
	return result
}

func abbreviateID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func abbreviateIDs(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = abbreviateID(id)
	}
	return out
}

func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
