package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/harness/session"
)

func TestParseMCPServer(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantName string
		wantCmd  string
		wantArgs []string
		wantErr  bool
	}{
		{"command only", "fs=mcp-server-fs", "fs", "mcp-server-fs", nil, false},
		{"command with args", "gh=npx -y @mcp/github", "gh", "npx", []string{"-y", "@mcp/github"}, false},
		{"missing equals", "just-a-name", "", "", nil, true},
		{"empty name", "=cmd", "", "", nil, true},
		{"empty command", "name=", "", "", nil, true},
		{"empty string", "", "", "", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMCPServer(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseMCPServer(%q) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMCPServer(%q) error: %v", tt.in, err)
			}
			if got.Name != tt.wantName || got.Command != tt.wantCmd {
				t.Errorf("got %q/%q, want %q/%q", got.Name, got.Command, tt.wantName, tt.wantCmd)
			}
			if len(got.Args) != len(tt.wantArgs) {
				t.Fatalf("args = %v, want %v", got.Args, tt.wantArgs)
			}
			for i := range got.Args {
				if got.Args[i] != tt.wantArgs[i] {
					t.Errorf("args[%d] = %q, want %q", i, got.Args[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestHarnessOptions(t *testing.T) {
	tests := []struct {
		name    string
		cfg     cliConfig
		wantN   int // expected option count
		wantErr bool
	}{
		{"empty config", cliConfig{}, 0, false},
		{"budgets only", cliConfig{MaxTurnTokens: 1000, MaxIterations: 5, MaxWallClock: time.Minute}, 1, false},
		{"toggles", cliConfig{NoPermissions: true, SubagentDepth: 2, SubagentDepthSet: true}, 2, false},
		{"dangerously skip permissions", cliConfig{SkipPermissions: true}, 1, false},
		{"approval timeout", cliConfig{ApprovalTimeout: 30 * time.Second, ApprovalTimeoutSet: true}, 1, false},
		{"conversation id", cliConfig{ConversationID: "abc"}, 1, false},
		{"one mcp server", cliConfig{MCPServers: []string{"fs=mcp-server-fs"}}, 1, false},
		{"bad mcp server", cliConfig{MCPServers: []string{"nope"}}, 0, true},
		{"bad role model", cliConfig{SubagentModel: "bogus/nope"}, 0, true},
		{"thinking set", cliConfig{Thinking: true, ThinkingSet: true}, 1, false},
		{"no-session and no-context-files", cliConfig{NoSession: true, NoContextFiles: true}, 2, false},
		{"resume adds conversation option", cliConfig{Resume: "abc123"}, 1, false},
		{"resume and continue are exclusive", cliConfig{Resume: "abc", ContinueLatest: true}, 0, true},
		{"continue requires persistence", cliConfig{ContinueLatest: true, NoSession: true}, 0, true},
		{"missing system file", cliConfig{SystemFile: "/definitely/not/here.md"}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := harnessOptions(&tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("harnessOptions error: %v", err)
			}
			if len(got) != tt.wantN {
				t.Errorf("got %d options, want %d", len(got), tt.wantN)
			}
		})
	}
}

func TestHarnessOptionsSystemFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SYSTEM.md")
	if err := os.WriteFile(path, []byte("be terse"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts, err := harnessOptions(&cliConfig{SystemFile: path})
	if err != nil {
		t.Fatalf("harnessOptions: %v", err)
	}
	if len(opts) != 1 {
		t.Errorf("options = %d, want 1 (system prompt)", len(opts))
	}
}

func TestHarnessOptionsContinueLatest(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	cfg := &cliConfig{ContinueLatest: true, sessionDir: dir, cwd: cwd}

	// no sessions yet → fresh start, no resume option
	opts, err := harnessOptions(cfg)
	if err != nil {
		t.Fatalf("harnessOptions: %v", err)
	}
	if len(opts) != 0 {
		t.Errorf("options = %d, want 0 (fresh start)", len(opts))
	}

	// seed a session → --continue picks it up (one extra option: the
	// conversation ID)
	s := session.NewStore(dir, cwd, "prior-conv", "m", time.Now)
	s.Append(session.Entry{Type: session.TypeUser, Text: "hi"})
	s.Close()

	opts, err = harnessOptions(cfg)
	if err != nil {
		t.Fatalf("harnessOptions: %v", err)
	}
	if len(opts) != 1 {
		t.Errorf("options = %d, want 1 (resume latest)", len(opts))
	}
}
