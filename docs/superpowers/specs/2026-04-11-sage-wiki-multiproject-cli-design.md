# sage-wiki 多项目支持：CLI-first 架构设计

## 目标

将 sage-wiki 从 MCP-dependent 模式迁移到 CLI-first 模式，同时实现多项目支持（联邦搜索、config 继承、hub 路由），通过 Claude Code skill 封装保持等同于 MCP 的使用体验。

## 背景

### 现状问题
- sage-wiki 通过 MCP server（`sage-wiki serve`）向 Claude Code 暴露工具
- MCP 长驻进程可靠性差（崩溃/卡死/stdio 缓冲问题）
- 多项目时工具名爆炸（N 项目 × 15 工具），3-4 个项目即触及 Claude Code deferred tools 阈值
- 新增项目需修改 .claude.json 并重启 Claude Code
- 7 个 MCP-only 功能缺少 CLI 等价命令

### 架构决策过程
经 autoresearch:scenario 25 轮迭代分析（15 轮架构选择 + 10 轮实现验证），评估了 4 种架构：
- A（多 MCP 实例）：工具爆炸硬上限，3-4 项目即降级
- B（config 继承）：解决配置重复，但不解决 MCP 问题
- C（单进程 workspace）：故障隔离差，上游维护成本高
- D（hub 聚合）：综合最优，但 MCP 层本身不可靠

**最终选择：CLI-first + Hub CLI 子命令 + Config 继承 + Skill 封装**

核心洞察：sage-wiki 已有完整 CLI 命令（compile/search/lint/status），MCP server 只是包了一层 JSON-RPC。去掉这层包装，用 Bash 工具直接调 CLI，消除所有 MCP 问题。

## 架构

```
Claude Code 会话
    │
    ▼ (Skill 或直接 Bash)
sage-wiki CLI (Go 二进制，调完即退)
    ├── 单项目命令: search/compile/lint/status/diff/list/write/ontology/learn/capture
    ├── Hub 子命令: hub search/status/compile/list/add/remove
    └── 所有命令支持 --format json
         │
         ▼
    项目数据 (完全隔离)
    ├── ~/wiki/ (主知识库)
    ├── ~/projects/DealX/ (项目库)
    └── ~/projects/MacroRes/ (主题库)
```

## 交付件

### 交付件 1: CLI 命令补齐

补齐 7 个 MCP-only 功能的 CLI 等价命令。

#### P0 — 核心工作流必需

**`sage-wiki diff`** — 显示待编译文件（替代 wiki_compile_diff）
```bash
sage-wiki diff --project ~/wiki/ --format json
# stdout: {"ok":true,"data":{"pending":["raw/regulation/xxx.pdf"],"count":5,"modified":2,"new":3}}
```

**`sage-wiki list`** — 列出 wiki 内容（替代 wiki_list）
```bash
sage-wiki list --project ~/wiki/ --type concepts --format json
sage-wiki list --project ~/wiki/ --type summaries
sage-wiki list --project ~/wiki/ --type articles
# --type: concepts | summaries | articles | sources
```

**`sage-wiki ontology`** — 查询和管理关系图谱（合并 wiki_ontology_query + wiki_add_ontology）
```bash
sage-wiki ontology query --entity "注册制" --direction outbound --depth 2 --project ~/wiki/
sage-wiki ontology add --from "注册制" --to "IPO" --relation "regulates" --project ~/wiki/
```

#### P1 — 编译管线完整性

**`sage-wiki write`** — 手动触发摘要/文章生成（替代 wiki_write_summary + wiki_write_article）
```bash
sage-wiki write summary --source "raw/regulation/xxx.pdf" --project ~/wiki/
sage-wiki write article --concept "注册制" --project ~/wiki/
```

#### P2 — 辅助功能

**`sage-wiki learn`** — 添加知识笔记（替代 wiki_learn）
```bash
sage-wiki learn "注册制改革的核心变化是从核准制转向注册制" --project ~/wiki/
```

**`sage-wiki capture`** — 快速捕获（替代 wiki_capture）
```bash
sage-wiki capture --url "https://..." --project ~/wiki/
sage-wiki capture --text "..." --project ~/wiki/
```

#### 不需要 CLI 化的
- `wiki_read` — Read 工具直接读文件
- `wiki_commit` — `git commit` 原生够用

#### 实现位置
`cmd/sage-wiki/main.go` — 新增 cobra 子命令，内部复用 `internal/` 包的现有逻辑。

### 交付件 2: --format json

所有 CLI 命令统一支持 `--format` flag。

**Flag 定义：**
```
--format text   (默认，人类可读)
--format json   (机器解析，skill 内部用)
```

**JSON 输出规范：**
```json
// 成功
{"ok": true, "data": {...}}

// 失败（stderr 也输出，但 stdout 保证 JSON）
{"ok": false, "error": "message", "code": 1}

// 部分成功
{"ok": true, "data": {"compiled": 27, "failed": 3, "failures": ["file1.pdf", "file2.pdf"]}}
```

**退出码：**
- 0: 成功
- 1: 错误（配置无效、文件不存在等）
- 2: 部分成功（编译部分失败）
- 3: API 错误（rate limit、认证失败）

**实现：** `rootCmd.PersistentFlags().StringVar(&outputFormat, "format", "text", "Output format: text or json")`。各命令的输出函数检查 format 决定文本还是 JSON。

### 交付件 3: Config extends

config.yaml 支持 `extends` 字段继承基础配置。

**语法：**
```yaml
# ~/projects/DealX/config.yaml
extends: ../../sage-base.yaml    # 相对于本 config 文件的路径

project: deal-x                  # 覆盖 base
sources:
  - path: raw
    type: auto
# API key、models、ontology 等从 base 继承
```

**加载逻辑（config.go，约 60 行改动）：**
1. 读项目 config，检查 `extends` 字段
2. 有 extends → 解析为相对于 config 文件的路径，读 base config
3. YAML unmarshal：先 base，再项目 config 覆盖（非零值覆盖）
4. base 文件不存在 → log warning，继续用项目 config（不报错）
5. 最多一层继承（base 的 extends 被忽略，防止循环）
6. `Validate()` 在合并后执行

**sage-base.yaml 示例：**
```yaml
version: 1
api:
  provider: openai-compatible
  api_key: ${MINIMAX_API_KEY}
  base_url: https://api.minimax.chat/v1
  rate_limit: 480
models:
  summarize: MiniMax-M2.5-highspeed
  extract: MiniMax-M2.5-highspeed
  write: MiniMax-M2.5-highspeed
embed:
  provider: openai-compatible
  model: acge_text_embedding
  api_key: ${SILICONFLOW_API_KEY}
  base_url: https://api.siliconflow.cn/v1
compiler:
  summary_max_tokens: 8000
  max_parallel: 8
ontology:
  relations:
    - name: amends
      synonyms: [修订, 修改]
    - name: regulates
      synonyms: [规范, 监管]
    - name: supplies
      synonyms: [供应, 提供]
    - name: competes_with
      synonyms: [竞争, 对标]
```

### 交付件 4: Hub 子命令

多项目路由、联邦搜索、项目管理。

**命令：**
```bash
# 项目管理
sage-wiki hub init                          # 创建 ~/.sage-hub.yaml
sage-wiki hub add ~/projects/DealX          # 注册项目
sage-wiki hub remove deal-x                 # 取消注册
sage-wiki hub list                          # 列出所有项目

# 联邦操作
sage-wiki hub search "注册制"               # 搜索所有 searchable 项目
sage-wiki hub search "注册制" -p main       # 只搜指定项目
sage-wiki hub status                        # 所有项目状态汇总
sage-wiki hub compile deal-x                # 编译指定项目
sage-wiki hub compile --all                 # 顺序编译所有项目
```

**配置文件 ~/.sage-hub.yaml：**
```yaml
projects:
  main:
    path: /Users/kellen/claude-workspace/wiki
    description: "法规/研报/纪要通用知识库"
    searchable: true
  deal-x:
    path: /Users/kellen/projects/DealX
    description: "XX并购项目"
    searchable: false        # 机密，不参与联邦搜索
  macro:
    path: /Users/kellen/projects/MacroResearch
    description: "宏观研究"
    searchable: true
```

**联邦搜索实现：**
1. 读 hub config，过滤 `searchable: true`（除非 `-p` 指定）
2. 并行打开各项目 DB（goroutine），执行 BM25 + vector 搜索
3. RRF（Reciprocal Rank Fusion）合并排序
4. 每条结果标注来源项目
5. JSON 输出带 `project` 字段

**`hub add` 自动检测：**
1. 检查目标路径有 config.yaml → 注册
2. 无 config.yaml → 提示用户先 `sage-wiki init`
3. 从 config.yaml 读 `project` 名作为注册 key
4. 自动设 `searchable: true`（用户可后续编辑改 false）

**实现位置：** 新文件 `cmd/sage-wiki/hub.go`。内部复用 `internal/storage`、`internal/hybrid`、`internal/memory`、`internal/vectors` 包。

**上游 PR 策略：** 先在 fork 实现验证，成熟后提 PR。

### 交付件 5: wiki skill

一个通用 skill `/wiki`，替代所有 MCP 工具调用。

**用法：**
```
/wiki search 注册制
/wiki search 注册制 --project main
/wiki status
/wiki compile deal-x
/wiki diff
/wiki lint --fix
```

**Skill 内部逻辑：**
- 路由表映射用户命令到 sage-wiki CLI 调用
- 所有 CLI 调用自动加 `--format json`
- 解析 JSON 输出，格式化为用户友好的展示
- 不指定 `--project` 时：搜索用 hub（全部），编译/lint 用默认项目
- hub 配置路径：`~/.sage-hub.yaml`
- 项目路径通过 `sage-wiki hub list --format json` 动态获取
- sage-wiki 二进制路径：`/Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki/sage-wiki`

**部署位置：** `~/.claude/skills/wiki/SKILL.md`

### 交付件 6: wiki-improve 迁移

将 wiki-improve skill 中的 MCP 调用替换为 CLI 调用。

**替换映射：**

| MCP 调用 | CLI 替换 |
|----------|---------|
| `mcp__sage-wiki__wiki_compile_diff` | `sage-wiki diff --project ~/wiki/ --format json` |
| `mcp__sage-wiki__wiki_status` | `sage-wiki status --project ~/wiki/ --format json` |
| `mcp__sage-wiki__wiki_search` | `sage-wiki search "query" --project ~/wiki/ --format json` |
| `mcp__sage-wiki__wiki_lint` | `sage-wiki lint --project ~/wiki/ --format json` |
| `mcp__sage-wiki__wiki_lint --fix` | `sage-wiki lint --fix --project ~/wiki/` |
| `mcp__sage-wiki__wiki_compile` | `echo y \| sage-wiki compile --project ~/wiki/` |
| `mcp__sage-wiki__wiki_add_ontology` | `sage-wiki ontology add ... --project ~/wiki/` |
| `mcp__sage-wiki__wiki_ontology_query` | `sage-wiki ontology query ... --project ~/wiki/ --format json` |
| `mcp__sage-wiki__wiki_write_article` | `sage-wiki write article ... --project ~/wiki/` |

**wiki-improve 不需要 hub** — 始终操作单个 wiki 项目，路径硬编码在 skill 中。

## 实施顺序

```
Phase 1 (上游 PR):
  ① CLI 命令补齐 (6 个新命令)
  ② --format json (所有命令)
  ③ Config extends (config.go ~60 行)

Phase 2 (fork):
  ④ Hub 子命令 (hub.go ~300 行)
  集成测试

Phase 3 (本地):
  ⑤ wiki skill (新 skill)
  ⑥ wiki-improve 迁移 (更新现有 skill)

Phase 4 (清理):
  从 .claude.json 移除 sage-wiki MCP 条目
  验证所有工作流正常
```

## 性能预期

| 操作 | MCP（现在） | CLI（迁移后） | 差异 |
|------|-----------|-------------|------|
| 单次搜索 | ~5-50ms | ~50-150ms | +100ms，无感 |
| 联邦搜索 (3库) | N/A | ~100-200ms | 新能力 |
| 编译 | 相同 | 相同 | LLM API 是瓶颈 |
| 进程启动 | ~20ms (已运行) | ~20-50ms | 无感 |

## 迁移前后对比

| 维度 | MCP 模式（现在） | CLI 模式（迁移后） |
|------|----------------|-----------------|
| 可靠性 | 长驻进程，可能崩溃/卡死 | 调完即退，每次独立 |
| 多项目扩展 | N×15 工具爆炸 | 固定 1 个 Bash 工具 |
| 新增项目 | 改 .claude.json + 重启 | hub add + 立即可用 |
| 联邦搜索 | 不支持 | hub search |
| 配置管理 | 每项目完整复制 | extends 继承 |
| 调试 | 难（stdio 管道） | 简单（终端直接跑） |
| 上游兼容 | 保留 serve 命令 | 零破坏性变更 |
