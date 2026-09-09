package permissions

import (
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// Commands are taken from real approval prompts (TEST_COMMANDS.txt).
func TestSuggest(t *testing.T) {
	tests := []struct {
		name    string
		allow   []string
		command string
		glob    string
		reason  string
	}{
		{"bare binary", nil, "head -40 foo_test.go", "head *", ""},
		{"git subcommand", nil, "git status --porcelain=v1", "git status *", ""},
		{"go subcommand", nil, "go build ./...", "go build *", ""},
		{"sed read flag", nil, "sed -n 825,833p schema.go", "sed -n *", ""},
		{"sed write flag", nil, "sed -i s/a/b/ f", "sed -i *", ""},
		{"npx nested tool", nil, "npx ng build --configuration development", "npx ng *", ""},
		{
			"first uncovered expression",
			[]string{"grep *"},
			`grep -n '"idp_id"' schema.go | head; sed -n 825,833p schema.go`,
			"head *", "",
		},
		{
			"skips covered, keeps order",
			[]string{"grep *", "head *"},
			"grep -n x f | head -30; echo ---; sed -n 1,5p g",
			"echo *", "",
		},
		{
			"cd chain",
			nil,
			`cd services/megatron-api && go build ./... 2>&1 | head -20`,
			"cd *", "",
		},
		{
			"stderr redirect is not a write",
			[]string{"cd *"},
			`cd svc && go vet ./... 2>&1 | head -20`,
			"go vet *", "",
		},
		{
			"env prefix stripped",
			nil,
			`GOFLAGS="-tags=integration" go test ./...`,
			"go test *", "",
		},
		{
			"file write suggests nothing",
			nil,
			"grep -rn ErrConflict . > /tmp/hits.txt",
			"", "writes a file — approve case by case",
		},
		{
			"append write suggests nothing",
			[]string{"grep *"},
			"grep -n x f | sort >> out.txt",
			"", "writes a file — approve case by case",
		},
		{
			"tee suggests nothing",
			[]string{"go build *"},
			"go build ./... | tee build.log",
			"", "writes a file — approve case by case",
		},
		{
			"fully covered",
			[]string{"grep *", "head *"},
			"grep -n x f | head -3",
			"", "already covered by the allow list",
		},
		{"empty command", nil, "", "", "already covered by the allow list"},
		{
			"command substitution body is its own expression",
			[]string{"git *"},
			"git log $(git rev-parse HEAD)",
			"", "already covered by the allow list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			glob, reason := NewBashRules(tt.allow, nil).Suggest(tt.command)
			if glob != tt.glob || reason != tt.reason {
				t.Errorf("Suggest(%q) = (%q, %q), want (%q, %q)",
					tt.command, glob, reason, tt.glob, tt.reason)
			}
		})
	}
}

// A suggestion must actually cover the expression it was proposed for:
// accepting it has to make progress, or the same prompt returns forever.
func TestSuggestConverges(t *testing.T) {
	const cmd = `cd svc && go build ./... 2>&1 | head -20; echo "BUILD EXIT: $?"`
	r := NewBashRules(nil, nil)

	var globs []string
	for range 10 {
		glob, reason := r.Suggest(cmd)
		if glob == "" {
			if reason != "already covered by the allow list" {
				t.Fatalf("stopped early: %q", reason)
			}
			break
		}
		globs = append(globs, glob)
		r.AllowPattern(glob)
	}

	if d, ok := r.Verdict(cmd); !ok || d != core.Allow {
		t.Fatalf("after %v the command still prompts (decision %v, ok %v)", globs, d, ok)
	}
}

// A discard redirect is not a file write: 2>/dev/null is too common to cost
// an approval every time.
func TestSuggestDiscardRedirect(t *testing.T) {
	glob, reason := NewBashRules(nil, nil).Suggest("ls -d migrations/ 2>/dev/null")
	if glob != "ls *" || reason != "" {
		t.Errorf("Suggest = (%q, %q), want (\"ls *\", \"\")", glob, reason)
	}
}
