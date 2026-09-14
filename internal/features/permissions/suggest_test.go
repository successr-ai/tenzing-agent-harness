package permissions

import (
	"encoding/json"
	"slices"
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
		{"unknown binary", nil, "./run_tests.sh -v", "./run_tests.sh *", ""},
		{"git subcommand", nil, "git commit -m x", "git commit *", ""},
		{"go subcommand", nil, "go get ./...", "go get *", ""},
		{"sed write flag", nil, "sed -i s/a/b/ f", "sed -i *", ""},
		{"npx nested tool", nil, "npx ng build --configuration development", "npx ng *", ""},
		{"wrapper keeps inner binary", nil, "sudo rm -rf x", "sudo rm *", ""},
		{"xargs keeps inner binary", nil, "find . -name x | xargs rm", "xargs rm *", ""},
		{
			"first uncovered mutating segment",
			[]string{"./a.sh *"},
			`./a.sh | head; ./b.sh; rm x`,
			"./b.sh *", "",
		},
		{
			"read segments never need a rule",
			nil,
			"grep -n x f | head -30; echo ---; ./deploy.sh",
			"./deploy.sh *", "",
		},
		{
			"env prefix stripped",
			nil,
			`GOFLAGS="-tags=integration" go install ./...`,
			"go install *", "",
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
			"binary write is suggestable",
			nil,
			"cp a b",
			"cp *", "",
		},
		{
			"fully covered by globs",
			[]string{"./a.sh *"},
			"./a.sh | head -3",
			"", "already covered by the allow list",
		},
		{"read-only needs nothing", nil, "grep -n x f | head -3", "", "already covered by the allow list"},
		{"empty command", nil, "", "", "already covered by the allow list"},
		{
			"command substitution body is its own segment",
			nil,
			"echo $(./gen.sh)",
			"./gen.sh *", "",
		},
		{"nested shell", nil, `bash -c "./x.sh"`, "./x.sh *", ""},
		{
			"binary glob leaves the redirect write to a human",
			[]string{"cat *"},
			"cat > f <<'EOF'\nbody\nEOF",
			"", "writes a file — approve case by case",
		},
		{"parse error", nil, "ls 'x", "", "could not parse command — approve case by case"},
		{"non-literal command word", nil, "$CMD x", "", "command is not statically analysable — approve case by case"},
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
	const cmd = `cd svc && ./gen.sh 2>&1 | head -20; sudo rm -rf out; echo "EXIT: $?"`
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

	if d, _, ok := r.Verdict(cmd); !ok || d != core.Allow {
		t.Fatalf("after %v the command still prompts (decision %v, ok %v)", globs, d, ok)
	}
	if want := []string{"./gen.sh *", "sudo rm *"}; !slices.Equal(globs, want) {
		t.Errorf("converged on %q, want %q", globs, want)
	}
}

// A discard redirect is not a file write: 2>/dev/null is too common to cost
// an approval every time.
func TestSuggestDiscardRedirect(t *testing.T) {
	glob, reason := NewBashRules(nil, nil).Suggest("./probe.sh -d migrations/ 2>/dev/null")
	if glob != "./probe.sh *" || reason != "" {
		t.Errorf("Suggest = (%q, %q), want (\"./probe.sh *\", \"\")", glob, reason)
	}
}

// A suggestion has to cover the segment it was derived from, including an
// argument-less one — otherwise accepting it makes no progress and the same
// prompt returns forever.
func TestSuggestCoversArgumentlessExpression(t *testing.T) {
	r := NewBashRules(nil, nil)
	glob, _ := r.Suggest("./a.sh -n x | ./b.sh")
	r.AllowPattern(glob)
	if glob, _ = r.Suggest("./a.sh -n x | ./b.sh"); glob != "./b.sh *" {
		t.Fatalf("second suggestion = %q, want \"./b.sh *\"", glob)
	}
	r.AllowPattern(glob)
	if d, _, ok := r.Verdict("./a.sh -n x | ./b.sh"); !ok || d != core.Allow {
		t.Errorf("bare `./b.sh` still uncovered by %q (decision %v, ok %v)", "./b.sh *", d, ok)
	}
}

// Category-allowed segments do not need a glob either.
func TestSuggestSkipsCategoryAllowed(t *testing.T) {
	var r BashRules
	if err := json.Unmarshal([]byte(`{"categories":{"allow":["vcs"]}}`), &r); err != nil {
		t.Fatal(err)
	}
	if glob, _ := r.Suggest("git commit -m x && git push"); glob != "git push *" {
		t.Errorf("Suggest = %q, want \"git push *\"", glob)
	}
}
