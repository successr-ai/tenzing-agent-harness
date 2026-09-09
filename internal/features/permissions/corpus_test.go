package permissions

import (
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// corpus is every command that actually raised an approval prompt in one
// real session (TEST_COMMANDS.txt): a code audit across a Go service and an
// Angular client. want is the glob Suggest proposes for it against an empty
// allow list — the first expression, since nothing is covered yet.
var corpus = []struct {
	command string
	want    string
}{
	{`git status --porcelain=v1`, "git status *"},
	{`ls services/megatron-api/db/migrations-deferred/ && cat services/megatron-api/db/migrations-deferred/*stage2* 2>/dev/null; git diff HEAD -- services/megatron-api/internal/services/auth/repository/organization.go`, "ls *"},
	{`grep -n '"idp_id"' services/megatron-api/db/generated/migrate/schema.go | head; sed -n 825,833p services/megatron-api/db/generated/migrate/schema.go`, "grep *"},
	{`grep -n "func (s \*Suite) Test\|func Test" services/megatron-api/internal/services/user/user_test.go | head -30; echo ---; grep -n "TestUserRepository" services/megatron-api/internal/services/user/repository/user_repository_integration_test.go | head -3`, "grep *"},
	{`head -40 services/megatron-api/internal/services/user/user_test.go; echo ===; grep -n "Test\|func " services/megatron-api/internal/services/user/user_test.go | head -40`, "head *"},
	{`cd services/megatron-api && go build ./... 2>&1 | head -20 && go vet ./api/... ./internal/services/user/... 2>&1 | head -20; echo "BUILD+VET EXIT: $?"`, "cd *"},
	{`cd clients/optimus-client && npx ng build --configuration development 2>&1 | tail -8`, "cd *"},
	{`grep -n "ErrConflict" services/megatron-api/internal/services/user/*.go services/megatron-api/internal/services/user/repository/*.go services/megatron-api/errors/*.go 2>/dev/null | head; echo ===; sed -n 10,40p services/megatron-api/internal/services/user/user.go`, "grep *"},
	{`grep -n "ErrConflict\|IsConstraintError\|duplicate" services/megatron-api/internal/services/user/repository/user_repository.go; echo ===; sed -n 1,20p services/megatron-api/errors/errors.go; sed -n 25,45p services/megatron-api/errors/errors.go`, "grep *"},
	{`grep -rn "ErrConflict" services/megatron-api/internal/services/user/ services/megatron-api/internal/services/billing/ services/megatron-api/api/resolver/user.go 2>/dev/null | head; echo ===; grep -rn "ErrConflict\|ConstraintError" services/megatron-api/internal/services/billing/service.go 2>/dev/null | head -5`, "grep *"},
	{`grep -n "ErrConflict" services/megatron-api/internal/services/auth/repository/organization.go | head -3; echo ===; grep -rn "Save\b" services/megatron-api/internal/services/user/repository/user_repository.go | head; echo ===; git log --oneline -3 -- services/megatron-api/internal/services/user/repository/user_repository.go`, "grep *"},
	{`grep -rn "duplicate email\|ErrConflict\|users_email" services/megatron-api/internal/services/user/ | grep -v _test | grep -v mock; echo "EXIT: $?"; echo ===; git log --all --oneline --grep="duplicate" -i | head -5`, "grep *"},
	{`grep -n "TestAdminCreateUser" -A 40 services/megatron-api/api/resolver/admin_test.go | sed -n 1,20p; echo ===; grep -rn "users_email_key\|Duplicate\|409" tests/integration/megatron-api/admin_create_user_test.go | head`, "grep *"},
	{`sed -n 40,75p tests/integration/megatron-api/admin_create_user_test.go; echo ===; sed -n 185,206p services/megatron-api/api/resolver/admin_test.go`, "sed -n *"},
	{`grep -rn "HTTPStatus" services/megatron-api/api/router/handlers/*.go | head -5; echo ===; sed -n 1,60p services/megatron-api/api/router/handlers/route.go 2>/dev/null || ls services/megatron-api/api/router/handlers/`, "grep *"},
	{`sed -n 330,350p services/megatron-api/db/generated/user_create.go; echo ===; grep -rn "ConstraintError\b" services/megatron-api/db/generated/ent.go | head -3; grep -rn "type ConstraintError" -A 10 services/megatron-api/db/generated/ent.go | head -15`, "sed -n *"},
	{`grep -rn "DoJSON\|func.*Request\|status" tests/integration/megatron-api/admin_create_user_test.go | head -10; echo ===; grep -rn "requester :=" tests/integration/megatron-api/admin_create_user_test.go | head -3`, "grep *"},
	{`grep -rn "func LoginTestUser\|func (c \*Client) DoJSON\|type Client" tests/integration/megatron-api/platform/*.go | head; echo ===; grep -n "DoJSON" -A 30 tests/integration/megatron-api/platform/client.go 2>/dev/null | grep -n "StatusForbidden\|StatusCode\|return\|err " | head -10`, "grep *"},
	{`git diff HEAD --stat -- tests/integration/megatron-api/login_user_test.go tests/integration/megatron-api/second_login_test.go tests/integration/megatron-api/create_business_test.go | cat`, "git diff *"},
	{`grep -rn "23505\|unique_violation\|SQLState\|users_email" services/megatron-api --include=*.go | grep -v generated | head; echo ===; grep -rn "errorMapping\|MapError\|translate" services/megatron-api/internal/clients/db_platform/*.go | head`, "grep *"},
	{`ls services/megatron-api/db/ | head; grep -rn "ErrConflict" services/megatron-api/db/*.go 2>/dev/null | head -5`, "ls *"},
	{`grep -n "Conflict\|Constraint\|Unique" services/megatron-api/db/client.go | head; echo ===; grep -rn "errors.Is" services/megatron-api/api/resolver/user.go services/megatron-api/api/resolver/admin.go | head`, "grep *"},
	{`grep -n "Hook\|Interceptor\|Conflict" services/megatron-api/db/client.go services/megatron-api/db/schema/user.go | head; echo ===; docker ps --format '{{.Names}}' 2>/dev/null | head`, "grep *"},
}

// Every prompted command gets a glob proposed for its first expression. None
// of them is a write, so none may come back with a reason instead.
func TestCorpusSuggestsFirstExpression(t *testing.T) {
	for _, tt := range corpus {
		glob, reason := NewBashRules(nil, nil).Suggest(tt.command)
		if glob != tt.want || reason != "" {
			t.Errorf("Suggest(%.60q…) = (%q, %q), want (%q, \"\")", tt.command, glob, reason, tt.want)
		}
	}
}

// Accepting every suggestion, prompt after prompt, must eventually silence
// the whole corpus — that is the point of suggesting one rule at a time. The
// rule set it converges on is asserted so a change in glob shape shows up
// here as a diff rather than as a surprise in someone's settings file.
func TestCorpusConverges(t *testing.T) {
	r := NewBashRules(nil, nil)
	for _, tt := range corpus {
		for range 20 {
			glob, reason := r.Suggest(tt.command)
			if glob == "" {
				if reason != "already covered by the allow list" {
					t.Fatalf("%.60q…: stopped with %q", tt.command, reason)
				}
				break
			}
			r.AllowPattern(glob)
		}
		if d, ok := r.Verdict(tt.command); !ok || d != core.Allow {
			t.Fatalf("%.60q… still prompts (decision %v, ok %v)", tt.command, d, ok)
		}
	}

	want := []string{
		"git status *", "ls *", "cat *", "git diff *", "grep *", "head *",
		"sed -n *", "echo *", "cd *", "go build *", "go vet *", "npx ng *",
		"tail *", "git log *", "docker ps *",
	}
	allow, _ := r.Lists()
	if len(allow) != len(want) {
		t.Fatalf("converged on %d rules %q, want %d %q", len(allow), allow, len(want), want)
	}
	for i := range want {
		if allow[i] != want[i] {
			t.Errorf("rule %d = %q, want %q (full set %q)", i, allow[i], want[i], allow)
		}
	}
}
