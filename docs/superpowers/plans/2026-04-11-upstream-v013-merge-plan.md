# Upstream v0.1.3 Merge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge upstream v0.1.3 (enhanced search + graph retrieval) into fork branch feature/chinese-localization, preserving all fork additions.

**Architecture:** `git merge upstream/main` → resolve 3-4 conflicts (take upstream as base, add fork code) → build → test → Rule 2 template/config check.

**Tech Stack:** Go, cobra CLI, SQLite FTS5, git

---

### Task 1: Execute Merge

**Files:** All 40 upstream files merge in; 3-4 files will have conflict markers.

- [ ] **Step 1: Run git merge**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git merge upstream/main
```

Expected: merge conflicts in `cmd/sage-wiki/main.go`, `internal/ontology/ontology.go`, `README.md`. Possibly `internal/config/config.go` (likely auto-merges).

- [ ] **Step 2: Check which files actually conflicted**

```bash
git diff --name-only --diff-filter=U
```

Record the list, proceed to Task 2.

---

### Task 2: Resolve main.go Conflicts

**Files:**
- Modify: `cmd/sage-wiki/main.go`

The conflict is at the `rootCmd.AddCommand` line (line 162 in fork). Both sides changed this line.

- [ ] **Step 1: Read the conflicted file and find conflict markers**

```bash
grep -n "<<<<<<" cmd/sage-wiki/main.go
```

- [ ] **Step 2: Resolve AddCommand conflict**

The upstream version adds `provenanceCmd` to the AddCommand list. The fork version adds 8 commands (diffCmd, listCmd, etc.). The resolved line must have ALL commands:

```go
rootCmd.AddCommand(initCmd, compileCmd, serveCmd, lintCmd, searchCmd, queryCmd, statusCmd, ingestCmd, doctorCmd, tuiCmd, provenanceCmd, diffCmd, listCmd, ontologyCmd, writeCmd, learnCmd, captureCmd, addSourceCmd, hubCmd)
```

- [ ] **Step 3: Resolve any other conflicts in main.go**

Upstream adds `provenanceCmd` var declaration (after `tuiCmd`) and `--prune` flag + `runProvenance` function. Fork adds `outputFormat` var, `SilenceErrors`/`SilenceUsage`, JSON error envelope, `--format` flag.

Both must be present. Verify:
1. Fork's `outputFormat` var in the `var` block ✓
2. Fork's `SilenceErrors`/`SilenceUsage` in `main()` ✓
3. Fork's JSON error envelope in `main()` ✓
4. Fork's `--format` flag in `init()` ✓
5. Upstream's `provenanceCmd` var declaration ✓
6. Upstream's `--prune` flag in compile flags ✓
7. Upstream's `prune` in `runCompile` ✓
8. Upstream's `runProvenance` function at end ✓

- [ ] **Step 4: Verify no conflict markers remain**

```bash
grep -c "<<<<<<" cmd/sage-wiki/main.go
```

Expected: 0

---

### Task 3: Resolve ontology.go Conflicts

**Files:**
- Modify: `internal/ontology/ontology.go`

Both sides append methods after `RelationCount()` at line 348. Upstream adds `EntityDegree`, `EntitiesCiting`, `CitedBy`. Fork adds `ListRelations`.

- [ ] **Step 1: Read the conflicted section**

```bash
grep -n "<<<<<<" internal/ontology/ontology.go
```

- [ ] **Step 2: Resolve — keep both sets of methods**

The resolved file should have after `RelationCount()`:
1. Upstream's `EntityDegree` method
2. Upstream's `EntitiesCiting` method
3. Upstream's `CitedBy` method
4. Fork's `ListRelations` method

All 4 methods are independent (no naming conflicts, no shared code).

- [ ] **Step 3: Verify no conflict markers remain**

```bash
grep -c "<<<<<<" internal/ontology/ontology.go
```

Expected: 0

---

### Task 4: Resolve README.md Conflicts

**Files:**
- Modify: `README.md`

Upstream changes: (1) compile command adds `--prune`, (2) adds `provenance` command row, (3) adds Search Quality section. Fork changes: adds 8 command rows after `doctor`.

- [ ] **Step 1: Read the conflicted section**

```bash
grep -n "<<<<<<" README.md
```

- [ ] **Step 2: Resolve Commands table**

The final Commands table should contain ALL rows. Upstream's compile line with `--prune`:

```markdown
| `sage-wiki compile [--watch] [--dry-run] [--batch] [--estimate] [--no-cache] [--prune]` | Compile sources into wiki articles |
```

After `doctor`, upstream's `provenance` + fork's 8 commands:

```markdown
| `sage-wiki doctor` | Validate config and connectivity |
| `sage-wiki provenance <source-or-concept>` | Show source↔article provenance mappings |
| `sage-wiki diff` | Show pending source changes against manifest |
| `sage-wiki list` | List wiki entities, concepts, or sources |
| `sage-wiki write <summary\|article>` | Write a summary or article |
| `sage-wiki ontology <query\|list\|add>` | Query, list, and manage the ontology graph |
| `sage-wiki hub <add\|remove\|search\|status\|list>` | Multi-project hub commands |
| `sage-wiki learn "text"` | Store a learning entry |
| `sage-wiki capture "text"` | Capture knowledge from text |
| `sage-wiki add-source <path>` | Register a source file in the manifest |
```

- [ ] **Step 3: Verify upstream's Search Quality section is present**

The section starting with `## Search Quality` should be present (from upstream). No fork changes conflict with this.

- [ ] **Step 4: Verify no conflict markers remain**

```bash
grep -c "<<<<<<" README.md
```

Expected: 0

---

### Task 5: Verify config.go (likely auto-merged)

**Files:**
- Check: `internal/config/config.go`

- [ ] **Step 1: Check if config.go had conflicts**

```bash
grep -c "<<<<<<" internal/config/config.go 2>/dev/null || echo "no conflicts"
```

If 0 or "no conflicts" → auto-merged successfully, skip to Task 6.

If conflicts exist: fork's `Extends` field is at the top of `Config` struct, upstream's `SearchConfig` additions are in a different struct. Resolve by keeping both.

---

### Task 6: Complete Merge Commit

- [ ] **Step 1: Stage all resolved files**

```bash
git add -A
```

- [ ] **Step 2: Verify no remaining conflict markers anywhere**

```bash
grep -r "<<<<<<" --include="*.go" --include="*.md" . | grep -v ".git/" | grep -v "vendor/"
```

Expected: no output

- [ ] **Step 3: Commit the merge**

```bash
git commit --no-edit
```

This uses the default merge commit message.

---

### Task 7: Build and Test

- [ ] **Step 1: Build the binary**

```bash
go build -o sage-wiki ./cmd/sage-wiki/
```

Expected: no errors. If build fails, read the error and fix (likely an import or type mismatch from conflict resolution).

- [ ] **Step 2: Run all tests**

```bash
go test ./...
```

Expected: all pass. New upstream tests (pipeline_test.go, graph/relevance_test.go, search/*_test.go, memory/chunks_test.go, etc.) should pass alongside existing integration_test.go.

- [ ] **Step 3: Run go vet**

```bash
go vet ./...
```

Expected: no issues.

---

### Task 8: Post-Merge Verification

- [ ] **Step 1: Verify CLI commands (21 total)**

```bash
./sage-wiki --help 2>&1 | grep -E "^  [a-z]"
```

Expected 12 top-level commands: add-source, capture, compile, completion, diff, doctor, hub, init, ingest, learn, lint, list, ontology, provenance, query, search, serve, status, tui, write. Some may be hidden; count from help output.

- [ ] **Step 2: Test new provenance command**

```bash
./sage-wiki provenance --help
```

Expected: shows usage for provenance command.

- [ ] **Step 3: Rule 2 — Check write.go template variables**

Read the merged `internal/compiler/write.go` and check if `buildArticlePrompt` (or its replacement) passes new variables to the prompt template that fork's `~/claude-workspace/wiki/prompts/write-article.md` would need to handle.

```bash
grep -n "Render\|Execute\|templateData\|promptData" internal/compiler/write.go | head -20
```

If new template variables were added, check `~/claude-workspace/wiki/prompts/write-article.md` for compatibility.

- [ ] **Step 4: Rule 2 — Config.yaml defaults**

Upstream added search config fields with defaults (query_expansion=true, rerank=true, chunk_size=800, graph_expansion=true, etc.). No changes needed to `~/claude-workspace/wiki/config.yaml` — defaults are applied in Go code. Verify:

```bash
./sage-wiki doctor --project /Users/kellen/claude-workspace/wiki
```

Expected: passes without search config errors.

- [ ] **Step 5: Commit plan and spec files**

```bash
git add docs/superpowers/specs/2026-04-11-upstream-v013-merge-design.md docs/superpowers/plans/2026-04-11-upstream-v013-merge-plan.md
git commit -m "docs: add upstream v0.1.3 merge design and plan"
```
