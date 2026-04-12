# PR Review 修复实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 upstream owner 对 PR #18/#25/#9 的 review 反馈（5 项代码修复 + 常量提取），扩展 wiki-manage 工作流 4，回复 M2

**Architecture:** 逐文件修复，每个 review 项独立 commit。wiki-manage SKILL.md 扩展工作流 4 为"上游协作"。

**Tech Stack:** Go 1.22+ / Claude Code SKILL.md / gh CLI

**Spec:** `docs/superpowers/specs/2026-04-12-pr-review-fixes-design.md`

---

## Task 1: C1 安全 + 常量提取（readhead.go）

**Files:**
- Modify: `internal/extract/readhead.go:44` (加 "--")
- Modify: `internal/extract/readhead.go:17` (常量提取)

- [ ] **Step 1: 修复 pdftotext 路径注入**

在 `readhead.go:44` 的 `exec.Command` 调用中，path 参数前加 `"--"`：

```go
// readhead.go:44 改为：
out, err := exec.Command(pdftotext, "-l", "1", "--", path, "-").Output()
```

- [ ] **Step 2: 提取 500 rune 常量**

在 `readhead.go` 文件顶部（import 之后）加常量：

```go
// DefaultHeadRunes is the default number of runes to read for content-based type detection.
const DefaultHeadRunes = 500
```

- [ ] **Step 3: 构建验证**

```bash
cd ~/claude-workspace/projects/KarpathyWiki/sage-wiki
go build ./internal/extract/
```

- [ ] **Step 4: Commit**

```bash
git add internal/extract/readhead.go
git commit -m "fix: add -- before path in pdftotext command and extract head runes constant

C1 security fix: prevents filenames starting with - from being
interpreted as pdftotext flags.
Minor: extract DefaultHeadRunes = 500 constant."
```

## Task 2: M1 性能（ReadHead 短路）

**Files:**
- Modify: `internal/compiler/diff.go:75`
- Modify: `internal/compiler/pipeline.go:908-910`
- Modify: `internal/wiki/ingest.go:114`

- [ ] **Step 1: diff.go 短路**

`diff.go:75` 改为：

```go
var contentHead string
if len(cfg.TypeSignals) > 0 {
    contentHead = extract.ReadHead(path, extract.DefaultHeadRunes)
}
current[relPath] = SourceInfo{
    Path: relPath,
    Hash: hash,
    Type: extract.DetectSourceTypeWithSignals(path, contentHead, convertSignals(cfg.TypeSignals)),
    Size: info.Size(),
}
```

- [ ] **Step 2: pipeline.go extractType 短路**

`pipeline.go:908-910` 的 `extractType` 函数改为：

```go
func extractType(path string, typeSignals []config.TypeSignal) string {
	var contentHead string
	if len(typeSignals) > 0 {
		contentHead = extract.ReadHead(path, extract.DefaultHeadRunes)
	}
	return extract.DetectSourceTypeWithSignals(path, contentHead, convertSignals(typeSignals))
}
```

- [ ] **Step 3: ingest.go 短路**

`ingest.go:114` 改为：

```go
var contentHead string
if len(cfg.TypeSignals) > 0 {
    contentHead = extract.ReadHead(absPath, extract.DefaultHeadRunes)
}
signals := make([]extract.TypeSignal, len(cfg.TypeSignals))
```

- [ ] **Step 4: 构建验证**

```bash
go build ./...
go test ./internal/compiler/ ./internal/wiki/ -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/compiler/diff.go internal/compiler/pipeline.go internal/wiki/ingest.go
git commit -m "perf: skip ReadHead when no TypeSignals configured

M1 fix: avoids file I/O and potential pdftotext subprocess for
every source file when type_signals is empty in config."
```

## Task 3: M3 验证（TypeSignal 校验）

**Files:**
- Modify: `internal/config/config.go:400` (Validate 函数末尾)

- [ ] **Step 1: 加 TypeSignal 校验**

在 `config.go` Validate() 的 `return nil` 之前（约 line 411），插入：

```go
for i, ts := range c.TypeSignals {
    if ts.Type == "" {
        return fmt.Errorf("config: type_signals[%d]: type is required", i)
    }
    if len(ts.FilenameKeywords) == 0 && len(ts.ContentKeywords) == 0 && ts.Pattern == "" {
        return fmt.Errorf("config: type_signals[%d] (%s): at least one keyword (filename, content, or pattern) is required", i, ts.Type)
    }
    if len(ts.ContentKeywords) > 0 && ts.MinContentHits <= 0 {
        return fmt.Errorf("config: type_signals[%d] (%s): min_content_hits must be > 0 when content_keywords is set", i, ts.Type)
    }
}
```

- [ ] **Step 2: 构建 + 测试**

```bash
go build ./internal/config/
go test ./internal/config/ -count=1
```

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "fix: validate TypeSignal fields in config

M3 fix: rejects empty Type, missing keywords, and
content_keywords without min_content_hits."
```

## Task 4: F1 stripThinkTags fallback log.Warn

**Files:**
- Modify: `internal/llm/client.go:266-269`

- [ ] **Step 1: 加 log.Warn**

`client.go:266-269` fallback 分支改为：

```go
// Fallback: extract content from inside first think block
if m := thinkContentRe.FindStringSubmatch(s); len(m) > 1 {
    log.Warn("stripThinkTags fallback: model put all content inside <think> tags, extracting think content as summary (quality may be degraded)")
    return strings.TrimSpace(m[1])
}
```

确认 import 中有 `"github.com/xoai/sage-wiki/internal/log"`。

- [ ] **Step 2: 构建验证**

```bash
go build ./internal/llm/
```

- [ ] **Step 3: Commit**

```bash
git add internal/llm/client.go
git commit -m "fix: add log.Warn when stripThinkTags fallback activates

PR #25 follow-up: warn users when think content is used as summary
fallback, indicating potentially degraded output quality."
```

## Task 5: F2 MCP server 日志一致性

**Files:**
- Modify: `internal/mcp/server.go:229`

- [ ] **Step 1: 替换 fmt.Fprintf 为 log.Warn**

`server.go:229` 改为：

```go
log.Warn("search embed failed, falling back to BM25-only", "error", embedErr)
```

确认 import 中有 `"github.com/xoai/sage-wiki/internal/log"`，移除 `"fmt"` 和 `"os"` 如果不再使用。

- [ ] **Step 2: 构建验证**

```bash
go build ./internal/mcp/
```

- [ ] **Step 3: Commit**

```bash
git add internal/mcp/server.go
git commit -m "fix: use log.Warn instead of fmt.Fprintf for embed error in MCP

PR #9 follow-up: align with codebase logging convention."
```

## Task 6: wiki-manage 工作流 4 扩展

**Files:**
- Modify: `skills/wiki-manage/SKILL.md`
- Modify: `~/.claude/skills/wiki-manage/SKILL.md`（部署）

- [ ] **Step 1: 修改 SKILL.md 工作流 4**

将"工作流 4: 更新底座"改为"工作流 4: 上游协作"，在现有 Step 0 之后增加 Step B/C/D：

```markdown
## 工作流 4: 上游协作

更新底座 + 检查 PR review + 修复反馈。

### Step A: 底座更新检查

（现有 Step 0 内容不变：fetch upstream、合并、构建、测试）

### Step B: PR Review 检查

检查 open PR 的 owner 回复：

\`\`\`bash
gh pr list --repo xoai/sage-wiki --state open --author kailunguu-code --json number,title,comments --jq '.[] | select(.comments | length > 0) | "\(.number) \(.title) (\(.comments | length) comments)"'
\`\`\`

如果有未处理的回复，逐个读取：

\`\`\`bash
gh api repos/xoai/sage-wiki/issues/{number}/comments --jq '.[] | "[\(.user.login)] \(.created_at[:10]):\n\(.body)\n---"'
\`\`\`

### Step C: Review 意见分类

对每个 PR 的 review 意见，按以下分类：

| 类型 | 标记 | 行动 |
|------|------|------|
| Critical (安全/正确性) | C | 必须修复，阻塞合并 |
| Major (架构/性能) | M | 应该修复，除非有充分理由 |
| Minor (风格/命名) | m | 修复或回复说明 |
| Question (澄清) | Q | 回复说明 |

展示分类表，确认修复范围。

### Step D: 逐个修复 + 推送

对确认要修复的项：
1. 按优先级逐个修复（C → M → m）
2. 每项修复独立 commit
3. 全部完成后 push 更新 PR
4. 回复 PR comment 说明已修复

\`\`\`bash
git push origin <branch>
gh pr comment <number> --repo xoai/sage-wiki --body "Addressed review feedback: ..."
\`\`\`
```

- [ ] **Step 2: 更新入口菜单**

将菜单项 4 从"更新底座"改为"上游协作"：

```
4. 上游协作 — 检查并合并上游更新 + 检查 PR review + 修复反馈
```

- [ ] **Step 3: 部署到本地**

```bash
cp ~/claude-workspace/projects/KarpathyWiki/sage-wiki/skills/wiki-manage/SKILL.md ~/.claude/skills/wiki-manage/SKILL.md
```

- [ ] **Step 4: Commit**

```bash
git add skills/wiki-manage/SKILL.md
git commit -m "feat: expand wiki-manage workflow 4 with PR review capability

Renamed 'update base' to 'upstream collaboration'. Adds PR review
check, comment classification (C/M/m/Q), and fix-then-push flow."
```

## Task 7: 全量验证 + Push

- [ ] **Step 1: 全量构建 + 测试**

```bash
cd ~/claude-workspace/projects/KarpathyWiki/sage-wiki
go build -o sage-wiki ./cmd/sage-wiki/
go test ./... -count=1
go vet ./...
```

- [ ] **Step 2: Push 更新 PR #18 和 PR #36**

```bash
git push origin feature/chinese-localization
```

- [ ] **Step 3: 回复 PR #18 M2**

```bash
gh pr comment 18 --repo xoai/sage-wiki --body "Addressed all review feedback:

- **C1 (security)**: Added \`--\` before path in pdftotext exec.Command
- **M1 (performance)**: Short-circuit ReadHead when TypeSignals is empty (3 call sites)
- **M2 (architecture)**: Already resolved — extract.TypeSignal is independently defined in extract.go, config.TypeSignal in config.go, bridged by convertSignals() in compiler. extract does not import config.
- **M3 (validation)**: Added TypeSignal validation in Validate(): non-empty Type, at least one keyword, positive MinContentHits when content_keywords set
- **Minor**: Extracted DefaultHeadRunes = 500 constant"
```

## Task 8: Spec 文档 commit

- [ ] **Step 1: Commit spec + plan**

```bash
git add docs/superpowers/specs/2026-04-12-pr-review-fixes-design.md docs/superpowers/plans/2026-04-12-pr-review-fixes-plan.md
git commit -m "docs: add PR review fixes spec and plan"
```
