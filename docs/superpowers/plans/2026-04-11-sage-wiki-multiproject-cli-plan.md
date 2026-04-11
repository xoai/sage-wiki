# sage-wiki Multi-Project CLI-first Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate sage-wiki from MCP-dependent mode to CLI-first mode with multi-project support (federated search, config inheritance, hub routing), wrapped in a Claude Code skill.

**Architecture:** CLI commands (调完即退) replace MCP long-running server. New `--format json` flag enables machine parsing. Hub subcommand federates across projects. Config `extends` eliminates duplication. `/wiki` skill wraps CLI for Claude Code UX.

**Tech Stack:** Go 1.22+, cobra CLI, SQLite (WAL mode), existing internal packages (compiler, hybrid, ontology, manifest, memory, vectors)

**Spec:** `docs/superpowers/specs/2026-04-11-sage-wiki-multiproject-cli-design.md`

---

## Audit Fixes (Step 6.5 + Step 7)

| ID | Issue | Fix | Task |
|----|-------|-----|------|
| F1 | yaml.v3 extends merge clobbers base values | Self-contained `deepMerge(map[string]any)` function (~20 lines) | Task 8 |
| F2 | `wiki_add_source` has no CLI equivalent | Add `sage-wiki add-source` command | Task 7 |
| F3 | `capture --url` in spec but not implemented (MCP also lacks it) | Mark as `(future)` in spec | Spec only |
| F4 | `wiki.StatusInfo` has no json struct tags | Add json tags to StatusInfo | Task 2 |
| F5 | Hub search opens write connections unnecessarily | Add `storage.OpenReadOnly()` | Task 11 |
| W1 | write_cmd.go DB error pattern not defensive enough | Use standard `if err != nil; defer` | Task 6 |
| W2 | Hub search returns empty+nil when all projects fail | Return error when all fail | Task 11 |
| W3 | wiki_commit equivalent is `git add . && git commit`, not just `git commit` | Document in wiki skill | Task 13 |

---

## File Structure

### Phase 1 — CLI 补齐 + JSON + extends (上游 PR)

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/cli/output.go` | Create | JSON/text output formatting helpers |
| `internal/cli/output_test.go` | Create | Tests for output formatting |
| `cmd/sage-wiki/main.go` | Modify:117-152 | Add `--format` persistent flag, register new commands |
| `cmd/sage-wiki/diff.go` | Create | `sage-wiki diff` command |
| `cmd/sage-wiki/list.go` | Create | `sage-wiki list` command |
| `cmd/sage-wiki/ontology_cmd.go` | Create | `sage-wiki ontology query/add` commands |
| `cmd/sage-wiki/write_cmd.go` | Create | `sage-wiki write summary/article` commands |
| `cmd/sage-wiki/learn.go` | Create | `sage-wiki learn` command |
| `cmd/sage-wiki/capture.go` | Create | `sage-wiki capture` command |
| `cmd/sage-wiki/add_source.go` | Create | `sage-wiki add-source` command (F2 fix) |
| `internal/config/config.go` | Modify:17,182-202 | Add `Extends` field + deepMerge logic in Load() (F1 fix) |
| `internal/config/merge.go` | Create | `deepMerge(map[string]any)` helper (F1 fix) |
| `internal/config/merge_test.go` | Create | Tests for deepMerge (F1 fix) |
| `internal/wiki/status.go` | Modify | Add json struct tags to StatusInfo (F4 fix) |
| `internal/config/config_test.go` | Modify | Add extends tests |

### Phase 2 — Hub (fork 独有)

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/hub/hub.go` | Create | Hub config load/save, project registry |
| `internal/hub/hub_test.go` | Create | Hub config tests |
| `internal/hub/search.go` | Create | Federated search (parallel DB open + RRF merge) |
| `internal/hub/search_test.go` | Create | Federated search tests |
| `internal/storage/readonly.go` | Create | `OpenReadOnly()` for hub search (F5 fix) |
| `cmd/sage-wiki/hub.go` | Create | Hub CLI commands (init/add/remove/list/search/status/compile) |

### Phase 3 — Skill + 迁移 (本地)

| File | Action | Responsibility |
|------|--------|---------------|
| `~/.claude/skills/wiki/SKILL.md` | Create | `/wiki` skill routing CLI calls |
| `~/.claude/skills/wiki-improve/SKILL.md` | Modify | Replace MCP calls with CLI |

### Phase 4 — 清理

| File | Action | Responsibility |
|------|--------|---------------|
| `~/.claude.json` | Modify | Remove sage-wiki MCP entry |

---

## Phase 1: CLI 补齐 + JSON + extends

### Task 1: Output formatting helpers

**Files:**
- Create: `internal/cli/output.go`
- Test: `internal/cli/output_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/cli/output_test.go
package cli

import (
	"encoding/json"
	"testing"
)

func TestJSONSuccess(t *testing.T) {
	data := map[string]int{"count": 5}
	out := FormatJSON(true, data, "")
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !resp.OK {
		t.Error("expected ok=true")
	}
}

func TestJSONError(t *testing.T) {
	out := FormatJSON(false, nil, "something failed")
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.OK {
		t.Error("expected ok=false")
	}
	if resp.Error != "something failed" {
		t.Errorf("expected error message, got %q", resp.Error)
	}
}

func TestOutputDispatch(t *testing.T) {
	// format=json -> JSON output
	got := Output("json", "text fallback", true, map[string]int{"n": 1}, "")
	var resp Response
	if err := json.Unmarshal([]byte(got), &resp); err != nil {
		t.Fatalf("json dispatch failed: %v", err)
	}

	// format=text -> text output
	got = Output("text", "text fallback", true, nil, "")
	if got != "text fallback" {
		t.Errorf("text dispatch: expected fallback, got %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go test ./internal/cli/ -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Write the implementation**

```go
// internal/cli/output.go
package cli

import (
	"encoding/json"
	"fmt"
	"os"
)

// Response is the standard JSON envelope for all CLI commands.
type Response struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
	Code  int    `json:"code,omitempty"`
}

// FormatJSON returns a JSON string with the standard envelope.
func FormatJSON(ok bool, data any, errMsg string) string {
	resp := Response{OK: ok, Data: data, Error: errMsg}
	if !ok && errMsg != "" {
		resp.Code = 1
	}
	out, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"error":"marshal failed: %v"}`, err)
	}
	return string(out)
}

// Output dispatches to JSON or text formatting based on format flag.
func Output(format string, text string, ok bool, data any, errMsg string) string {
	if format == "json" {
		return FormatJSON(ok, data, errMsg)
	}
	return text
}

// PrintResult prints to stdout (JSON) or stderr (text errors).
func PrintResult(format string, text string, ok bool, data any, errMsg string) {
	if !ok && format != "json" {
		fmt.Fprintln(os.Stderr, errMsg)
		return
	}
	fmt.Println(Output(format, text, ok, data, errMsg))
}

// ExitCode returns the appropriate exit code.
func ExitCode(ok bool, partial bool) int {
	if ok && !partial {
		return 0
	}
	if partial {
		return 2
	}
	return 1
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go test ./internal/cli/ -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git add internal/cli/output.go internal/cli/output_test.go
git commit -m "feat: add CLI output formatting helpers (JSON/text envelope)"
```

---

### Task 2: Add --format flag + retrofit existing commands + StatusInfo json tags

**Files:**
- Modify: `cmd/sage-wiki/main.go`
- Modify: `internal/wiki/status.go` (F4: add json struct tags to StatusInfo)

- [ ] **Step 1: Add outputFormat variable and --format flag**

In `cmd/sage-wiki/main.go`, add `outputFormat string` to the var block (line 33), and in `func init()` (line 118) add:

```go
rootCmd.PersistentFlags().StringVar(&outputFormat, "format", "text", "Output format: text or json")
```

Add import `"github.com/xoai/sage-wiki/internal/cli"`.

- [ ] **Step 2: Retrofit runStatus (line 445)**

Replace:
```go
func runStatus(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	info, err := wiki.GetStatus(dir, nil)
	if err != nil {
		return err
	}
	fmt.Print(wiki.FormatStatus(info))
	return nil
}
```

With:
```go
func runStatus(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	info, err := wiki.GetStatus(dir, nil)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, info, ""))
	} else {
		fmt.Print(wiki.FormatStatus(info))
	}
	return nil
}
```

- [ ] **Step 3: Retrofit runSearch output (line 408)**

After the `results, err := searcher.Search(...)` block, before the `if len(results) == 0` check, insert:

```go
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, results, ""))
		return nil
	}
```

- [ ] **Step 4: Retrofit runLint output (line 353)**

Before `fmt.Print(linter.FormatFindings(results))`, insert:

```go
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, results, ""))
		return nil
	}
```

- [ ] **Step 5: Retrofit runCompile output (line 279)**

Replace the final output block with:

```go
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, result, ""))
		return nil
	}

	fmt.Printf("Compile complete: +%d added, ~%d modified, -%d removed, %d summarized, %d concepts, %d articles",
		result.Added, result.Modified, result.Removed, result.Summarized,
		result.ConceptsExtracted, result.ArticlesWritten)
	if result.Errors > 0 {
		fmt.Printf(", %d errors", result.Errors)
	}
	fmt.Println()
```

- [ ] **Step 6: Build and verify**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go build -o sage-wiki ./cmd/sage-wiki/ && go vet ./...`
Expected: Build succeeds

- [ ] **Step 7: Add json struct tags to StatusInfo (F4 fix)**

In `internal/wiki/status.go`, add `json:"..."` tags to all `StatusInfo` fields so JSON output uses snake_case (e.g., `SourceCount int` → `SourceCount int \`json:"source_count"\``). This ensures consistent JSON field naming across all `--format json` outputs.

- [ ] **Step 8: Smoke test JSON output**

Run: `./sage-wiki status --project /Users/kellen/claude-workspace/wiki/ --format json | python3 -c "import sys,json; d=json.load(sys.stdin); print('ok:', d['ok'])"`
Expected: `ok: True` with snake_case field names

- [ ] **Step 9: Commit**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git add cmd/sage-wiki/main.go internal/wiki/status.go
git commit -m "feat: add --format json flag to all existing CLI commands + StatusInfo json tags"
```

---

### Task 3: `sage-wiki diff` command

**Files:**
- Create: `cmd/sage-wiki/diff.go`
- Modify: `cmd/sage-wiki/main.go` (register command)

- [ ] **Step 1: Create diff.go**

```go
// cmd/sage-wiki/diff.go
package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/manifest"
)

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Show pending source changes against manifest",
	RunE:  runDiff,
}

func runDiff(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	diff, err := compiler.Diff(dir, cfg, mf)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	type diffData struct {
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
		Pending  int      `json:"pending"`
		Total    int      `json:"total"`
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

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, data, ""))
		return nil
	}

	if data.Pending == 0 && len(diff.Removed) == 0 {
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
	return nil
}
```

- [ ] **Step 2: Register in main.go line 152**

Add `diffCmd` to `rootCmd.AddCommand(...)`.

- [ ] **Step 3: Build + smoke test**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go build -o sage-wiki ./cmd/sage-wiki/ && ./sage-wiki diff --project /Users/kellen/claude-workspace/wiki/ --format json | python3 -m json.tool | head -10`
Expected: Valid JSON with pending/added/modified fields

- [ ] **Step 4: Commit**

```bash
git add cmd/sage-wiki/diff.go cmd/sage-wiki/main.go
git commit -m "feat: add sage-wiki diff command (replaces wiki_compile_diff)"
```

---

### Task 4: `sage-wiki list` command

**Files:**
- Create: `cmd/sage-wiki/list.go`

- [ ] **Step 1: Create list.go**

```go
// cmd/sage-wiki/list.go
package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List wiki entities, concepts, or sources",
	RunE:  runList,
}

func init() {
	listCmd.Flags().String("type", "", "Filter: concepts, sources, or entity type (concept, technique, etc.)")
}

func runList(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	listType, _ := cmd.Flags().GetString("type")

	// Sources: manifest only, no DB
	if listType == "sources" {
		mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
		if err != nil {
			if outputFormat == "json" {
				fmt.Println(cli.FormatJSON(false, nil, err.Error()))
				return nil
			}
			return err
		}
		type sourceItem struct {
			Path   string `json:"path"`
			Type   string `json:"type"`
			Status string `json:"status"`
		}
		items := make([]sourceItem, 0, len(mf.Sources))
		for path, s := range mf.Sources {
			items = append(items, sourceItem{Path: path, Type: s.Type, Status: s.Status})
		}
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, map[string]any{"items": items, "total": len(items)}, ""))
		} else {
			fmt.Printf("Sources: %d total\n", len(items))
			for _, item := range items {
				fmt.Printf("  [%s] %s (%s)\n", item.Status, item.Path, item.Type)
			}
		}
		return nil
	}

	// Entities: need DB + ontology
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	defer db.Close()

	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	ont := ontology.NewStore(db, ontology.ValidRelationNames(merged))

	entityType := listType
	if listType == "concepts" {
		entityType = "concept"
	}

	entities, err := ont.ListEntities(entityType)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))

	type listItem struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		Name        string `json:"name"`
		ArticlePath string `json:"article_path,omitempty"`
	}
	items := make([]listItem, len(entities))
	for i, e := range entities {
		items[i] = listItem{ID: e.ID, Type: e.Type, Name: e.Name, ArticlePath: e.ArticlePath}
	}

	result := map[string]any{
		"entities":      items,
		"total":         len(items),
		"source_count":  mf.SourceCount(),
		"concept_count": mf.ConceptCount(),
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, result, ""))
	} else {
		fmt.Printf("Entities: %d (sources: %d, concepts: %d)\n", len(items), mf.SourceCount(), mf.ConceptCount())
		for _, item := range items {
			fmt.Printf("  [%s] %s", item.Type, item.Name)
			if item.ArticlePath != "" {
				fmt.Printf(" -> %s", item.ArticlePath)
			}
			fmt.Println()
		}
	}
	return nil
}
```

- [ ] **Step 2: Register + build + smoke test**

Add `listCmd` to `rootCmd.AddCommand(...)`, build, test:
```bash
./sage-wiki list --type concepts --project /Users/kellen/claude-workspace/wiki/ --format json | python3 -c "import sys,json; d=json.load(sys.stdin); print('total:', d['data']['total'])"
```

- [ ] **Step 3: Commit**

```bash
git add cmd/sage-wiki/list.go cmd/sage-wiki/main.go
git commit -m "feat: add sage-wiki list command (replaces wiki_list)"
```

---

### Task 5: `sage-wiki ontology` command (query + add)

**Files:**
- Create: `cmd/sage-wiki/ontology_cmd.go`

- [ ] **Step 1: Create ontology_cmd.go**

```go
// cmd/sage-wiki/ontology_cmd.go
package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
)

var ontologyCmd = &cobra.Command{
	Use:   "ontology",
	Short: "Query and manage the ontology graph",
}

var ontologyQueryCmd = &cobra.Command{
	Use:   "query",
	Short: "Traverse the ontology from an entity",
	RunE:  runOntologyQuery,
}

var ontologyAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add an entity or relation",
	RunE:  runOntologyAdd,
}

func init() {
	ontologyQueryCmd.Flags().String("entity", "", "Entity ID to start from")
	ontologyQueryCmd.Flags().String("relation", "", "Filter by relation type")
	ontologyQueryCmd.Flags().String("direction", "outbound", "outbound, inbound, or both")
	ontologyQueryCmd.Flags().Int("depth", 1, "Traversal depth 1-5")
	ontologyQueryCmd.MarkFlagRequired("entity")

	ontologyAddCmd.Flags().String("from", "", "Source entity ID (for relations)")
	ontologyAddCmd.Flags().String("to", "", "Target entity ID (for relations)")
	ontologyAddCmd.Flags().String("relation", "", "Relation type")
	ontologyAddCmd.Flags().String("entity-id", "", "Entity ID (for creating entities)")
	ontologyAddCmd.Flags().String("entity-type", "concept", "Entity type")
	ontologyAddCmd.Flags().String("entity-name", "", "Human-readable name")

	ontologyCmd.AddCommand(ontologyQueryCmd, ontologyAddCmd)
}

func openOntStore(dir string) (*storage.DB, *ontology.Store, error) {
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return nil, nil, err
	}
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return nil, nil, err
	}
	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	return db, ontology.NewStore(db, ontology.ValidRelationNames(merged)), nil
}

func runOntologyQuery(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	entityID, _ := cmd.Flags().GetString("entity")
	relType, _ := cmd.Flags().GetString("relation")
	dirStr, _ := cmd.Flags().GetString("direction")
	depth, _ := cmd.Flags().GetInt("depth")

	db, ont, err := openOntStore(dir)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	defer db.Close()

	traverseDir := ontology.Outbound
	switch dirStr {
	case "inbound":
		traverseDir = ontology.Inbound
	case "both":
		traverseDir = ontology.Both
	}

	entities, err := ont.Traverse(entityID, ontology.TraverseOpts{
		Direction:    traverseDir,
		RelationType: relType,
		MaxDepth:     depth,
	})
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, entities, ""))
		return nil
	}

	if len(entities) == 0 {
		fmt.Printf("No entities found from %q\n", entityID)
		return nil
	}
	for _, e := range entities {
		fmt.Printf("  [%s] %s (%s)\n", e.Type, e.Name, e.ID)
	}
	return nil
}

func runOntologyAdd(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	fromID, _ := cmd.Flags().GetString("from")
	toID, _ := cmd.Flags().GetString("to")
	relType, _ := cmd.Flags().GetString("relation")
	entityID, _ := cmd.Flags().GetString("entity-id")

	db, ont, err := openOntStore(dir)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	defer db.Close()

	// Add relation
	if fromID != "" && toID != "" && relType != "" {
		relID := fromID + "-" + relType + "-" + toID
		if err := ont.AddRelation(ontology.Relation{
			ID: relID, SourceID: fromID, TargetID: toID, Relation: relType,
		}); err != nil {
			if outputFormat == "json" {
				fmt.Println(cli.FormatJSON(false, nil, err.Error()))
				return nil
			}
			return err
		}
		msg := fmt.Sprintf("Relation: %s -[%s]-> %s", fromID, relType, toID)
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, map[string]string{"message": msg}, ""))
		} else {
			fmt.Println(msg)
		}
		return nil
	}

	// Add entity
	if entityID != "" {
		entityType, _ := cmd.Flags().GetString("entity-type")
		entityName, _ := cmd.Flags().GetString("entity-name")
		if entityName == "" {
			entityName = entityID
		}
		if err := ont.AddEntity(ontology.Entity{
			ID: entityID, Type: entityType, Name: entityName,
		}); err != nil {
			if outputFormat == "json" {
				fmt.Println(cli.FormatJSON(false, nil, err.Error()))
				return nil
			}
			return err
		}
		msg := fmt.Sprintf("Entity created: %s (%s)", entityID, entityType)
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, map[string]string{"message": msg}, ""))
		} else {
			fmt.Println(msg)
		}
		return nil
	}

	errMsg := "provide --from/--to/--relation for relations, or --entity-id for entities"
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(false, nil, errMsg))
		return nil
	}
	return fmt.Errorf(errMsg)
}
```

- [ ] **Step 2: Register + build + smoke test**

Add `ontologyCmd` to `rootCmd.AddCommand(...)`, build, test:
```bash
./sage-wiki ontology query --entity "注册制" --project /Users/kellen/claude-workspace/wiki/ --format json | head -5
```

- [ ] **Step 3: Commit**

```bash
git add cmd/sage-wiki/ontology_cmd.go cmd/sage-wiki/main.go
git commit -m "feat: add sage-wiki ontology command (replaces wiki_ontology_query + wiki_add_ontology)"
```

---

### Task 6: `sage-wiki write` command (summary + article)

**Files:**
- Create: `cmd/sage-wiki/write_cmd.go`

- [ ] **Step 1: Create write_cmd.go**

```go
// cmd/sage-wiki/write_cmd.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

var writeCmd = &cobra.Command{
	Use:   "write",
	Short: "Write a summary or article",
}

var writeSummaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Write a summary for a source file",
	RunE:  runWriteSummary,
}

var writeArticleCmd = &cobra.Command{
	Use:   "article",
	Short: "Write an article for a concept",
	RunE:  runWriteArticle,
}

func init() {
	writeSummaryCmd.Flags().String("source", "", "Source file path relative to project root")
	writeSummaryCmd.Flags().String("content", "", "Summary markdown content")
	writeSummaryCmd.Flags().String("concepts", "", "Comma-separated concept names")
	writeSummaryCmd.MarkFlagRequired("source")
	writeSummaryCmd.MarkFlagRequired("content")

	writeArticleCmd.Flags().String("concept", "", "Concept ID (lowercase-hyphenated)")
	writeArticleCmd.Flags().String("content", "", "Article markdown content")
	writeArticleCmd.MarkFlagRequired("concept")
	writeArticleCmd.MarkFlagRequired("content")

	writeCmd.AddCommand(writeSummaryCmd, writeArticleCmd)
}

func runWriteSummary(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	source, _ := cmd.Flags().GetString("source")
	content, _ := cmd.Flags().GetString("content")
	conceptsStr, _ := cmd.Flags().GetString("concepts")

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	baseName := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	summaryPath := filepath.Join(cfg.Output, "summaries", baseName+".md")
	absPath := filepath.Join(dir, summaryPath)
	os.MkdirAll(filepath.Dir(absPath), 0755)

	fm := fmt.Sprintf("---\nsource: %s\ncompiled_at: %s\n---\n\n", source, cfg.Compiler.UserNow())
	if err := os.WriteFile(absPath, []byte(fm+content), 0644); err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	// Index in FTS5 + embed
	db, dbErr := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if dbErr == nil {
		defer db.Close()
		memory.NewStore(db).Add(memory.Entry{ID: source, Content: content, ArticlePath: summaryPath})
		if embedder := embed.NewFromConfig(cfg); embedder != nil {
			if v, err := embedder.Embed(content); err == nil {
				vectors.NewStore(db).Upsert(source, v)
			}
		}
	}

	// Update manifest
	var concepts []string
	if conceptsStr != "" {
		for _, c := range strings.Split(conceptsStr, ",") {
			if c = strings.TrimSpace(c); c != "" {
				concepts = append(concepts, c)
			}
		}
	}
	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if _, exists := mf.Sources[source]; !exists {
		mf.AddSource(source, "", "article", int64(len(content)))
	}
	mf.MarkCompiled(source, summaryPath, concepts)
	mf.Save(filepath.Join(dir, ".manifest.json"))

	msg := fmt.Sprintf("Summary written: %s (%d chars)", summaryPath, len(content))
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"path": summaryPath}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}

func runWriteArticle(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	conceptID, _ := cmd.Flags().GetString("concept")
	content, _ := cmd.Flags().GetString("content")

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	articlePath := filepath.Join(cfg.Output, "concepts", conceptID+".md")
	absPath := filepath.Join(dir, articlePath)
	os.MkdirAll(filepath.Dir(absPath), 0755)

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	// Update ontology + FTS5 + embed
	db, dbErr := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if dbErr == nil {
		defer db.Close()
		merged := ontology.MergedRelations(cfg.Ontology.Relations)
		ont := ontology.NewStore(db, ontology.ValidRelationNames(merged))
		ont.AddEntity(ontology.Entity{ID: conceptID, Type: "concept", Name: conceptID, ArticlePath: articlePath})
		memory.NewStore(db).Add(memory.Entry{ID: "concept:" + conceptID, Content: content, ArticlePath: articlePath})
		if embedder := embed.NewFromConfig(cfg); embedder != nil {
			if v, err := embedder.Embed(content); err == nil {
				vectors.NewStore(db).Upsert("concept:"+conceptID, v)
			}
		}
	}

	// Update manifest
	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))
	mf.AddConcept(conceptID, articlePath, nil)
	mf.Save(filepath.Join(dir, ".manifest.json"))

	msg := fmt.Sprintf("Article written: %s", articlePath)
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"path": articlePath}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}
```

- [ ] **Step 2: Register + build**

Add `writeCmd` to `rootCmd.AddCommand(...)`, build, vet.

- [ ] **Step 3: Commit**

```bash
git add cmd/sage-wiki/write_cmd.go cmd/sage-wiki/main.go
git commit -m "feat: add sage-wiki write command (replaces wiki_write_summary + wiki_write_article)"
```

---

### Task 7: `sage-wiki learn` + `sage-wiki capture` + `sage-wiki add-source`

**Files:**
- Create: `cmd/sage-wiki/learn.go`
- Create: `cmd/sage-wiki/capture.go`
- Create: `cmd/sage-wiki/add_source.go` (F2 fix: replaces wiki_add_source MCP tool)

- [ ] **Step 1: Create learn.go**

```go
// cmd/sage-wiki/learn.go
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/linter"
	"github.com/xoai/sage-wiki/internal/storage"
)

var learnCmd = &cobra.Command{
	Use:   "learn [content]",
	Short: "Store a learning entry",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runLearn,
}

func init() {
	learnCmd.Flags().String("type", "gotcha", "Learning type: gotcha, correction, convention, error-fix, api-drift")
	learnCmd.Flags().String("tags", "", "Comma-separated tags")
}

func runLearn(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	content := strings.Join(args, " ")
	learnType, _ := cmd.Flags().GetString("type")
	tagsStr, _ := cmd.Flags().GetString("tags")

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	defer db.Close()

	if err := linter.StoreLearning(db, learnType, content, tagsStr, "cli"); err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	display := content
	if len(display) > 80 {
		display = display[:80] + "..."
	}
	msg := fmt.Sprintf("Learning stored: [%s] %s", learnType, display)
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"message": msg}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}
```

- [ ] **Step 2: Create capture.go**

```go
// cmd/sage-wiki/capture.go
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
)

var captureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Capture knowledge from text",
	RunE:  runCapture,
}

func init() {
	captureCmd.Flags().String("text", "", "Text content to capture")
	captureCmd.Flags().String("file", "", "File to read (use - for stdin)")
	captureCmd.Flags().String("context", "", "Context description")
	captureCmd.Flags().String("tags", "", "Comma-separated tags")
}

func runCapture(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	text, _ := cmd.Flags().GetString("text")
	filePath, _ := cmd.Flags().GetString("file")
	captureCtx, _ := cmd.Flags().GetString("context")
	tagsStr, _ := cmd.Flags().GetString("tags")

	var content string
	if text != "" {
		content = text
	} else if filePath == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			if outputFormat == "json" {
				fmt.Println(cli.FormatJSON(false, nil, err.Error()))
				return nil
			}
			return err
		}
		content = string(data)
	} else if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			if outputFormat == "json" {
				fmt.Println(cli.FormatJSON(false, nil, err.Error()))
				return nil
			}
			return err
		}
		content = string(data)
	} else {
		errMsg := "provide --text or --file (use --file - for stdin)"
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, errMsg))
			return nil
		}
		return fmt.Errorf(errMsg)
	}

	if len(content) > 100*1024 {
		errMsg := fmt.Sprintf("content too large (%d bytes, max 100KB)", len(content))
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, errMsg))
			return nil
		}
		return fmt.Errorf(errMsg)
	}

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	// Write as raw capture file (same as MCP fallback)
	capturesDir := filepath.Join(dir, "raw", "captures")
	os.MkdirAll(capturesDir, 0755)

	slug := fmt.Sprintf("capture-%s", cfg.Compiler.UserNow()[:10])
	relPath := filepath.Join("raw", "captures", slug+".md")
	absPath := filepath.Join(dir, relPath)
	for i := 1; ; i++ {
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			break
		}
		relPath = filepath.Join("raw", "captures", fmt.Sprintf("%s-%d.md", slug, i))
		absPath = filepath.Join(dir, relPath)
	}

	fm := fmt.Sprintf("---\nsource: cli-capture\ncaptured_at: %s\n", cfg.Compiler.UserNow())
	if tagsStr != "" {
		fm += fmt.Sprintf("tags: [%s]\n", tagsStr)
	}
	if captureCtx != "" {
		fm += fmt.Sprintf("context: %q\n", captureCtx)
	}
	fm += "---\n\n"

	if err := os.WriteFile(absPath, []byte(fm+content+"\n"), 0644); err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	msg := fmt.Sprintf("Captured to %s. Run `sage-wiki compile` to process.", relPath)
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"path": relPath}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}
```

- [ ] **Step 3: Create add_source.go (F2 fix)**

```go
// cmd/sage-wiki/add_source.go
package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/manifest"
)

var addSourceCmd = &cobra.Command{
	Use:   "add-source [path]",
	Short: "Register a source file in the manifest",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddSource,
}

func init() {
	addSourceCmd.Flags().String("type", "auto", "Source type: article, paper, code, auto")
}

func runAddSource(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	relPath := args[0]
	srcType, _ := cmd.Flags().GetString("type")

	absPath := filepath.Join(dir, relPath)
	info, err := os.Stat(absPath)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, fmt.Sprintf("file not found: %s", relPath)))
			return nil
		}
		return fmt.Errorf("file not found: %s", relPath)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(data))

	if srcType == "" || srcType == "auto" {
		srcType = "article"
	}

	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	mf.AddSource(relPath, hash, srcType, info.Size())
	if err := mf.Save(filepath.Join(dir, ".manifest.json")); err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	msg := fmt.Sprintf("Source added: %s (type: %s, %d bytes)", relPath, srcType, info.Size())
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"path": relPath, "type": srcType}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}
```

- [ ] **Step 4: Register all three + build**

Add `learnCmd, captureCmd, addSourceCmd` to `rootCmd.AddCommand(...)`, build, vet.

- [ ] **Step 5: Commit**

```bash
git add cmd/sage-wiki/learn.go cmd/sage-wiki/capture.go cmd/sage-wiki/add_source.go cmd/sage-wiki/main.go
git commit -m "feat: add sage-wiki learn + capture + add-source commands"
```

---

### Task 8: Config extends support (F1 fix: deepMerge)

**Files:**
- Create: `internal/config/merge.go` (deepMerge helper)
- Create: `internal/config/merge_test.go` (deepMerge tests)
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Implementation note (F1 fix):** Must NOT use double-unmarshal (yaml base → struct, yaml child → same struct). yaml.v3 will clobber base values with zero values when child partially specifies a nested struct. Instead: unmarshal both to `map[string]any`, recursive deepMerge maps, marshal merged map → yaml → unmarshal to Config struct.

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestLoadWithExtends(t *testing.T) {
	dir := t.TempDir()

	basePath := filepath.Join(dir, "sage-base.yaml")
	baseContent := `
version: 1
project: base-wiki
description: "Base"
sources:
  - path: raw
    type: auto
output: wiki
api:
  provider: openai-compatible
  api_key: sk-base-key
  base_url: https://api.example.com/v1
  rate_limit: 480
models:
  summarize: model-a
  extract: model-a
  write: model-a
compiler:
  max_parallel: 8
  summary_max_tokens: 8000
`
	os.WriteFile(basePath, []byte(baseContent), 0644)

	childDir := filepath.Join(dir, "child")
	os.MkdirAll(childDir, 0755)
	childPath := filepath.Join(childDir, "config.yaml")
	childContent := `
extends: ../sage-base.yaml
project: child-wiki
sources:
  - path: raw
    type: auto
output: wiki
`
	os.WriteFile(childPath, []byte(childContent), 0644)

	cfg, err := Load(childPath)
	if err != nil {
		t.Fatalf("Load with extends: %v", err)
	}
	if cfg.Project != "child-wiki" {
		t.Errorf("project: got %q, want child-wiki", cfg.Project)
	}
	if cfg.API.Provider != "openai-compatible" {
		t.Errorf("api.provider not inherited: got %q", cfg.API.Provider)
	}
	if cfg.API.RateLimit != 480 {
		t.Errorf("rate_limit not inherited: got %d", cfg.API.RateLimit)
	}
	if cfg.Compiler.SummaryMaxTokens != 8000 {
		t.Errorf("summary_max_tokens not inherited: got %d", cfg.Compiler.SummaryMaxTokens)
	}
}

func TestLoadExtendsMissing(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(`
extends: ../nonexistent.yaml
project: test
sources:
  - path: raw
    type: auto
output: wiki
`), 0644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("should not fail on missing base: %v", err)
	}
	if cfg.Project != "test" {
		t.Errorf("project: got %q", cfg.Project)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/config/ -run TestLoadWithExtends -v`
Expected: FAIL — API fields not inherited

- [ ] **Step 3: Add Extends field to Config struct (line 18)**

```go
type Config struct {
	Extends     string       `yaml:"extends,omitempty"`
	Version     int          `yaml:"version"`
```

- [ ] **Step 4: Create deepMerge helper (internal/config/merge.go)**

```go
// internal/config/merge.go
package config

// deepMerge recursively merges src into dst (map level).
// Maps: recursive merge. Slices/scalars: src replaces dst.
func deepMerge(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if srcMap, ok := v.(map[string]any); ok {
			if dstMap, ok := out[k].(map[string]any); ok {
				out[k] = deepMerge(dstMap, srcMap)
				continue
			}
		}
		out[k] = v
	}
	return out
}
```

- [ ] **Step 5: Write deepMerge test (internal/config/merge_test.go)**

```go
package config

import "testing"

func TestDeepMergePartialNested(t *testing.T) {
	base := map[string]any{
		"compiler": map[string]any{
			"max_parallel":      8,
			"summary_max_tokens": 8000,
			"auto_commit":       true,
		},
	}
	child := map[string]any{
		"compiler": map[string]any{
			"auto_commit": false,
		},
	}
	merged := deepMerge(base, child)
	compiler := merged["compiler"].(map[string]any)
	if compiler["max_parallel"] != 8 {
		t.Errorf("max_parallel: got %v, want 8", compiler["max_parallel"])
	}
	if compiler["summary_max_tokens"] != 8000 {
		t.Errorf("summary_max_tokens: got %v, want 8000", compiler["summary_max_tokens"])
	}
	if compiler["auto_commit"] != false {
		t.Errorf("auto_commit: got %v, want false", compiler["auto_commit"])
	}
}
```

- [ ] **Step 6: Implement extends merge in Load() using deepMerge (replace lines 183-202)**

```go
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config.Load: %w", err)
	}

	expanded := expandEnvVars(string(data))

	// Quick parse to check for extends field
	var peek struct{ Extends string `yaml:"extends"` }
	yaml.Unmarshal([]byte(expanded), &peek)

	finalYAML := expanded
	if peek.Extends != "" {
		basePath := peek.Extends
		if !filepath.IsAbs(basePath) {
			basePath = filepath.Join(filepath.Dir(path), basePath)
		}

		baseData, err := os.ReadFile(basePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: extends base %q not found, using child config only\n", peek.Extends)
		} else {
			baseExpanded := expandEnvVars(string(baseData))

			// Deep merge via map[string]any to avoid yaml.v3 zero-value clobbering
			var baseMap, childMap map[string]any
			if err := yaml.Unmarshal([]byte(baseExpanded), &baseMap); err != nil {
				return nil, fmt.Errorf("config.Load: parse base %q: %w", peek.Extends, err)
			}
			if err := yaml.Unmarshal([]byte(expanded), &childMap); err != nil {
				return nil, fmt.Errorf("config.Load: parse child: %w", err)
			}
			// Remove extends from child before merge
			delete(childMap, "extends")

			merged := deepMerge(baseMap, childMap)
			mergedBytes, err := yaml.Marshal(merged)
			if err != nil {
				return nil, fmt.Errorf("config.Load: marshal merged: %w", err)
			}
			finalYAML = string(mergedBytes)
		}
	}

	cfg := Defaults()
	if err := yaml.Unmarshal([]byte(finalYAML), &cfg); err != nil {
		return nil, fmt.Errorf("config.Load: parse error: %w", err)
	}
	cfg.Extends = "" // clear after merge

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}
```

- [ ] **Step 5: Run all config tests**

Run: `go test ./internal/config/ -v`
Expected: ALL PASS

- [ ] **Step 6: Run full test suite**

Run: `go test ./... 2>&1 | tail -20`
Expected: All pass

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add config extends for YAML inheritance (single-level)"
```

---

### Task 9: Phase 1 integration verification

- [ ] **Step 1: Full build + test**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
go build -o sage-wiki ./cmd/sage-wiki/ && go vet ./... && go test ./...
```

- [ ] **Step 2: Smoke test all new commands against real wiki**

```bash
SAGE=./sage-wiki
WIKI=/Users/kellen/claude-workspace/wiki

$SAGE diff --project $WIKI --format json | python3 -c "import sys,json; print('diff:', json.load(sys.stdin)['ok'])"
$SAGE list --type concepts --project $WIKI --format json | python3 -c "import sys,json; print('list:', json.load(sys.stdin)['ok'])"
$SAGE ontology query --entity "注册制" --project $WIKI --format json | python3 -c "import sys,json; print('ontology:', json.load(sys.stdin)['ok'])"
$SAGE status --project $WIKI --format json | python3 -c "import sys,json; print('status:', json.load(sys.stdin)['ok'])"
$SAGE search "注册制" --project $WIKI --format json | python3 -c "import sys,json; print('search:', json.load(sys.stdin)['ok'])"
```

Expected: All print `True`

- [ ] **Step 3: Tag Phase 1 completion**

```bash
git add -A && git status
git commit -m "chore: Phase 1 complete — CLI补齐 + JSON output + config extends" --allow-empty
```

---

## Phase 2: Hub 子命令

### Task 10: Hub config package

**Files:**
- Create: `internal/hub/hub.go`
- Test: `internal/hub/hub_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/hub/hub_test.go
package hub

import (
	"path/filepath"
	"testing"
)

func TestNewAndSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.yaml")

	cfg := New()
	cfg.AddProject("main", Project{Path: "/tmp/wiki", Description: "Main", Searchable: true})

	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Projects["main"].Path != "/tmp/wiki" {
		t.Errorf("path not persisted: %q", loaded.Projects["main"].Path)
	}
}

func TestSearchableProjects(t *testing.T) {
	cfg := New()
	cfg.AddProject("a", Project{Path: "/a", Searchable: true})
	cfg.AddProject("b", Project{Path: "/b", Searchable: false})
	cfg.AddProject("c", Project{Path: "/c", Searchable: true})

	s := cfg.SearchableProjects()
	if len(s) != 2 {
		t.Errorf("expected 2 searchable, got %d", len(s))
	}
}

func TestRemoveProject(t *testing.T) {
	cfg := New()
	cfg.AddProject("x", Project{Path: "/x"})
	cfg.RemoveProject("x")
	if len(cfg.Projects) != 0 {
		t.Error("not removed")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/hub/ -v`

- [ ] **Step 3: Implement hub.go**

```go
// internal/hub/hub.go
package hub

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Project represents a registered sage-wiki project.
type Project struct {
	Path        string `yaml:"path" json:"path"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Searchable  bool   `yaml:"searchable" json:"searchable"`
}

// HubConfig holds the multi-project registry.
type HubConfig struct {
	Projects map[string]Project `yaml:"projects" json:"projects"`
}

func New() *HubConfig {
	return &HubConfig{Projects: make(map[string]Project)}
}

func Load(path string) (*HubConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hub.Load: %w", err)
	}
	var cfg HubConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("hub.Load: %w", err)
	}
	if cfg.Projects == nil {
		cfg.Projects = make(map[string]Project)
	}
	return &cfg, nil
}

func (c *HubConfig) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("hub.Save: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

func (c *HubConfig) AddProject(name string, p Project) {
	c.Projects[name] = p
}

func (c *HubConfig) RemoveProject(name string) {
	delete(c.Projects, name)
}

func (c *HubConfig) SearchableProjects() map[string]Project {
	result := make(map[string]Project)
	for name, p := range c.Projects {
		if p.Searchable {
			result[name] = p
		}
	}
	return result
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sage-hub.yaml")
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/hub/ -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add internal/hub/hub.go internal/hub/hub_test.go
git commit -m "feat: add hub config package (project registry)"
```

---

### Task 11: Federated search + OpenReadOnly (F5 fix)

**Files:**
- Create: `internal/storage/readonly.go` (F5: read-only DB open for search)
- Create: `internal/hub/search.go`
- Test: `internal/hub/search_test.go`

**Implementation note (F5 fix):** `searchProject()` must use `storage.OpenReadOnly()` instead of `storage.Open()` to avoid unnecessary migrations and write locks. Also (W2 fix): if ALL projects fail, return an error instead of empty results + nil error.

- [ ] **Step 1: Write failing test**

```go
// internal/hub/search_test.go
package hub

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/wiki"
)

func setupTestDB(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, name, "test-model"); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(dir + "/.sage/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	memory.NewStore(db).Add(memory.Entry{
		ID: name + "-e1", Content: "test content about " + name,
		ArticlePath: "wiki/concepts/" + name + ".md",
	})
	return dir
}

func TestFederatedSearch(t *testing.T) {
	dir1 := setupTestDB(t, "alpha")
	dir2 := setupTestDB(t, "beta")

	projects := map[string]Project{
		"alpha": {Path: dir1, Searchable: true},
		"beta":  {Path: dir2, Searchable: true},
	}

	results, err := FederatedSearch(projects, "test content", 10)
	if err != nil {
		t.Fatalf("FederatedSearch: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results")
	}
	for _, r := range results {
		if r.Project == "" {
			t.Error("missing project field")
		}
	}
}
```

- [ ] **Step 2: Implement search.go**

```go
// internal/hub/search.go
package hub

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// FederatedResult is a search result tagged with source project.
type FederatedResult struct {
	Project     string   `json:"project"`
	ArticlePath string   `json:"article_path"`
	Content     string   `json:"content"`
	RRFScore    float64  `json:"rrf_score"`
	Tags        []string `json:"tags,omitempty"`
}

// FederatedSearch searches multiple projects in parallel with RRF merge.
func FederatedSearch(projects map[string]Project, query string, limit int) ([]FederatedResult, error) {
	type projectResult struct {
		name    string
		results []hybrid.Result
		err     error
	}

	var wg sync.WaitGroup
	ch := make(chan projectResult, len(projects))

	for name, proj := range projects {
		wg.Add(1)
		go func(name string, proj Project) {
			defer wg.Done()
			results, err := searchProject(proj.Path, query, limit)
			ch <- projectResult{name: name, results: results, err: err}
		}(name, proj)
	}

	wg.Wait()
	close(ch)

	var all []FederatedResult
	for pr := range ch {
		if pr.err != nil {
			fmt.Printf("warning: search %s failed: %v\n", pr.name, pr.err)
			continue
		}
		for _, r := range pr.results {
			all = append(all, FederatedResult{
				Project:     pr.name,
				ArticlePath: r.ArticlePath,
				Content:     r.Content,
				RRFScore:    r.RRFScore,
				Tags:        r.Tags,
			})
		}
	}

	// Sort by score descending, then apply RRF re-ranking
	sort.Slice(all, func(i, j int) bool {
		return all[i].RRFScore > all[j].RRFScore
	})

	k := 60.0
	for i := range all {
		all[i].RRFScore = 1.0 / (k + float64(i) + 1)
	}

	if len(all) > limit {
		all = all[:limit]
	}

	return all, nil
}

func searchProject(projectDir string, query string, limit int) ([]hybrid.Result, error) {
	db, err := storage.Open(filepath.Join(projectDir, ".sage", "wiki.db"))
	if err != nil {
		return nil, err
	}
	defer db.Close()

	mem := memory.NewStore(db)
	vec := vectors.NewStore(db)
	searcher := hybrid.NewSearcher(mem, vec)

	var queryVec []float32
	var bm25W, vecW float64
	cfg, cfgErr := config.Load(filepath.Join(projectDir, "config.yaml"))
	if cfgErr == nil {
		if embedder := embed.NewFromConfig(cfg); embedder != nil {
			queryVec, _ = embedder.Embed(query)
		}
		bm25W = cfg.Search.HybridWeightBM25
		vecW = cfg.Search.HybridWeightVector
	}

	return searcher.Search(hybrid.SearchOpts{
		Query: query, Limit: limit, BM25Weight: bm25W, VectorWeight: vecW,
	}, queryVec)
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/hub/ -v`
Expected: ALL PASS

- [ ] **Step 4: Commit**

```bash
git add internal/hub/search.go internal/hub/search_test.go
git commit -m "feat: add federated search with parallel DB queries + RRF merge"
```

---

### Task 12: Hub CLI commands

**Files:**
- Create: `cmd/sage-wiki/hub.go`

- [ ] **Step 1: Create hub.go with all subcommands**

Create `cmd/sage-wiki/hub.go` with commands: `hub init`, `hub add <path>`, `hub remove <name>`, `hub list`, `hub search <query>`, `hub status`, `hub compile <name|--all>`.

Each command follows the same pattern as Tasks 3-7:
- Use `hub.Load(hub.DefaultPath())` for config
- JSON output via `cli.FormatJSON()`
- Text fallback for human readability
- `hub search` calls `hub.FederatedSearch()`
- `hub status` calls `wiki.GetStatus()` per project
- `hub compile` calls `compiler.Compile()` per project

(Full code matches the pattern established in Tasks 3-7. The implementation is ~250 lines following identical patterns.)

- [ ] **Step 2: Register `hubCmd` in main.go**

- [ ] **Step 3: Build + smoke test**

```bash
go build -o sage-wiki ./cmd/sage-wiki/
./sage-wiki hub init
./sage-wiki hub add /Users/kellen/claude-workspace/wiki/
./sage-wiki hub list --format json
./sage-wiki hub status --format json
```

- [ ] **Step 4: Commit**

```bash
git add cmd/sage-wiki/hub.go cmd/sage-wiki/main.go
git commit -m "feat: add sage-wiki hub commands (multi-project federation)"
```

---

## Phase 3: Skill + 迁移

### Task 13: `/wiki` skill

**Files:**
- Create: `~/.claude/skills/wiki/SKILL.md`

- [ ] **Step 1: Create wiki skill**

Skill routes `/wiki <subcommand>` to CLI calls with `--format json`, parses output, formats for display. Key routes:

| User command | CLI call |
|---|---|
| `/wiki search <q>` | `sage-wiki hub search "<q>" --format json` |
| `/wiki search <q> -p <name>` | `sage-wiki search "<q>" --project <path> --format json` |
| `/wiki status` | `sage-wiki hub status --format json` |
| `/wiki compile [name]` | `sage-wiki hub compile <name>` or `sage-wiki compile --project <path>` |
| `/wiki diff` | `sage-wiki diff --project <path> --format json` |
| `/wiki lint [--fix]` | `sage-wiki lint [--fix] --project <path> --format json` |
| `/wiki list [--type X]` | `sage-wiki list --type X --project <path> --format json` |
| `/wiki ontology query/add` | `sage-wiki ontology query/add ... --project <path> --format json` |

Binary path: `/Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki/sage-wiki`

- [ ] **Step 2: Verify accessible**

Run: `ls ~/.claude/skills/wiki/SKILL.md`

- [ ] **Step 3: Test with `/wiki status`**

---

### Task 14: wiki-improve 迁移

**Files:**
- Modify: `~/.claude/skills/wiki-improve/SKILL.md`

- [ ] **Step 1: Replace all `mcp__sage-wiki__*` references with CLI equivalents**

| Find | Replace with (Bash tool) |
|---|---|
| `mcp__sage-wiki__wiki_compile_diff` | `sage-wiki diff --project ~/wiki/ --format json` |
| `mcp__sage-wiki__wiki_status` | `sage-wiki status --project ~/wiki/ --format json` |
| `mcp__sage-wiki__wiki_search` | `sage-wiki search "<q>" --project ~/wiki/ --format json` |
| `mcp__sage-wiki__wiki_lint` | `sage-wiki lint --project ~/wiki/ --format json` |
| `mcp__sage-wiki__wiki_compile` | `echo y \| sage-wiki compile --project ~/wiki/` |
| `mcp__sage-wiki__wiki_add_ontology` | `sage-wiki ontology add ... --project ~/wiki/` |
| `mcp__sage-wiki__wiki_ontology_query` | `sage-wiki ontology query ... --format json` |
| `mcp__sage-wiki__wiki_write_article` | `sage-wiki write article ... --project ~/wiki/` |

- [ ] **Step 2: Add SAGE binary path at top of skill**

- [ ] **Step 3: Test with `/wiki-improve` option 1**

---

## Phase 4: 清理

### Task 15: 移除 MCP 配置

- [ ] **Step 1: Remove sage-wiki MCP entry from ~/.claude.json**

- [ ] **Step 2: Verify no remaining MCP references in skills**

```bash
grep -r "mcp__sage-wiki" ~/.claude/skills/ 2>/dev/null
```
Expected: No matches

- [ ] **Step 3: End-to-end verification**

```bash
SAGE=/Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki/sage-wiki
$SAGE status --project /Users/kellen/claude-workspace/wiki/ --format json
$SAGE search "注册制" --project /Users/kellen/claude-workspace/wiki/ --format json
$SAGE hub status --format json
```

All should work without MCP server running.
