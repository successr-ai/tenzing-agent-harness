package builtins

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

const (
	// maxDiffLines is the hard limit: a unified diff longer than this is
	// dropped entirely, leaving only the +/- counts.
	maxDiffLines = 50
	// maxInlineDiffLines is the soft limit: a diff longer than this is kept
	// but rendered collapsed by default. Consumed by the UI, not here.
	maxInlineDiffLines = 20
	// maxDiffFileLines skips diffing altogether — difflib's matcher is
	// O(n*m), so a generated or vendored file of this size would stall the
	// tool for a diff nobody would read.
	maxDiffFileLines = 20000
	// diffContext is the number of unchanged lines shown around each hunk.
	diffContext = 3
)

// Diff is the outcome of diffing one file edit. Text is empty exactly when
// Omitted is set; Added and Removed are populated whenever they are known.
type Diff struct {
	Text    string
	Added   int
	Removed int
	Omitted string // "", "too large", "file too large", "binary"
}

// Summary renders the counts for a tool result, e.g. "+3 -1".
func (d Diff) Summary() string {
	return fmt.Sprintf("+%d -%d", d.Added, d.Removed)
}

// Inline reports whether the diff is short enough to show expanded by
// default. False for an omitted diff.
func (d Diff) Inline() bool {
	return d.Text != "" && strings.Count(d.Text, "\n") <= maxInlineDiffLines
}

// DiffFiles produces a unified diff of one file's before and after content.
// A nil old is a newly created file (all additions). Binary content, an
// oversized file, and an oversized diff each yield an empty Text with
// Omitted saying which.
func DiffFiles(name string, old, new []byte) Diff {
	if isBinary(old) || isBinary(new) {
		return Diff{Omitted: "binary"}
	}

	oldLines := splitLines(string(old))
	newLines := splitLines(string(new))
	if len(oldLines) > maxDiffFileLines || len(newLines) > maxDiffFileLines {
		return Diff{Omitted: "file too large"}
	}

	text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        oldLines,
		B:        newLines,
		FromFile: "a/" + name,
		ToFile:   "b/" + name,
		Context:  diffContext,
	})
	if err != nil {
		// Diffing is cosmetic: report no diff rather than failing the edit.
		return Diff{Omitted: "unavailable"}
	}

	d := Diff{Text: text}
	lines := 0
	for _, l := range strings.Split(text, "\n") {
		lines++
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			d.Added++
		case strings.HasPrefix(l, "-"):
			d.Removed++
		}
	}
	if lines > maxDiffLines {
		d.Text = ""
		d.Omitted = "too large"
	}
	return d
}

// splitLines splits content into difflib's expected form: one entry per
// line, each keeping its trailing newline. A final line without one gets it
// added — difflib would otherwise run it into the next diff line, which
// breaks both rendering and the +/- counts. The cost is that a missing
// trailing newline is not itself shown as a change (git's "\ No newline at
// end of file"), which no consumer here needs.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	// SplitAfter leaves a trailing "" when the content ends in a newline.
	if last := len(lines) - 1; lines[last] == "" {
		lines = lines[:last]
	} else {
		lines[last] += "\n"
	}
	return lines
}

// diffMetadata renders a Diff as tool-result metadata. The counts are always
// present; "diff" and "diff_omitted" are mutually exclusive.
func diffMetadata(d Diff) map[string]string {
	m := map[string]string{
		"diff_added":   fmt.Sprint(d.Added),
		"diff_removed": fmt.Sprint(d.Removed),
	}
	if d.Omitted != "" {
		m["diff_omitted"] = d.Omitted
	} else {
		m["diff"] = d.Text
	}
	return m
}

// PreviewDiff computes the diff a pending Edit or Write call would produce,
// without touching the file. It shares applyEdit with the Edit tool so the
// preview and the real thing cannot drift apart.
//
// ok is false for any other tool. The file is read at preview time, so a
// concurrent change makes the preview stale — cosmetic only, since the
// FileTracker still verifies the content the real edit is based on.
func PreviewDiff(workingDir, toolName, arguments string) (d Diff, ok bool, err error) {
	var input struct {
		FilePath   string `json:"file_path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
		Content    string `json:"content"`
	}
	switch toolName {
	case "Edit", "Write":
		if err := json.Unmarshal([]byte(arguments), &input); err != nil {
			return Diff{}, true, fmt.Errorf("invalid input JSON: %w", err)
		}
	default:
		return Diff{}, false, nil
	}
	if input.FilePath == "" {
		return Diff{}, true, errors.New("file_path is required")
	}

	path := resolvePath(workingDir, input.FilePath)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Diff{}, true, fmt.Errorf("cannot read file: %w", err)
	}
	if os.IsNotExist(err) {
		existing = nil
	}

	if toolName == "Write" {
		return DiffFiles(input.FilePath, existing, []byte(input.Content)), true, nil
	}
	if existing == nil {
		return Diff{}, true, errors.New("cannot read file: no such file")
	}
	updated, aerr := applyEdit(string(existing), input.OldString, input.NewString, input.ReplaceAll)
	if aerr != nil {
		return Diff{}, true, aerr
	}
	return DiffFiles(input.FilePath, existing, []byte(updated)), true, nil
}
