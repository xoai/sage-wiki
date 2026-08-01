[English](../../README.md) | **中文** | [日本語](README_ja.md) | [한국어](README_ko.md) | [Tiếng Việt](README_vi.md) | [Français](README_fr.md) | [Русский](README_ru.md)

<!-- translations: may-lag -->
> ⚠️ 本翻译可能滞后于 README.md — 以英文版为准。

# sage-wiki

**sage-wiki** 是一个由 AI Agent 与人类共同构建、共同查询的图记忆与知识库。放入文档，LLM 编译器就会将它们变成一个带知识图谱的互联 wiki —— Agent 通过 MCP 查询，人类以纯 markdown 浏览。启用可选的图谱处理阶段（graph passes）后，它会成为一个*带证据*的图谱：类型化实体、带溯源的关系、已消解的别名，以及答案中逐条事实的引用。一个 Go 二进制文件即可从个人 vault 扩展到团队 hub，再到公司级知识图谱。

**→ 立即开始：[安装](#安装) · [快速开始](#快速开始)**

从 [Andrej Karpathy 的想法](https://x.com/karpathy/status/2039805659525644595)——LLM 编译型个人知识库——生长而来，使用 [Sage Framework](https://github.com/xoai/sage) 构建。一路走来的一些经验总结见[这里](https://x.com/xoai/status/2040936964799795503)。

- **带引用的图记忆。** 通过 `wiki_graph_query` 提出关系型问题——答案仅以序列化的图边为依据；启用带证据的图谱后，每条引用都携带其来源文档与置信度。
- **为 Agent 与人类而建。** 19 个 MCP 工具加上生成的技能文件，教会 Agent 何时搜索、捕获与编译；人类则在同一份数据之上获得 Obsidian 原生的 markdown、TUI 和 Web UI。
- **信任与溯源。** 查询输出在通过验证前处于隔离状态；每条带证据的关系都记录了是哪个文档断言了它。
- **输入源文件，输出 wiki。** 编译管线读取论文、笔记、代码和邮件；进行摘要、提取概念，并写出互相关联的文章——它是上述一切能力的摄取层。每个新源文件都会丰富已有文章；wiki 随着增长不断复利积累。
- **向你的 wiki 提问。** chunk 级混合搜索配合 LLM 查询扩展、重排序与图感知的上下文组装，返回带引用的答案。
- **可扩展至 100K+ 文档。** 分层编译快速索引一切，只在真正重要之处花费 LLM 预算。

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

_外圈边界上的点代表知识库中所有文档的摘要，内圈的点代表从知识库中提取的概念，连线展示了这些概念之间的关联。_

## 从个人 vault 到公司知识图谱

- **个人** —— 在已有 Obsidian vault 上叠加运行（`init --vault`），使用[本地模型](../guides/local-models.md)实现零成本，并在需要带证据的图谱时选择启用图谱处理阶段（`ontology.triples` + `ontology.resolve`）。
- **团队** —— 通过 git 或[自托管服务器](../guides/self-hosted-server.md)共享同一个 wiki，共同评审实体消解提案与[输出信任](../guides/output-trust.md)，并用 hub 将多个 wiki 联邦起来。参阅[团队配置](../guides/team-setup.md)。
- **公司** —— 将存储迁移到 [PostgreSQL/pgvector](../guides/storage-backends.md)，开启[指标监控](../guides/metrics.md)，在服务器前加上认证，并用[分层编译](../guides/large-vault-performance.md)扩展摄取能力。

## 知识图谱与图记忆

![sage-wiki 图引擎](../../assets/sage-wiki-graph-engine.png)

向量检索返回与查询*看起来相似*的片段。图还记录 **事物之间如何关联**，因此需要两三跳才能回答的问题可以靠遍历得出，而不必指望某个片段恰好包含完整链条。sage-wiki 把这张图作为编译产物构建 —— 而不是另一套需要同步的数据库。

- **实体与带类型的关系。** 每次编译都会抽取实体（概念、来源、产物）并用带类型的关系连接。关系词表由你定义 —— 参见
  [可配置关系](../guides/configurable-relations.md)。
- **带证据的边。** 关系可携带 `evidence`（支撑它的原文片段）、`confidence`（0–1）和 `source_doc`，因此结论能追溯到证成这条边的那句话，而不只是整篇文档。
- **三元组。** 可选的结构化输出流程直接抽取 主语 → 关系 → 宾语。需显式开启（`ontology.triples`）：它会为每篇文档增加一次 LLM 调用，默认配置绝不在未经询问时花你的额度。
- **实体归一。** “K8s”与“Kubernetes”合并为同一节点。合并提案默认需经复核，不会静默合并。

**图是检索通道，而非旁支视图。** 每次检索融合三条通道 —— 词法（BM25）、向量与图邻近：查询词点亮起始实体，受限遍历为其邻域排序，三者按 `search.hybrid_weight_graph` 融合。本体为空时零开销，结果逐字节保持不变。

可直接查询，也可让 agent 通过 MCP 调用：

```bash
sage-wiki ontology query --entity kubernetes --depth 3 --direction both
sage-wiki provenance "service mesh"    # 哪些来源产生了这个概念
```

边是双时态（bi-temporal）的：事实被更正时旧边会被作废而非冲突，默认答案不含矛盾，`as_of` 查询可回答"一月份我们相信什么？"。有歧义的矛盾仍通过
[输出可信度](../guides/output-trust.md)复核浮现。对于跨语料的问题（“整体的主要主题是什么？”），可选的社区检测（`ontology.communities.enabled`）会生成缓存的社区摘要，并通过 `wiki_graph_query` `mode: "global"` 作答。深入了解：
[图记忆](../guides/graph-memory.md)。

## 指南

| 指南 | 说明 |
|-------|-------------|
| [Agent 记忆层](../guides/agent-memory-layer.md) | MCP 配置、技能文件、捕获工作流、读取-捕获-演进循环 |
| [HTTP API](../guides/http-api.md) | /v1 REST 接口：认证、错误模型、幂等性、异步任务 |
| [图记忆](../guides/graph-memory.md) | 带证据的关系、三元组抽取、实体消解、图问答 |
| [配置](../guides/configuration.md) | 逐行注释的完整 config.yaml、多提供商配置、serve 工作器 |
| [团队配置](../guides/team-setup.md) | Git 同步、共享服务器与 hub 联邦三种部署模式 |
| [搜索质量](../guides/search-quality.md) | 分块索引、查询扩展、重排序、图扩展、ANN |
| [大型 Vault 性能](../guides/large-vault-performance.md) | 分层编译、背压控制、代码解析器、100K+ 扩展 |
| [输出信任](../guides/output-trust.md) | 事实性验证、共识确认、提升/降级生命周期 |
| [订阅认证](../guides/subscription-auth.md) | OAuth 登录、令牌导入、凭证管理 |
| [自托管服务器](../guides/self-hosted-server.md) | Docker Compose、Syncthing、反向代理、VPS 部署 |
| [存储后端](../guides/storage-backends.md) | SQLite 与 PostgreSQL/pgvector 的安装、切换、连接池配置 |
| [可配置关系](../guides/configurable-relations.md) | 自定义本体类型、多语言同义词、类型限制 |
| [自定义提示词](../guides/customizing-prompts.md) | 提示词脚手架、按类型覆盖、自定义 frontmatter 字段 |
| [本地模型](../guides/local-models.md) | Ollama 设置、GPU/CPU 路由、按阶段模型配置 |
| [指标监控](../guides/metrics.md) | 日志快照、/metrics 端点、基数控制 |
| [贡献包](../../CONTRIBUTING.md) | 创建包、解析器开发、注册表提交 |

## 安装

```bash
# 仅命令行（不含 Web UI）
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest

# 含 Web UI（需要 Node.js 构建前端资源）
git clone https://github.com/xoai/sage-wiki.git && cd sage-wiki
cd web && npm install && npm run build && cd ..
go build -tags webui -o sage-wiki ./cmd/sage-wiki/
```

## 快速开始

![编译器管线](../../assets/sage-wiki-compiler-pipeline.png)

### 全新项目（Greenfield）

```bash
sage-wiki init my-wiki && cd my-wiki
# 将源文件添加到 raw/
cp ~/papers/*.pdf raw/
# 编辑 config.yaml，添加 API Key 并选择 LLM
sage-wiki compile                                  # 首次编译
sage-wiki search "attention mechanism"             # 混合搜索
sage-wiki query "How does flash attention work?"   # 带引用的问答
sage-wiki tui                                      # 终端面板
sage-wiki serve --ui                               # 浏览器（webui 构建）
sage-wiki compile --watch                          # 监听文件夹
```

`config.yaml` 的每一个键都有逐行注释：[配置指南](../guides/configuration.md)。

**项目结构**（`init` 创建的内容 — 节选，仅为示意并不详尽）：

```
my-wiki/
├── config.yaml           # 提供商、模型、编译器、搜索、本体
├── raw/                  # 将来源放在这里（文章、论文、代码、图片）
├── wiki/                 # 编译输出 — 兼容 Obsidian 的 markdown
│   ├── summaries/        # 每个来源的 LLM 摘要
│   ├── concepts/         # 概念文章（知识图谱）
│   ├── images/           # 视觉模型生成的图片描述
│   ├── outputs/          # 已归档的查询回答（trust.include_outputs: "true"）
│   ├── under_review/     # 等待信任审核的回答（默认）
│   └── archive/          # 已清理的文章
├── .sage/wiki.db         # 单个 SQLite 文件：FTS 索引、向量、本体、队列
└── .manifest.json        # 来源↔文章映射 + 编译状态
```

### Vault 覆盖模式（已有 Obsidian vault）

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# 编辑 config.yaml，设置源文件/忽略文件夹，添加 API Key，选择 LLM
sage-wiki compile --watch
```

更喜欢容器？预构建的多架构 Docker 镜像与 compose 文件见[自托管服务器指南](../guides/self-hosted-server.md)。

## 支持的源文件格式

| 格式        | 扩展名                                   | 提取内容                                                     |
| ----------- | --------------------------------------- | ----------------------------------------------------------- |
| Markdown    | `.md`                                   | 正文，frontmatter 单独解析                                   |
| PDF         | `.pdf`                                  | 纯 Go 提取全文                                               |
| Word        | `.docx`                                 | XML 中的文档文本                                             |
| Excel       | `.xlsx`                                 | 单元格值和工作表数据                                         |
| PowerPoint  | `.pptx`                                 | 幻灯片文本内容                                               |
| CSV         | `.csv`                                  | 表头 + 行数据（最多 1000 行）                                |
| EPUB        | `.epub`                                 | XHTML 中的章节文本                                           |
| 邮件        | `.eml`                                  | 邮件头（发件人/收件人/主题/日期）+ 正文                      |
| 纯文本      | `.txt`, `.log`                          | 原始内容                                                     |
| 字幕/转录   | `.vtt`, `.srt`                          | 原始内容                                                     |
| 图片        | `.png`, `.jpg`, `.gif`, `.webp`, `.svg`, `.bmp` | 通过视觉 LLM 生成描述（标题、内容、可见文字）        |
| 代码        | `.go`, `.py`, `.js`, `.ts`, `.rs` 等    | 源代码                                                       |

只需把文件放入源文件夹——sage-wiki 会自动检测格式。图片需要具备视觉能力的 LLM（Gemini、Claude、GPT-4o）。需要列表中没有的格式？sage-wiki 支持[外部解析器](#外部解析器)——任何语言编写的脚本，从 stdin 读取，向 stdout 输出文本。

## 图记忆

开箱即用，wiki 基于关键词邻近构建知识图谱——当关系关键词与 `[[wikilink]]` 在同一文本块中共现时，概念之间即建立连接。启用**可选的图谱处理阶段**，即可将其变成带证据的图谱：

- **三元组抽取**（`ontology.triples.enabled`）—— 对每个完整编译的文档追加一次 LLM 调用，抽取类型化实体与关系，每条都携带证据片段、置信度和来源文档。
- **实体消解**（`ontology.resolve.enabled`）—— 表面形式的变体（"NASA" / "National Aeronautics and Space Administration"）会被链接到规范实体。高置信度提案自动应用（阈值 0.85；设为恰好 `1.0` 则仅供评审），每次链接都可通过 `ontology resolve --unlink` 精确撤销。
- **图问答** —— `wiki_graph_query` MCP 工具回答多跳关系型问题，*仅*以有界的、序列化的边集合为依据；当边带有证据时，引用会携带 `source_doc` 和 `confidence`（关键词邻近边两者皆无）。常规问答的上下文也会在每篇相关文章下标注连接它的边。

深入程度、成本、评审工作流与撤销语义：[图记忆](../guides/graph-memory.md)。

## 命令

以下是核心命令面；运行 `sage-wiki <command> --help` 查看各命令参数。

| 命令 | 说明 |
| ------- | ----------- |
| `sage-wiki init [dir] [--vault] [--skill <agent>] [--pack <name>] [--prompts] [--force]` | 初始化项目（全新或 vault 覆盖模式） |
| `sage-wiki compile [--watch] [--batch] [--estimate] [--dry-run] [--no-cache] [--fresh] [--re-embed] [--re-extract] [--prune]` | 将源文件编译为 wiki 文章 |
| `sage-wiki serve [--transport stdio\|sse] [--ui] [--port 3333]` | MCP 服务器 / Web UI |
| `sage-wiki reindex [--drop-chunk-vectors]` | 以当前的 `chunk_size` / `chunk_overlap_tokens` 从磁盘上的文档重建 chunk 索引 |
| `sage-wiki search "query" [--tags ...] [--boost-tags ...] [--limit N] [--channels bm25,vector,graph] [--expand] [--rerank]` | 混合搜索（BM25 + 向量 + 本体图） |
| `sage-wiki query "question"` | 对 wiki 进行带引用的问答 |
| `sage-wiki tui` | 交互式终端面板 |
| `sage-wiki ontology <query\|list\|add\|resolve>` | 查询、管理和消解本体图 |
| `sage-wiki ingest <url\|path>` / `sage-wiki add-source <path>` | 添加源文件 |
| `sage-wiki source <show\|list>` / `sage-wiki coverage` | 检查源文件与编译覆盖率 |
| `sage-wiki status` / `sage-wiki doctor` / `sage-wiki diff` | 健康状态、配置校验、待处理变更 |
| `sage-wiki lint [--fix]` / `sage-wiki list` / `sage-wiki write <summary\|article>` | 维护与手动写入 |
| `sage-wiki hub <init\|add\|remove\|search\|status\|list\|compile>` | 多项目 hub |
| `sage-wiki learn "text"` / `sage-wiki capture "text"` / `sage-wiki scribe <session-file>` | 知识捕获 |
| `sage-wiki skill <refresh\|preview> [--target <agent>]` | 生成或刷新 Agent 技能文件 |
| `sage-wiki provenance <source-or-concept>` / `sage-wiki version` | 溯源映射、版本信息 |

特定主题的命令族随各自的指南介绍：`pack *` 见 [CONTRIBUTING](../../CONTRIBUTING.md)，`auth *`（login、import、status、logout、migrate）见[订阅认证](../guides/subscription-auth.md)，`verify` / `outputs *` 见[输出信任](../guides/output-trust.md)。

## TUI

```bash
sage-wiki tui
```

功能完整的终端面板，包含 4 个标签页：

- **[F1] 浏览** —— 按分区浏览文章（概念、摘要、输出）。方向键选择，Enter 阅读 glamour 渲染的 markdown，Esc 返回。
- **[F2] 搜索** —— 模糊搜索加分屏预览。输入即过滤，结果按混合分数排序，Enter 在 `$EDITOR` 中打开。
- **[F3] 问答** —— 流式对话问答。提出问题，获取 LLM 合成的带来源引用的回答。Ctrl+S 将回答保存到 outputs/。
- **[F4] 编译** —— 实时编译面板。监听源目录变化并自动重新编译。可浏览已编译文件并预览。

标签页切换：任意标签页按 `F1`-`F4`，浏览/编译页可按 `1`-`4`，`Esc` 返回浏览页。`Ctrl+C` 退出。

## Web UI

```bash
sage-wiki serve --ui        # http://127.0.0.1:3333，需要 -tags webui 构建
```

- **文章浏览器** —— 渲染 markdown、语法高亮、可点击的 `[[wikilinks]]`
- **混合搜索** —— 排序结果与摘要片段
- **知识图谱** —— 概念及其连接的交互式力导向可视化
- **流式问答** —— 提问并获取 LLM 合成的带来源引用的回答
- **目录导航** —— 支持滚动监听；深色/浅色模式自动检测系统偏好；失效的文章链接以灰色显示

使用 Preact + Tailwind 构建，通过 `go:embed` 嵌入（~1.2 MB，gzip 后 ~420 KB）；省略 `-tags webui` 可得到仅含 CLI/MCP 的二进制文件。认证令牌、允许的主机与部署加固：[自托管服务器](../guides/self-hosted-server.md)。

## MCP 集成

![MCP 集成](../../assets/sage-wiki-interfaces.png)

添加到 `.mcp.json`（Claude Code；其他 Agent 见 [Agent 记忆层指南](../guides/agent-memory-layer.md)）：

```json
{
  "mcpServers": {
    "sage-wiki": {
      "command": "sage-wiki",
      "args": ["serve", "--transport", "stdio", "--project", "/path/to/wiki"]
    }
  }
}
```

网络客户端：`sage-wiki serve --transport sse --port 3333`。服务器暴露 19 个工具——搜索、读取、图查询、捕获、`wiki_query`（问答并将结果存入待审区）、按需编译等；各 Agent 的配置方法与捕获工作流见 [Agent 记忆层指南](../guides/agent-memory-layer.md)。

**Agent 技能文件** —— `sage-wiki skill refresh --target <agent>` 会向 Agent 的指令文件（CLAUDE.md、.cursorrules 等）写入一段行为规范，根据你的配置生成，教会它何时搜索、捕获什么、如何查询。支持的目标：`claude-code`、`cursor`、`windsurf`、`agents-md`（Antigravity）、`codex`、`gemini`、`generic`。

### Agent 技能

安装 sage-wiki 的参考技能，让编程助手无需阅读本 README 即可了解完整的工具面——全部 19 个 MCP 工具、`/v1` REST 对应接口、可选开关、层级、异步编译语义和错误码：

```bash
# Claude Code
npx skills add https://github.com/xoai/sage-wiki --skill sage-wiki

# 或手动：将 skills/sage-wiki/SKILL.md 复制到 .claude/skills/
```

流水线技能 `sage-wiki-integrate` 以交互方式将 sage-wiki 接入新仓库（检测语言 → 安装客户端或配置 MCP → 存取冒烟测试）：

```bash
npx skills add https://github.com/xoai/sage-wiki --skill sage-wiki-integrate
```

两个技能都从实时 MCP 注册表生成（`go run ./tools/skillgen/`），并在 CI 中进行漂移检查——工具变更时不会过时。Pre-1.0 —— 请锁定版本。

**知识捕获** —— Agent 通过 `wiki_capture` / `wiki_learn` 将洞见存回 wiki，闭合"读取-捕获-演进"循环。工作流与技巧：[Agent 记忆层](../guides/agent-memory-layer.md)。

## 客户端 SDK

`/v1` REST API 的类型化客户端（Pre-1.0 —— 请锁定版本）：

**Python** —— `pip install sagewiki`（≥3.9，仅依赖 `httpx`）：

```python
from sagewiki import SageWiki

c = SageWiki()  # 从环境变量读取 SAGE_WIKI_URL / SAGE_WIKI_TOKEN
for r in c.search("attention", limit=5).results:
    print(r.final_score, r.content[:80])
job = c.compile(topic="attention")
job.wait(timeout=600)  # 必须显式指定超时
```

**TypeScript** —— `npm install sagewiki`（零运行时依赖，全局
`fetch`；Node ≥18、Deno、Bun、边缘运行时）：

```ts
import { SageWikiClient } from "sagewiki";

const c = new SageWikiClient();
const results = await c.search("attention", { limit: 5 });
const job = await c.compile({ topic: "attention" });
await job.waitUntilDone({ timeoutMs: 600_000 });
```

两个客户端都覆盖完整的 `/v1` 接口：搜索、溯源、图查询、编译后的 wiki、
捕获/写入，以及异步 compile/lint 任务和基于错误码的错误分类。文档：
[Python](../../clients/python/README.md) · [TypeScript](../../clients/typescript/README.md) ·
[HTTP API 指南](../guides/http-api.md)。Go 程序可以完全绕过 HTTP ——
参见[在 Go 程序中嵌入](#在-go-程序中嵌入)。

### 示例

可直接复制的框架集成，在 CI 中针对真实服务器运行验证：

- [`examples/langgraph/`](../../examples/langgraph/) —— 带记忆的 LangGraph
  节点（Python 客户端）：`uncompiled_sources` → 主题编译模式的检索与捕获。
- [`examples/vercel-ai-sdk/`](../../examples/vercel-ai-sdk/) —— 以 Vercel AI
  SDK 工具形式提供 `search`、`graphQuery`、`provenance`（TypeScript
  客户端）；可部署到边缘。

### 在 Go 程序中嵌入

要在自己的 Go 进程中调用相同的工具——无需子进程，无需管理 stdio 或端口——请使用 `pkg/sagewiki` 配合 mcp-go 的进程内传输：

```go
srv, err := sagewiki.NewServer("/path/to/wiki")  // project must already exist
if err != nil {
    return err
}
defer srv.Close()  // the caller owns the DB handle here

cli, err := client.NewInProcessClient(srv.MCPServer())
if err != nil {
    return err
}
defer cli.Close()

if err := cli.Start(ctx); err != nil {
    return err
}
if _, err := cli.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
    return err
}

res, err := cli.CallTool(ctx, mcp.CallToolRequest{
    Params: mcp.CallToolParams{
        Name:      "wiki_search",
        Arguments: map[string]any{"query": "attention", "limit": 5},
    },
})
```

项目必须已存在，且调用方拥有数据库句柄，因此必须调用 `Close` —— 与 `serve` 不同，没有其他东西会关闭它。日志输出到宿主的 stderr，`initialize` 报告的是 sage-wiki 的构建版本（普通 `go build` 时为 `dev`）；在启动时调用 `sagewiki.SetVersion` 可以报告你自己的版本字符串。

该包在 sage-wiki 未达 1.0 前处于**实验阶段**：Go 签名预期保持稳定，但工具名称、参数 schema 和 `config.yaml` 布局可能在任何版本中变化。请固定版本。

## 运维

- **存储** —— 默认 SQLite（单文件、零配置）；服务器部署可用 PostgreSQL + pgvector。切换与连接池配置：[存储后端](../guides/storage-backends.md)。
- **可观测性** —— 结构化日志快照与可选开启的 `/metrics` 端点：[指标监控](../guides/metrics.md)。
- **结构化输出** —— LLM 抽取阶段使用各提供商的原生机制（Anthropic tool-use、OpenAI `response_format`、Gemini `responseSchema`），并带有校验式代码块剥离回退。
- **凭证** —— 订阅令牌在可用时存储于操作系统钥匙串；运行一次 `sage-wiki auth migrate` 即可将文件存储的凭证迁移过去。[订阅认证](../guides/subscription-auth.md)。
- **配置** —— 每个键都有注释，含多提供商配方与 serve 模式编译工作器：[配置指南](../guides/configuration.md)。
- **实体消解** —— 0.85 自动应用，可通过 `--unlink` 精确撤销；见上文[图记忆](#图记忆)。
- **自定义关系/实体类型** —— 扩展内置类型或添加自定义类型（`ontology.relation_types`），支持多语言同义词与类型限制：[可配置关系](../guides/configurable-relations.md)。
- **输出信任** —— 查询输出在通过事实性验证、共识确认或手动提升之前处于隔离状态：[输出信任](../guides/output-trust.md)。
- **搜索调优** —— 分块、查询扩展、重排序、图扩展与可选 ANN：[搜索质量](../guides/search-quality.md)。

### 费用

sage-wiki 会追踪每次编译的 token 用量并估算费用。**Prompt 缓存**（默认开启）在同一编译阶段的多次调用间复用系统提示词——Anthropic 和 Gemini 显式缓存，OpenAI 自动缓存——可节省 50-90% 的输入 token。**Batch API**（Anthropic、OpenAI 和 Gemini）可将大型编译的费用减半：

```bash
sage-wiki compile --batch       # 提交批次，保存检查点，退出
sage-wiki compile               # 轮询状态，完成后取回结果
```

`compile --estimate` 可预览费用；`compiler.mode: auto` 会在超过阈值后自动使用批处理。详情：[配置指南](../guides/configuration.md)。

### 扩展到大型 vault

分层编译按类型和使用情况路由每个源文件，而不是对所有内容都进行 LLM 编译：

| 层级 | 处理内容 | 费用 | 每文档耗时 |
|------|-------------|------|-------------|
| **0** —— 仅索引 | FTS5 全文搜索 | 免费 | ~5ms |
| **1** —— 索引 + 向量 | FTS5 + 向量 embedding | ~$0.00002 | ~200ms |
| **2** —— 代码解析 | 正则解析器生成结构摘要（无 LLM） | 免费 | ~10ms |
| **3** —— 完整编译 | 摘要 + 提取概念 + 写作文章 | ~$0.05-0.15 | ~5-8 分钟 |

对于大型 vault：先在层级 1 索引所有内容（100K 文档的 vault 约需 ~5.5 小时），然后按需编译——自动提升、背压与代码解析器详见[大型 Vault 性能](../guides/large-vault-performance.md)。

## 生态系统

### 贡献包

包（pack）为特定领域捆绑本体类型、提示词与技能触发器。8 个内置包可离线使用：

| 包 | 受众 | 关键本体 |
|------|----------|-------------|
| `academic-research` | 研究人员 | cites, contradicts, finding, research_hypothesis |
| `software-engineering` | 开发团队 | implements, depends_on, adr, runbook |
| `product-management` | 产品经理 | addresses, prioritizes, user_story |
| `personal-knowledge` | 笔记管理者 | relates_to, inspired_by, fleeting_note |
| `study-group` | 学生 | explains, prerequisite_of, definition |
| `meeting-organizer` | 管理者 | decided, assigned_to, action_item |
| `content-creation` | 写作者 | references, revises, draft, published |
| `legal-compliance` | 法务团队 | regulates, supersedes, policy, control |

`sage-wiki init --pack academic-research` 可在初始化时应用一个包；`pack install <name|url>` 可添加更多。创建与发布包：[CONTRIBUTING](../../CONTRIBUTING.md)。

### 外部解析器

用任何语言编写的脚本处理任意文件格式（stdin → 文本输出到 stdout），在 `parsers/parser.yaml` 中声明，并需双重显式启用——它们作为未沙箱化的子进程运行，带有超时强制与环境变量清理。编写与加固细节：[CONTRIBUTING](../../CONTRIBUTING.md)；信任边界的讨论：[团队配置](../guides/team-setup.md)。

### 团队

三种共享模式——git 同步、共享服务器、hub 联邦——以及团队信任评审与费用管理：[团队配置](../guides/team-setup.md)。

## 基准测试

两套评测回答不同的问题。完整细节：
[eval/benchmarks/REPORT.md](../../eval/benchmarks/REPORT.md) · [eval/REPORT.md](../../eval/REPORT.md)

**记忆基准** — 能否回答关于长对话的问题？采用公开数据集、LLM 评判，沿用
[mem0ai/memory-benchmarks](https://github.com/mem0ai/memory-benchmarks) 的提示词与流程，仅将后端换成 sage-wiki（回答与评判均为 gpt-5，抽样）：

| 基准 | Score | Mem0 Platform |
|---|---|---|
| LOCOMO (150 q) | **92.0%** @ top-50 | 91.8% @ top-50 |
| LongMemEval-S (30 q) | **93.3%** @ top-50 | 94.8% @ top-50 |
| BEAM 100K (60 q) | **0.691** mean nugget | 0.641 @ 1M |

这并非严格对等的排名：mem0 在其托管平台上跑完整题库，这里是抽样（±4–5 个百分点），编译管线也不同。报告中已列明各项前提。

**质量与性能评估** — wiki 是否结构良好且快速？可在任何已编译的 wiki 上运行，无需 API key，数秒完成。10 个真实 wiki 的中位数：总分 **87.4%**，事实抽取 100%，recall@10 100%，交叉引用完整性 100%。进程内检索：FTS5 top-10 **0.035 ms**，混合 RRF **4.9 ms**，图 BFS **0.001 ms**。

```bash
python3 eval/eval.py .                      # wiki 的质量与性能
python3 -m pytest eval/eval_test.py -q      # 工具自测
```

## 许可证

MIT
