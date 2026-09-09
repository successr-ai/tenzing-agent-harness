package main

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// defaultFiles are the starter tenzing.yaml, settings.json and
// SYSTEM_PROMPT.md `tenzing init` copies into the per-user config dir. They
// are embedded so the binary carries them with no install step; go:embed
// cannot reach outside its own package, which is why they live here rather
// than at the repo root.
//
//go:embed defaults
var defaultFiles embed.FS

// newInitCmd builds `tenzing init`: seed <UserConfigDir>/tenzing with the
// embedded defaults. An existing file is never overwritten — the config
// there may hold API keys and hand edits — so re-running is safe and only
// fills in what is missing.
func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Write starter config files into the per-user config directory",
		Long: "Copies the built-in tenzing.yaml, settings.json and SYSTEM_PROMPT.md into\n" +
			"the per-user config directory (~/Library/Application Support/tenzing on\n" +
			"macOS, $XDG_CONFIG_HOME/tenzing or ~/.config/tenzing elsewhere), the\n" +
			"directory --config and --settings fall back to.\n\n" +
			"Existing files are left untouched, so re-running only fills in gaps.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := userConfigDir()
			if dir == "" {
				return fmt.Errorf("cannot locate the user config directory")
			}
			return writeDefaults(dir, cmd.OutOrStdout())
		},
	}
}

// writeDefaults copies every embedded default into dir, reporting one line
// per file. Files already present are skipped, not overwritten.
func writeDefaults(dir string, out io.Writer) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	entries, err := fs.ReadDir(defaultFiles, "defaults")
	if err != nil {
		return fmt.Errorf("read embedded defaults: %w", err)
	}

	fmt.Fprintf(out, "%s\n", dir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		dst := filepath.Join(dir, name)
		if _, err := os.Stat(dst); err == nil {
			fmt.Fprintf(out, "  skipped  %s (already exists)\n", name)
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat %s: %w", dst, err)
		}

		body, err := fs.ReadFile(defaultFiles, "defaults/"+name)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", name, err)
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
		fmt.Fprintf(out, "  created  %s\n", name)
	}
	return nil
}
