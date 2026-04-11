# Upstream Incremental Merge Design（PR#25/#27/#28/#29 + eval 重组）

## 目标

将 upstream/main（0e773d5）合并到 fork branch feature/chinese-localization（9dd83de），覆盖 v0.1.3 后的 5 个 PR。以上游为准，补充 fork 增量。

## 上游变化（14 commits, 22 files, +1139 lines）

| PR | 内容 | 风险 |
|----|------|------|
| #25（我们的） | minChunkTokenBudget 500 + stripThinkTags fallback | LOW — 代码已在 fork，可能产生"identical change"自动合并 |
| #27 | fix isIgnored nested paths + scanSnapshot 统一 | LOW — diff.go/watch.go 我们只改了 .DS_Store 过滤，不同区域 |
| #28 | Chinese README（README_zh.md） | NONE — 新文件 |
| #29 | **language config** — `Language` field + prompts.Render 签名变更 | **MED** — Render(name, data) → Render(name, data, language)，所有调用者需第三参数 |
| — | eval → eval/ 目录重组 | NONE — 文件移动 |

## 关键变化：PR#29 language config

- `config.go`: 新增 `Language string` 字段
- `prompts.Render` 签名: `Render(name, data)` → `Render(name, data, language)`
- 语言注入: 非 JSON 模板末尾自动追加 "Write your entire response in {language}"
- JSON 模板检测: 含 "Output ONLY a JSON" / "Return ONLY a JSON" 则跳过注入

### 对 fork 的影响

上游已更新所有 Render 调用者（summarize.go, write.go, concepts.go, pipeline.go, tools_write.go），merge 会自动带入。fork 无额外 Render 调用需手动修改。

### config.yaml 需要更新

合并后需在 `~/claude-workspace/wiki/config.yaml` 添加：
```yaml
language: zh
```
这样所有 prompts 会自动追加中文输出指令，不需要修改 prompts/ 覆盖模板。

## 合并步骤

1. `git merge upstream/main`
2. 解决冲突（预期 0-2 个，大部分自动合并）
3. `go build` + `go test`
4. `config.yaml` 添加 `language: zh`
5. 验证 prompts 兼容性（确认覆盖模板不含 JSON 检测关键词冲突）

## 不做的事

- 不重新编译 wiki（底座更新独立于数据编译）
- 不修改 prompts/ 覆盖模板（language 注入是自动追加，不改模板内容）
