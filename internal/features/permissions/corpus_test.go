package permissions

import (
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// corpus is every command that actually raised an approval prompt in one
// real session (TEST_COMMANDS.txt): a code audit across a Go service and an
// Angular client. want is the glob globFor builds for its first segment.
var corpus = []struct {
	command string
	want    string
	prompts bool // the one command that still needs a human: npx runs arbitrary packages
}{
	{`git status --porcelain=v1`, "git status *", false},
	{`ls services/megatron-api/db/migrations-deferred/ && cat services/megatron-api/db/migrations-deferred/*stage2* 2>/dev/null; git diff HEAD -- services/megatron-api/internal/services/auth/repository/organization.go`, "ls *", false},
	{`grep -n '"idp_id"' services/megatron-api/db/generated/migrate/schema.go | head; sed -n 825,833p services/megatron-api/db/generated/migrate/schema.go`, "grep *", false},
	{`grep -n "func (s \*Suite) Test\|func Test" services/megatron-api/internal/services/user/user_test.go | head -30; echo ---; grep -n "TestUserRepository" services/megatron-api/internal/services/user/repository/user_repository_integration_test.go | head -3`, "grep *", false},
	{`head -40 services/megatron-api/internal/services/user/user_test.go; echo ===; grep -n "Test\|func " services/megatron-api/internal/services/user/user_test.go | head -40`, "head *", false},
	{`cd services/megatron-api && go build ./... 2>&1 | head -20 && go vet ./api/... ./internal/services/user/... 2>&1 | head -20; echo "BUILD+VET EXIT: $?"`, "cd *", false},
	{`cd clients/optimus-client && npx ng build --configuration development 2>&1 | tail -8`, "npx ng *", true},
	{`grep -n "ErrConflict" services/megatron-api/internal/services/user/*.go services/megatron-api/internal/services/user/repository/*.go services/megatron-api/errors/*.go 2>/dev/null | head; echo ===; sed -n 10,40p services/megatron-api/internal/services/user/user.go`, "grep *", false},
	{`grep -n "ErrConflict\|IsConstraintError\|duplicate" services/megatron-api/internal/services/user/repository/user_repository.go; echo ===; sed -n 1,20p services/megatron-api/errors/errors.go; sed -n 25,45p services/megatron-api/errors/errors.go`, "grep *", false},
	{`grep -rn "ErrConflict" services/megatron-api/internal/services/user/ services/megatron-api/internal/services/billing/ services/megatron-api/api/resolver/user.go 2>/dev/null | head; echo ===; grep -rn "ErrConflict\|ConstraintError" services/megatron-api/internal/services/billing/service.go 2>/dev/null | head -5`, "grep *", false},
	{`grep -n "ErrConflict" services/megatron-api/internal/services/auth/repository/organization.go | head -3; echo ===; grep -rn "Save\b" services/megatron-api/internal/services/user/repository/user_repository.go | head; echo ===; git log --oneline -3 -- services/megatron-api/internal/services/user/repository/user_repository.go`, "grep *", false},
	{`grep -rn "duplicate email\|ErrConflict\|users_email" services/megatron-api/internal/services/user/ | grep -v _test | grep -v mock; echo "EXIT: $?"; echo ===; git log --all --oneline --grep="duplicate" -i | head -5`, "grep *", false},
	{`grep -n "TestAdminCreateUser" -A 40 services/megatron-api/api/resolver/admin_test.go | sed -n 1,20p; echo ===; grep -rn "users_email_key\|Duplicate\|409" tests/integration/megatron-api/admin_create_user_test.go | head`, "grep *", false},
	{`sed -n 40,75p tests/integration/megatron-api/admin_create_user_test.go; echo ===; sed -n 185,206p services/megatron-api/api/resolver/admin_test.go`, "sed -n *", false},
	{`grep -rn "HTTPStatus" services/megatron-api/api/router/handlers/*.go | head -5; echo ===; sed -n 1,60p services/megatron-api/api/router/handlers/route.go 2>/dev/null || ls services/megatron-api/api/router/handlers/`, "grep *", false},
	{`sed -n 330,350p services/megatron-api/db/generated/user_create.go; echo ===; grep -rn "ConstraintError\b" services/megatron-api/db/generated/ent.go | head -3; grep -rn "type ConstraintError" -A 10 services/megatron-api/db/generated/ent.go | head -15`, "sed -n *", false},
	{`grep -rn "DoJSON\|func.*Request\|status" tests/integration/megatron-api/admin_create_user_test.go | head -10; echo ===; grep -rn "requester :=" tests/integration/megatron-api/admin_create_user_test.go | head -3`, "grep *", false},
	{`grep -rn "func LoginTestUser\|func (c \*Client) DoJSON\|type Client" tests/integration/megatron-api/platform/*.go | head; echo ===; grep -n "DoJSON" -A 30 tests/integration/megatron-api/platform/client.go 2>/dev/null | grep -n "StatusForbidden\|StatusCode\|return\|err " | head -10`, "grep *", false},
	{`git diff HEAD --stat -- tests/integration/megatron-api/login_user_test.go tests/integration/megatron-api/second_login_test.go tests/integration/megatron-api/create_business_test.go | cat`, "git diff *", false},
	{`grep -rn "23505\|unique_violation\|SQLState\|users_email" services/megatron-api --include=*.go | grep -v generated | head; echo ===; grep -rn "errorMapping\|MapError\|translate" services/megatron-api/internal/clients/db_platform/*.go | head`, "grep *", false},
	{`ls services/megatron-api/db/ | head; grep -rn "ErrConflict" services/megatron-api/db/*.go 2>/dev/null | head -5`, "ls *", false},
	{`grep -n "Conflict\|Constraint\|Unique" services/megatron-api/db/client.go | head; echo ===; grep -rn "errors.Is" services/megatron-api/api/resolver/user.go services/megatron-api/api/resolver/admin.go | head`, "grep *", false},
	{`grep -n "Hook\|Interceptor\|Conflict" services/megatron-api/db/client.go services/megatron-api/db/schema/user.go | head; echo ===; docker ps --format '{{.Names}}' 2>/dev/null | head`, "grep *", false},
}

// All but one of these commands are read-only, so with an empty rule set
// they no longer prompt. This is the headline behaviour of the classifier:
// the session that produced the corpus would have needed a single approval
// (npx, which runs an arbitrary package) instead of 23 — and accepting that
// one suggestion silences it too.
func TestCorpusAutoAllowed(t *testing.T) {
	r := NewBashRules(nil, nil)
	for _, tt := range corpus {
		d, reason, ok := r.Verdict(tt.command)
		glob, sreason := r.Suggest(tt.command)
		if !tt.prompts {
			if !ok || d != core.Allow {
				t.Errorf("%.60q… still prompts (decision %v, ok %v): %s", tt.command, d, ok, reason)
			}
			if glob != "" {
				t.Errorf("%.60q… suggests %q (%q), want nothing", tt.command, glob, sreason)
			}
			continue
		}
		if d != core.AskUser {
			t.Errorf("%.60q… should prompt, got %v", tt.command, d)
		}
		if glob != tt.want {
			t.Errorf("%.60q… suggests %q, want %q", tt.command, glob, tt.want)
		}
		r.AllowPatternSession(glob)
		if d, _, ok := r.Verdict(tt.command); !ok || d != core.Allow {
			t.Errorf("%.60q… still prompts after accepting %q", tt.command, glob)
		}
	}
}

// The glob shape is pinned so a change shows up here as a diff rather than
// as a surprise in someone's settings file: for read-only commands, the
// glob globFor would build for the first segment; for the one that
// prompts, the segment Suggest actually picks.
func TestCorpusGlobShape(t *testing.T) {
	r := NewBashRules(nil, nil)
	for _, tt := range corpus {
		if tt.prompts {
			continue
		}
		a := r.Analyze(tt.command)
		if a.Err != nil || len(a.Segments) == 0 {
			t.Fatalf("%.60q…: %v", tt.command, a.Err)
		}
		if got := globFor(a.Segments[0]); got != tt.want {
			t.Errorf("globFor(%.60q…) = %q, want %q", tt.command, got, tt.want)
		}
	}
}
