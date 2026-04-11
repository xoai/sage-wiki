# Upstream v0.1.3 Merge Design

## 目标

将 xoai/sage-wiki upstream/main（v0.1.3，commit 1cc1d4b）合并到 fork branch `feature/chinese-localization`（commit 42c92c7）。以上游为准，补充 fork 增加的内容。

## 上游 v0.1.3 新增内容

两个 feature commit（40 文件，+4176 行）：

1. **c1f1a3d — 增强搜索**
   - chunk-level indexing（文章切 ~800 token chunk，各有 FTS5 + vector 条目）
   - LLM query expansion（关键词改写 + 语义改写 + 假设答案）
   - LLM re-ranking（top 15 候选 LLM 打分，position-aware blending）
   - BM25-prefiltered vector search（限制 cosine 计算在 ~250 chunks 内）

2. **1cc1d4b — 图增强检索**
   - graph-based context expansion（本体图谱关系扩展搜索结果）
   - source provenance（source↔article 映射追踪）
   - cascade awareness（删源时识别受影响文章）
   - `provenance` CLI 命令 + `--prune` compile flag

6 个新包：`internal/graph/`、`internal/search/`、`internal/memory/chunks.go`、`internal/compiler/backfill.go`、`internal/manifest/manifest_test.go`、`docs/guides/search-quality.md`

## 冲突文件分析

Merge base: `364287e`（v0.1.2 CHANGELOG）

| 文件 | 冲突风险 | fork 修改 | upstream 修改 | 解决策略 |
|------|---------|----------|--------------|---------|
| `cmd/sage-wiki/main.go` | HIGH | +outputFormat, SilenceErrors, JSON envelope, --format flag, AddCommand +8 commands | +provenanceCmd, --prune flag, AddCommand +provenance | 取 upstream 为基础，补入 fork 的 outputFormat/SilenceErrors/JSON/--format/8 commands |
| `internal/ontology/ontology.go` | HIGH | +ListRelations (line 347+) | +EntityDegree/EntitiesCiting/CitedBy (line 347+) | 取 upstream 三个方法，追加 fork 的 ListRelations |
| `README.md` | MED | Commands 表 +8 fork commands | +provenance command, compile +--prune, +Search Quality 章节 | 取 upstream 版本，补入 fork 的 8 个命令行 |
| `config.go` | LOW | Config struct +Extends field, Load() +extends 继承逻辑 | SearchConfig +12 fields + getter methods | 预期自动合并（不同代码区域）|

## 非冲突文件（36 个）

全部为 upstream-only 变更，自动合并：
- 新包：graph/, search/, memory/chunks.go 等
- 已有文件扩展：compiler/pipeline.go, query/query.go, vectors/store.go, storage/db.go 等
- Web UI 更新：dist/ assets, GraphView.tsx, Sidebar.tsx
- 文档：CHANGELOG.md, search-quality.md

## Rule 2 检查项

### config.go → config.yaml
上游新增 search 配置字段（query_expansion, rerank, chunk_size, graph_expansion 等），全部有合理默认值（true/800/10/2/8000 等）。config.yaml 无需显式添加，除非要禁用某功能。

### write.go → prompts/ 模板
上游 write.go 新增 `ArticleWriteOpts` struct（含 ChunkStore, ChunkSize），重构了 WriteArticles 签名。需检查：
- fork 的 prompts/ 覆盖模板是否引用了 WriteArticles 的参数
- 如模板只用 prompt 文本变量（非 Go 函数参数），则不受影响

## 合并步骤

1. `git merge upstream/main`
2. 解决 3-4 个冲突文件（按上表策略）
3. `go build -o sage-wiki ./cmd/sage-wiki/` — 编译验证
4. `go test ./...` — 测试验证
5. `./sage-wiki --help` — 确认 21 个命令（20 fork + provenance）
6. 检查 prompts/ 模板兼容性

## 不做的事

- 不修改 config.yaml search 参数（默认值已最优）
- 不重新编译 wiki 数据（留给 wiki-improve 日常维护）
- 不改动 fork 已有的 hub/diff/list/write 等 CLI 命令代码
- 不做 rebase（会破坏 PR #18/#25 的 commit 引用）
