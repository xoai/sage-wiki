# PR Review 修复 + wiki-manage PR Review 工作流

> 状态：brainstorm 完成，待 writing-plans
> 日期：2026-04-12

## 1. 概述

处理 upstream owner 对已提交 PR 的 review 反馈（5 项代码修复 + 1 项回复），同时在 wiki-manage skill 的工作流 4 中增加 PR review 能力。

## 2. PR #18 修复（3 项必修）

### C1 安全：pdftotext 路径注入

**问题**：`readHeadPDFToText` 将文件路径直接传给 `exec.Command`。虽然 Go 不走 shell，但以 `-` 开头的文件名会被 pdftotext 解释为 flag。

**修复**：`readhead.go:44` 在 path 参数前加 `"--"`

```go
// Before
exec.Command(pdftotext, "-l", "1", path, "-")
// After
exec.Command(pdftotext, "-l", "1", "--", path, "-")
```

### M1 性能：ReadHead 无条件执行

**问题**：`ReadHead(path, 500)` 在每个源文件上执行（含 PDF subprocess），即使没配 TypeSignals。500 个文件 = 500 次无意义 I/O。

**修复**：3 处调用点加短路判断——TypeSignals 为空时传 `""` 跳过 ReadHead。

涉及文件：
- `internal/compiler/diff.go:75`
- `internal/compiler/pipeline.go:909`
- `internal/wiki/ingest.go:114`

### M3 验证：TypeSignal 无校验

**问题**：空 Type、无 keyword、content keywords 配了但 MinContentHits=0 都会静默产生错误数据。

**修复**：`config.go` 的 `Validate()` 中加 TypeSignal 校验：
- Type 非空
- 至少一个 keyword（filename 或 content）
- 有 ContentKeywords 时 MinContentHits > 0

### M2 架构（无代码改动）

**现状**：extract.TypeSignal 在 extract.go 独立定义，config.TypeSignal 在 config.go 独立定义，compiler 通过 convertSignals() 桥接。extract 不 import config——依赖方向正确。

**行动**：回复 owner 说明此问题在后续 commit 中已解决。

## 3. Follow-up 修复（2 项可选但一起做）

### F1 PR #25：stripThinkTags fallback log.Warn

owner 建议：fallback 激活时（strip 产生空结果，提取 think 内容代替）加 `log.Warn`，帮用户发现低质量输出。

**修复**：`internal/compiler/summarize.go` 或 `internal/llm/client.go` 中 stripThinkTags 函数，fallback 分支加 `log.Warn`。

### F2 PR #9：MCP server 日志一致性

owner 建议：MCP server 中 embed 失败用 `fmt.Fprintf(os.Stderr, ...)` 应改为 `log.Warn(...)`。

**修复**：`internal/mcp/server.go` 对应位置替换。

## 4. wiki-manage 工作流 4 扩展

**现状**：工作流 4"更新底座"只做 fetch upstream + merge + build + test。

**扩展为"上游协作"**：
- Step A: fetch upstream + merge（现有）
- Step B: `gh pr list` 检查 open PR 的 owner 回复
- Step C: 对有新回复的 PR，读取 review 意见，列出待处理项
- Step D: 逐个修复，修复后 push 更新 PR

**CLI 命令**：
```bash
gh pr list --repo xoai/sage-wiki --state open --author kailunguu-code --json number,title,comments
gh api repos/xoai/sage-wiki/issues/{n}/comments
```

## 5. 常量提取（Minor）

owner 提到 500 rune limit 出现在 4 处。提取为 `readhead.go` 中的命名常量 `DefaultHeadRunes = 500`。

## 6. 文件变更清单

| 文件 | 改动 |
|------|------|
| `internal/extract/readhead.go` | C1 + 常量 |
| `internal/compiler/diff.go` | M1 短路 |
| `internal/compiler/pipeline.go` | M1 短路 |
| `internal/wiki/ingest.go` | M1 短路 |
| `internal/config/config.go` | M3 校验 |
| `internal/llm/client.go` 或 `summarize.go` | F1 log.Warn |
| `internal/mcp/server.go` | F2 log.Warn |
| `skills/wiki-manage/SKILL.md` | 工作流 4 扩展 |
