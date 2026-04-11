# sage-wiki 健康检查问题修复实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复健康检查发现的全部 10 个问题，消除 MCP 残留、修复代码缺陷、补齐测试和文档。

**Architecture:** 4 阶段按依赖顺序执行：A 数据清理 → B 代码修改（Go + 文档 + 测试）→ C 运维操作（rebuild + recompile + re-embed）→ D 回归验证。

**Tech Stack:** Go 1.22+, cobra, SQLite3, Bash

---

## File Structure

| 文件 | 操作 | 职责 |
|------|------|------|
| `~/.claude.json` | 删除条目 | MCP 配置清理 (#1) |
| `~/claude-workspace/wiki/wiki/` | 清空子目录 | 旧编译残留清理 (#2) |
| `internal/compiler/diff.go:46-50` | 修改 | .DS_Store 隐藏文件过滤 (#3) |
| `internal/compiler/diff_test.go` | 修改 | .DS_Store 过滤测试 (#3) |
| `cmd/sage-wiki/main.go:40-45,47` | 修改 | JSON 错误信封 (#4) |
| `cmd/sage-wiki/ontology_cmd.go` | 修改 | ontology list 子命令 (#7) |
| `internal/ontology/ontology.go` | 修改 | ListRelations 方法 (#7) |
| `internal/ontology/ontology_test.go` | 修改 | ListRelations 测试 (#7) |
| `internal/hub/hub.go:50-52` | 修改 | hub add 覆盖提示 (#9) |
| `internal/hub/hub_test.go` | 修改 | 覆盖提示测试 (#9) |
| `README.md` | 修改 | 补充 fork 新增命令 (#8) |
| `integration_test.go` | 修改 | 扩展测试覆盖 (#10) |

---

### Task 1: 阶段 A — 数据清理 (#1, #2)

**Files:**
- Modify: `~/.claude.json` (删除 sage-wiki MCP 条目)
- Delete: `~/claude-workspace/wiki/wiki/concepts/*.md`, `~/claude-workspace/wiki/wiki/summaries/*.md`
- Delete: `~/claude-workspace/wiki/raw/**/.DS_Store`

- [ ] **Step 1: 删除 MCP 配置 (#1)**

读取 `~/.claude.json`，找到 `mcpServers.sage-wiki` 条目并删除。使用 python3 精确操作：

```bash
python3 -c "
import json
with open('/Users/kellen/.claude.json', 'r') as f:
    data = json.load(f)
if 'mcpServers' in data and 'sage-wiki' in data['mcpServers']:
    del data['mcpServers']['sage-wiki']
    with open('/Users/kellen/.claude.json', 'w') as f:
        json.dump(data, f, indent=2, ensure_ascii=False)
    print('REMOVED sage-wiki from mcpServers')
else:
    print('NOT FOUND')
"
```

预期：`REMOVED sage-wiki from mcpServers`

- [ ] **Step 2: 验证 MCP 配置已删除**

```bash
python3 -c "import json; d=json.load(open('/Users/kellen/.claude.json')); print('sage-wiki' in d.get('mcpServers', {}))"
```

预期：`False`

- [ ] **Step 3: 清理 wiki 旧编译残留 (#2)**

```bash
WIKI=/Users/kellen/claude-workspace/wiki
# 保留目录结构，删除内容
rm -f "$WIKI/wiki/concepts/"*.md
rm -f "$WIKI/wiki/summaries/"*.md
rm -f "$WIKI/wiki/CHANGELOG.md"
# 验证
echo "concepts: $(ls "$WIKI/wiki/concepts/" 2>/dev/null | wc -l)"
echo "summaries: $(ls "$WIKI/wiki/summaries/" 2>/dev/null | wc -l)"
```

预期：concepts: 0, summaries: 0

- [ ] **Step 4: 删除 .DS_Store 文件**

```bash
find /Users/kellen/claude-workspace/wiki/raw/ -name ".DS_Store" -delete -print
```

预期：列出被删除的 .DS_Store 文件

- [ ] **Step 5: Commit 数据清理（wiki 仓库）**

```bash
cd /Users/kellen/claude-workspace/wiki
git add -A
git commit -m "chore: clean stale wiki output + remove .DS_Store"
```

---

### Task 2: .DS_Store 源文件过滤 (#3)

**Files:**
- Modify: `internal/compiler/diff.go:46-50`
- Modify: `internal/compiler/diff_test.go`

- [ ] **Step 1: 写失败测试**

在 `internal/compiler/diff_test.go` 追加：

```go
func TestDiffIgnoresHiddenFiles(t *testing.T) {
	dir := t.TempDir()
	// 创建 config
	os.MkdirAll(filepath.Join(dir, "raw"), 0755)
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("project: test\nsources:\n  - raw\n"), 0644)
	// 创建正常文件和隐藏文件
	os.WriteFile(filepath.Join(dir, "raw", "doc.md"), []byte("# Doc"), 0644)
	os.WriteFile(filepath.Join(dir, "raw", ".DS_Store"), []byte{0, 0, 0, 1}, 0644)
	os.WriteFile(filepath.Join(dir, "raw", ".hidden.md"), []byte("# Hidden"), 0644)

	cfg, _ := config.Load(filepath.Join(dir, "config.yaml"))
	mf := &manifest.Manifest{Sources: make(map[string]manifest.Source)}

	result, err := Diff(dir, cfg, mf)
	if err != nil {
		t.Fatal(err)
	}

	// 只有 doc.md 应该被检测为 Added，隐藏文件应被跳过
	for _, s := range result.Added {
		if strings.HasPrefix(filepath.Base(s.Path), ".") {
			t.Errorf("hidden file should be filtered: %s", s.Path)
		}
	}
	if len(result.Added) != 1 {
		t.Errorf("expected 1 added, got %d", len(result.Added))
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
go test ./internal/compiler/ -run TestDiffIgnoresHiddenFiles -v
```

预期：FAIL（.DS_Store 和 .hidden.md 被当作 Added）

- [ ] **Step 3: 实现隐藏文件过滤**

在 `internal/compiler/diff.go` 的 `WalkDir` 回调中，`if err != nil || d.IsDir()` 后面添加隐藏文件检查：

```go
// diff.go:46-51 修改后
if err := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
	if err != nil || d.IsDir() {
		return nil
	}

	// 跳过隐藏文件（.DS_Store, .hidden 等）
	if strings.HasPrefix(d.Name(), ".") {
		return nil
	}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./internal/compiler/ -run TestDiffIgnoresHiddenFiles -v
```

预期：PASS

- [ ] **Step 5: 运行全部 compiler 测试确认无回归**

```bash
go test ./internal/compiler/ -v
```

预期：全部 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/compiler/diff.go internal/compiler/diff_test.go
git commit -m "fix: filter hidden files (.DS_Store) from source scanning"
```

---

### Task 3: JSON 错误信封 (#4)

**Files:**
- Modify: `cmd/sage-wiki/main.go:40-53`

- [ ] **Step 1: 修改 main() 和 rootCmd**

将 `main.go` 中的 `main()` 函数和 `rootCmd` 修改为：

```go
func main() {
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true

	if err := rootCmd.Execute(); err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
		} else {
			fmt.Fprintln(os.Stderr, "Error:", err)
			// 显示 usage（仅在 text 模式）
			if rootCmd.HasParent() {
				rootCmd.Usage()
			}
		}
		os.Exit(1)
	}
}
```

注意：`SilenceErrors` 和 `SilenceUsage` 阻止 cobra 自动输出错误和 usage，改由我们控制。

- [ ] **Step 2: 验证 JSON 错误输出**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
go build -o sage-wiki ./cmd/sage-wiki/

# 测试 JSON 错误输出
./sage-wiki search --project /Users/kellen/claude-workspace/wiki --format json 2>&1
```

预期：`{"ok":false,"error":"requires at least 1 arg(s), only received 0"}`

- [ ] **Step 3: 验证 text 模式错误仍正常**

```bash
./sage-wiki search --project /Users/kellen/claude-workspace/wiki 2>&1
```

预期：`Error: requires at least 1 arg(s)...` 在 stderr

- [ ] **Step 4: Commit**

```bash
git add cmd/sage-wiki/main.go
git commit -m "fix: wrap CLI errors in JSON envelope when --format json"
```

---

### Task 4: ontology list 子命令 (#7)

**Files:**
- Modify: `internal/ontology/ontology.go` (新增 ListRelations)
- Modify: `internal/ontology/ontology_test.go` (新增 TestListRelations)
- Modify: `cmd/sage-wiki/ontology_cmd.go` (新增 list 子命令)

- [ ] **Step 1: 写 ListRelations 测试**

在 `internal/ontology/ontology_test.go` 追加：

```go
func TestListRelations(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	s := NewStore(db, nil)

	// 添加实体和关系
	s.AddEntity(Entity{ID: "a", Type: "concept", Name: "A"})
	s.AddEntity(Entity{ID: "b", Type: "concept", Name: "B"})
	s.AddRelation(Relation{ID: "a-cites-b", SourceID: "a", TargetID: "b", Relation: "cites"})

	rels, err := s.ListRelations("", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 relation, got %d", len(rels))
	}
	if rels[0].Relation != "cites" {
		t.Errorf("expected 'cites', got %q", rels[0].Relation)
	}

	// 按类型过滤
	rels2, _ := s.ListRelations("cites", 100)
	if len(rels2) != 1 {
		t.Errorf("expected 1 filtered relation, got %d", len(rels2))
	}
	rels3, _ := s.ListRelations("extends", 100)
	if len(rels3) != 0 {
		t.Errorf("expected 0 filtered relations, got %d", len(rels3))
	}
}
```

- [ ] **Step 2: 实现 ListRelations**

在 `internal/ontology/ontology.go` 的 `RelationCount` 方法后追加：

```go
// ListRelations returns relations, optionally filtered by type, with a limit.
func (s *Store) ListRelations(relationType string, limit int) ([]Relation, error) {
	var rows *sql.Rows
	var err error
	if relationType != "" {
		rows, err = s.db.ReadDB().Query(
			`SELECT id, source_id, target_id, relation, created_at
			 FROM relations WHERE relation=? ORDER BY created_at DESC LIMIT ?`,
			relationType, limit,
		)
	} else {
		rows, err = s.db.ReadDB().Query(
			`SELECT id, source_id, target_id, relation, created_at
			 FROM relations ORDER BY created_at DESC LIMIT ?`,
			limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rels []Relation
	for rows.Next() {
		var r Relation
		if err := rows.Scan(&r.ID, &r.SourceID, &r.TargetID, &r.Relation, &r.CreatedAt); err != nil {
			return nil, err
		}
		rels = append(rels, r)
	}
	return rels, rows.Err()
}
```

- [ ] **Step 3: 运行测试**

```bash
go test ./internal/ontology/ -run TestListRelations -v
```

预期：PASS

- [ ] **Step 4: 添加 ontology list CLI 子命令**

在 `cmd/sage-wiki/ontology_cmd.go` 添加：

```go
var ontologyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List entities or relations",
	RunE:  runOntologyList,
}

func init() {
	// ... 在现有 init() 中添加:
	ontologyListCmd.Flags().String("type", "entities", "What to list: entities or relations")
	ontologyListCmd.Flags().String("entity-type", "", "Filter entities by type (concept, source, etc.)")
	ontologyListCmd.Flags().String("relation-type", "", "Filter relations by type")
	ontologyListCmd.Flags().Int("limit", 100, "Maximum results")

	ontologyCmd.AddCommand(ontologyQueryCmd, ontologyAddCmd, ontologyListCmd)
}

func runOntologyList(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	listType, _ := cmd.Flags().GetString("type")
	limit, _ := cmd.Flags().GetInt("limit")

	db, ont, err := openOntStore(dir)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	defer db.Close()

	switch listType {
	case "entities":
		entityType, _ := cmd.Flags().GetString("entity-type")
		entities, err := ont.ListEntities(entityType)
		if err != nil {
			if outputFormat == "json" {
				fmt.Println(cli.FormatJSON(false, nil, err.Error()))
				return nil
			}
			return err
		}
		if limit > 0 && len(entities) > limit {
			entities = entities[:limit]
		}
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, entities, ""))
			return nil
		}
		fmt.Printf("Entities: %d\n", len(entities))
		for _, e := range entities {
			fmt.Printf("  [%s] %s (%s)\n", e.Type, e.Name, e.ID)
		}

	case "relations":
		relType, _ := cmd.Flags().GetString("relation-type")
		rels, err := ont.ListRelations(relType, limit)
		if err != nil {
			if outputFormat == "json" {
				fmt.Println(cli.FormatJSON(false, nil, err.Error()))
				return nil
			}
			return err
		}
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, rels, ""))
			return nil
		}
		fmt.Printf("Relations: %d\n", len(rels))
		for _, r := range rels {
			fmt.Printf("  %s -[%s]-> %s\n", r.SourceID, r.Relation, r.TargetID)
		}

	default:
		return fmt.Errorf("unknown list type %q, use 'entities' or 'relations'", listType)
	}
	return nil
}
```

注意：需要修改 `init()` 中已有的 `ontologyCmd.AddCommand(ontologyQueryCmd, ontologyAddCmd)` 为 `ontologyCmd.AddCommand(ontologyQueryCmd, ontologyAddCmd, ontologyListCmd)`。

- [ ] **Step 5: 构建并验证**

```bash
go build -o sage-wiki ./cmd/sage-wiki/
./sage-wiki ontology list --type entities --project /Users/kellen/claude-workspace/wiki | head -10
./sage-wiki ontology list --type relations --limit 5 --project /Users/kellen/claude-workspace/wiki
./sage-wiki ontology list --type entities --format json --project /Users/kellen/claude-workspace/wiki | python3 -c "import sys,json; d=json.load(sys.stdin); print('ok:', d['ok'], 'count:', len(d['data']))"
```

- [ ] **Step 6: Commit**

```bash
git add internal/ontology/ontology.go internal/ontology/ontology_test.go cmd/sage-wiki/ontology_cmd.go
git commit -m "feat: add ontology list subcommand (entities + relations)"
```

---

### Task 5: hub add 覆盖提示 (#9)

**Files:**
- Modify: `internal/hub/hub.go:50-52`
- Modify: `internal/hub/hub_test.go`

- [ ] **Step 1: 修改 AddProject 返回是否已存在**

将 `hub.go` 中的 `AddProject` 改为：

```go
// AddProject adds or updates a project. Returns true if it overwrote an existing entry.
func (c *HubConfig) AddProject(name string, p Project) bool {
	_, existed := c.Projects[name]
	c.Projects[name] = p
	return existed
}
```

- [ ] **Step 2: 更新调用方（cmd/sage-wiki/hub.go）**

找到调用 `AddProject` 的地方，使用返回值输出提示。在 `cmd/sage-wiki/hub.go` 中：

```go
overwritten := hubCfg.AddProject(cfg.Project, hub.Project{...})
if overwritten {
	fmt.Fprintf(os.Stderr, "info: project %q already existed, overwriting\n", cfg.Project)
}
```

- [ ] **Step 3: 更新测试**

在 `internal/hub/hub_test.go` 添加：

```go
func TestAddProjectOverwrite(t *testing.T) {
	c := New()
	existed := c.AddProject("test", Project{Path: "/old"})
	if existed {
		t.Error("first add should return false")
	}
	existed = c.AddProject("test", Project{Path: "/new"})
	if !existed {
		t.Error("second add should return true")
	}
	if c.Projects["test"].Path != "/new" {
		t.Error("should be overwritten with new value")
	}
}
```

- [ ] **Step 4: 运行测试**

```bash
go test ./internal/hub/ -v
```

预期：全部 PASS

- [ ] **Step 5: Commit**

```bash
git add internal/hub/hub.go internal/hub/hub_test.go cmd/sage-wiki/hub.go
git commit -m "fix: show info message when hub add overwrites existing project"
```

---

### Task 6: README 补充 (#8)

**Files:**
- Modify: `README.md`

- [ ] **Step 1: 在 README.md 的 Commands 表格中补充 fork 新增命令**

找到 Commands section，在现有命令表格中追加：

```markdown
| `diff`       | Show pending source changes against manifest |
| `list`       | List wiki entities, concepts, or sources |
| `write`      | Write a summary or article |
| `ontology`   | Query, list, and manage the ontology graph |
| `hub`        | Multi-project hub commands (add/remove/search/status) |
| `learn`      | Store a learning entry |
| `capture`    | Capture knowledge from text |
| `add-source` | Register a source file in the manifest |
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: add fork-specific CLI commands to README"
```

---

### Task 7: 集成测试扩展 (#10)

**Files:**
- Modify: `integration_test.go`

- [ ] **Step 1: 在 TestIntegrationM1 后添加新子测试**

在 `integration_test.go` 的 `TestIntegrationM1` 函数内，在现有子测试（vectors）后追加：

```go
t.Run("lint", func(t *testing.T) {
	// lint 不需要 API，直接运行
	runner := linter.NewRunner()
	ctx := &linter.LintContext{
		ProjectDir: dir,
		OutputDir:  "wiki",
		DBPath:     filepath.Join(dir, ".sage", "wiki.db"),
	}
	results, err := runner.Run(ctx, "", false)
	if err != nil {
		t.Fatal(err)
	}
	// lint 应该能运行，结果可以有 findings
	t.Logf("lint findings: %d", len(results))
})

t.Run("hub_add_list", func(t *testing.T) {
	hubCfg := hub.New()
	existed := hubCfg.AddProject("test-project", hub.Project{
		Path: dir, Searchable: true,
	})
	if existed {
		t.Error("first add should return false")
	}
	projects := hubCfg.SearchableProjects()
	if len(projects) != 1 {
		t.Errorf("expected 1 searchable project, got %d", len(projects))
	}
})

t.Run("learn", func(t *testing.T) {
	memStore := memory.NewStore(db)
	err := memStore.AddLearning("test learning entry", []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	learnings, err := memStore.ListLearnings(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(learnings) == 0 {
		t.Error("expected at least 1 learning")
	}
})
```

注意：需要添加对应的 import（linter, hub, memory）。具体方法签名需在实施时确认。

- [ ] **Step 2: 运行集成测试**

```bash
go test -v -run Integration -timeout 60s .
```

预期：新增子测试 PASS

- [ ] **Step 3: Commit**

```bash
git add integration_test.go
git commit -m "test: expand integration tests with lint, hub, learn coverage"
```

---

### Task 8: 阶段 C — 重建 + 重编译 + 重嵌入 (#5, #6)

**Files:**
- No code changes, operational steps only

- [ ] **Step 1: 重建二进制**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
go build -o sage-wiki ./cmd/sage-wiki/
```

- [ ] **Step 2: 全量编译**

```bash
WIKI=/Users/kellen/claude-workspace/wiki
echo "y" | ./sage-wiki compile --fresh --project "$WIKI"
```

预期：所有源文件（不含 .DS_Store）编译成功

- [ ] **Step 3: 向量补齐 (#5)**

```bash
./sage-wiki compile --re-embed --project /Users/kellen/claude-workspace/wiki
```

预期：re-embedded N entries

- [ ] **Step 4: 验证混合搜索 (#6)**

```bash
./sage-wiki search "收购" --project /Users/kellen/claude-workspace/wiki -vv 2>&1 | head -20
```

检查 -vv 日志中是否出现 vector/embed 相关信息。如果搜索结果有 score > 0 说明混合搜索工作。

- [ ] **Step 5: 验证 FTS 和向量条目对齐**

```bash
WIKI=/Users/kellen/claude-workspace/wiki
sqlite3 "$WIKI/.sage/wiki.db" "SELECT COUNT(*) FROM entries;"
sqlite3 "$WIKI/.sage/wiki.db" "SELECT COUNT(*) FROM vec_entries;"
```

预期：两者数量相等或接近

---

### Task 9: 阶段 D — 回归验证

- [ ] **Step 1: 全套测试**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
go test ./... -timeout 120s
go vet ./...
```

预期：全部 PASS

- [ ] **Step 2: 关键健康检查项回归**

```bash
WIKI=/Users/kellen/claude-workspace/wiki

# MCP 配置已删除
python3 -c "import json; d=json.load(open('/Users/kellen/.claude.json')); print('MCP clean:', 'sage-wiki' not in d.get('mcpServers', {}))"

# .DS_Store 不再被编译
./sage-wiki diff --project "$WIKI" 2>&1

# JSON 错误信封
./sage-wiki search --format json --project "$WIKI" 2>&1 | python3 -c "import sys,json; d=json.load(sys.stdin); print('JSON envelope:', d.get('ok') == False)"

# ontology list
./sage-wiki ontology list --type entities --limit 5 --project "$WIKI"

# hub add 覆盖提示
./sage-wiki hub add "$WIKI" --project "$WIKI" 2>&1 | grep -i "already\|overwrite\|existed"
```

- [ ] **Step 3: 更新 health-report.md 标记已修复**

在 health-report.md 发现汇总表中，每个已修复项添加 ✅ 标记。

- [ ] **Step 4: Final commit**

```bash
git add health-report.md
git commit -m "docs: mark all health check findings as resolved"
```

---

## Task 依赖

```
Task 1 (数据清理)
  └── Task 2 (.DS_Store 过滤) ─┐
  └── Task 3 (JSON 错误信封)   │ 可并行
  └── Task 4 (ontology list)   │
  └── Task 5 (hub add 提示)    │
  └── Task 6 (README)          │
  └── Task 7 (集成测试)        ┘
        └── Task 8 (重建+重编译+重嵌入) ← 依赖 Task 1-7 全部完成
              └── Task 9 (回归验证)
```

Task 2-7 可并行执行（互不依赖）。Task 8 必须等所有代码改完后执行。
