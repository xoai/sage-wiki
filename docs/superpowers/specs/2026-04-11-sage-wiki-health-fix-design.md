# sage-wiki 健康检查问题修复设计文档

日期: 2026-04-11
分支: feature/chinese-localization
前置: health-report.md（健康检查报告）

## 目标

修复健康检查发现的全部 10 个问题，按依赖顺序分 4 个阶段执行。

## 修复清单

| # | 严重度 | 问题 | 修复方案 | 阶段 |
|---|--------|------|---------|------|
| 1 | HIGH | ~/.claude.json 仍有 sage-wiki MCP server 配置 | 删除 mcpServers.sage-wiki 条目 | A |
| 2 | HIGH | wiki/ 旧编译残留（537 概念，实际仅 31 属于当前源） | 清空 wiki/ 输出目录，重新编译 | A |
| 3 | MED | .DS_Store 被当作源文件编译 | 在 manifest 扫描层添加隐藏文件过滤 | B |
| 4 | MED | --format json 错误时输出纯文本 | 在 cobra root command 添加错误 JSON 包装 | B |
| 5 | MED | FTS 1263 vs 向量 1170，差 93 条 | 清理后 compile --fresh + --re-embed | C |
| 6 | MED | 混合搜索可能仅走 BM25 | 确认 embedding 在 compile 时生成，验证 -vv 日志 | C |
| 7 | LOW | ontology 无列表接口 | 添加 `ontology list` 子命令（entities/relations） | B |
| 8 | LOW | README 缺 fork 新增 8 个命令 | 补充 diff/list/write/ontology/hub/learn/capture/add-source | B |
| 9 | LOW | hub add 重复添加静默覆盖 | 添加 "already exists, overwriting" 提示信息 | B |
| 10 | LOW | 集成测试仅覆盖读操作 | 扩展测试覆盖 lint/hub/learn/capture/add-source | B |

## 执行阶段

### 阶段 A：数据清理（先清后建）
1. 删除 ~/.claude.json 中 sage-wiki MCP 配置（#1）
2. 清空 wiki/concepts/、wiki/summaries/（保留目录结构）
3. 删除 raw/ 下 .DS_Store 文件
4. 不重编译（等阶段 B 的 .DS_Store 过滤代码就位后再编译）

### 阶段 B：代码修改（4 个 Go 改动 + 1 文档 + 1 测试）
5. .DS_Store 过滤（#3）— 在 manifest 或 compiler 的源文件扫描层添加 `strings.HasPrefix(name, ".")` 过滤
6. JSON 错误信封（#4）— 在 cmd/sage-wiki/main.go 的 root command 添加 `SilenceErrors: true` + 自定义错误输出，当 --format json 时输出 `{"ok":false,"error":"..."}`
7. ontology list 子命令（#7）— 新增 `ontology list --type entities|relations` 子命令，查询 storage 层返回列表
8. hub add 重复提示（#9）— 在 hub.AddProject 中检查已存在时输出 info 消息
9. README 补充（#8）— 在 Commands 表格中添加 fork 新增的 8 个命令
10. 集成测试扩展（#10）— 在 integration_test.go 添加 lint/hub add/hub list/learn/capture/add-source 子测试

### 阶段 C：运维操作（依赖阶段 B 的新二进制）
11. 重新构建二进制 `go build`
12. 全量编译 `compile --fresh`（.DS_Store 过滤已生效）
13. 向量补齐 `compile --re-embed`（#5）
14. 混合搜索验证（#6）— `search -vv` 确认 vector 路径被激活

### 阶段 D：验证
15. 运行 `go test ./...` 确认所有测试通过
16. 运行 `go vet ./...` 确认无警告
17. 运行健康检查关键项回归验证
18. 更新 health-report.md 标记已修复项

## 文件影响清单

| 文件 | 操作 | 对应修复 |
|------|------|---------|
| ~/.claude.json | 删除条目 | #1 |
| ~/claude-workspace/wiki/wiki/concepts/*.md | 删除旧文件 | #2 |
| ~/claude-workspace/wiki/wiki/summaries/*.md | 删除旧文件 | #2 |
| ~/claude-workspace/wiki/raw/**/.DS_Store | 删除 | #3 前置 |
| internal/manifest/manifest.go（或 compiler/pipeline.go） | 添加隐藏文件过滤 | #3 |
| cmd/sage-wiki/main.go | SilenceErrors + JSON 错误包装 | #4 |
| cmd/sage-wiki/ontology_cmd.go | 新增 list 子命令 | #7 |
| internal/hub/hub.go | 添加覆盖提示 | #9 |
| README.md | 补充命令列表 | #8 |
| integration_test.go | 扩展子测试 | #10 |

## 风险与注意

- 阶段 A 清理 wiki/ 是破坏性操作，清理前确认当前数据已不再需要（用户已确认原文件删除、新文件放入 inbox）
- 阶段 C 的 compile --fresh 消耗 API 额度（上次 $0.35 / 12 源）
- #4 JSON 错误信封需要兼容 cobra 的错误处理机制，SilenceErrors=true 后所有命令错误需手动输出
- #7 ontology list 需要确认 storage 层是否有 ListEntities/ListRelations 方法，如果没有需要新增
- #10 集成测试扩展的 learn/capture 子测试需要确认 DB 写入逻辑
