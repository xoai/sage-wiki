package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/storage"
)

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Show pending source changes against manifest",
	RunE:  runDiff,
}

func runDiff(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	cfg, err := config.Load(resolveConfigPath(dir))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	diff, err := compiler.Diff(dir, cfg, mf)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	type driftEntry struct {
		Path  string `json:"path"`
		Class string `json:"class"`
	}
	type diffData struct {
		Added    []string     `json:"added"`
		Modified []string     `json:"modified"`
		Removed  []string     `json:"removed"`
		Drifted  []driftEntry `json:"drifted,omitempty"`
		Pending  int          `json:"pending"`
		Total    int          `json:"total"`
	}

	added := make([]string, len(diff.Added))
	for i, s := range diff.Added {
		added[i] = s.Path
	}
	modified := make([]string, len(diff.Modified))
	for i, s := range diff.Modified {
		modified[i] = s.Path
	}

	data := diffData{
		Added:    added,
		Modified: modified,
		Removed:  diff.Removed,
		Pending:  len(diff.Added) + len(diff.Modified),
		Total:    mf.SourceCount(),
	}

	// SPEC-04: key-drift annotation — docs whose content is unchanged but
	// whose compile inputs drifted (pipeline/templates/models/config/embed).
	// The classification hashes effective templates — load prompts/ overrides
	// exactly as compile does, or every override workspace gets a perpetual
	// false 'templates' drift (Gate-1 review F-1).
	if err := prompts.LoadFromDir(filepath.Join(dir, "prompts")); err != nil {
		log.Warn("diff: prompts/ overrides not loaded", "error", err)
	}
	// Read-only open, and only when the DB exists (NEW-1: a read command
	// must not create/migrate state as a side effect).
	dbPath := filepath.Join(dir, ".sage", "wiki.db")
	if _, err := os.Stat(dbPath); err == nil {
		if sdb, err := storage.OpenWithOptions(dbPath, storage.OpenOptions{ReadOnly: true}); err == nil {
			items := compiler.NewCompileItemStore(sdb, config.NowUTC)
			if cls, err := compiler.ClassifySkipsForDiff(cfg, items, mf, diff); err == nil {
				for path, class := range cls {
					data.Drifted = append(data.Drifted, driftEntry{Path: path, Class: class})
				}
				sort.Slice(data.Drifted, func(i, j int) bool { return data.Drifted[i].Path < data.Drifted[j].Path })
			} else {
				log.Warn("diff: drift annotation unavailable", "error", err)
			}
			sdb.Close()
		} else {
			log.Warn("diff: open db for drift annotation failed", "error", err)
		}
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, data, ""))
		return nil
	}

	if data.Pending == 0 && len(diff.Removed) == 0 && len(data.Drifted) == 0 {
		fmt.Println("Nothing to compile — wiki is up to date.")
		return nil
	}
	fmt.Printf("Sources: %d total, %d pending\n", data.Total, data.Pending)
	for _, p := range added {
		fmt.Printf("  + %s (new)\n", p)
	}
	for _, p := range modified {
		fmt.Printf("  ~ %s (modified)\n", p)
	}
	for _, p := range diff.Removed {
		fmt.Printf("  - %s (removed)\n", p)
	}
	for _, d := range data.Drifted {
		fmt.Printf("  ≈ %s (drift: %s)\n", d.Path, d.Class)
	}
	return nil
}
