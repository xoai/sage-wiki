[English](README.md) | **中文**

# sage-wiki

基于 [Andrej Karpathy 的想法](https://x.com/karpathy/status/2039805659525644595)实现的 LLM 编译型个人知识库。使用 [Sage Framework](https://github.com/xoai/sage) 开发。

构建 sage-wiki 过程中的一些经验总结见[这里](https://x.com/xoai/status/2040936964799795503)。

将你的论文、文章和笔记放入文件夹,sage-wiki 会将它们编译为结构化、互相链接的 wiki -- 自动提取概念、发现交叉引用,并支持全文搜索。

- **输入源文件,输出 wiki。** 将文档放入文件夹。LLM 会阅读、摘要、提取概念,并生成互相关联的文章。
- **知识持续积累。** 每一个新源文件都会丰富已有文章。wiki 会随着内容增长变得越来越智能。
- **与你的工具无缝集成。** 原生支持 Obsidian 打开。通过 MCP 连接任意 LLM Agent。单一二进制文件 -- 除了 API Key 无需额外安装。
- **向你的 wiki 提问。** 增强搜索支持 chunk 级别索引、LLM 查询扩展和重排序。用自然语言提问,获取带引用的回答。

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

*外圈的点代表知识库中所有文档的摘要,内圈的点代表从知识库中提取的概念,连线展示了这些概念之间的关联。*

## 安装

```bash
# 仅命令行 (不含 Web UI)
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest

# 含 Web UI (需要 Node.js 用于构建前端资源)
git clone https://github.com/xoai/sage-wiki.git && cd sage-wiki
cd web && npm install && npm run build && cd ..
go build -tags webui -o sage-wiki ./cmd/sage-wiki/
```

## 支持的源文件格式

| 格式 | 扩展名 | 提取内容 |
|------|--------|---------|
| Markdown | `.md` | 正文,frontmatter 单独解析 |
| PDF | `.pdf` | 纯 Go 提取全文 |
| Word | `.docx` | XML 中的文档文本 |
| Excel | `.xlsx` | 单元格值和工作表数据 |
| PowerPoint | `.pptx` | 幻灯片文本内容 |
| CSV | `.csv` | 表头 + 行数据 (最多 1000 行) |
| EPUB | `.epub` | XHTML 中的章节文本 |
| 邮件 | `.eml` | 邮件头 (发件人/收件人/主题/日期) + 正文 |
| 纯文本 | `.txt`, `.log` | 原始内容 |
| 字幕 | `.vtt`, `.srt` | 原始内容 |
| 图片 | `.png`, `.jpg`, `.gif`, `.webp`, `.svg` | 通过 vision LLM 生成描述 (标题、内容、可见文字) |
| 代码 | `.go`, `.py`, `.js`, `.ts`, `.rs` 等 | 源代码 |

只需将文件放入源文件夹 -- sage-wiki 会自动检测格式。图片需要支持 vision 的 LLM (Gemini、Claude、GPT-4o)。

## 快速开始

![Compiler Pipeline](sage-wiki-compiler-pipeline.png)

### 全新项目 (Greenfield)

```bash
mkdir my-wiki && cd my-wiki
sage-wiki init
# 将源文件放入 raw/
cp ~/papers/*.pdf raw/papers/
cp ~/articles/*.md raw/articles/
# 编辑 config.yaml,添加 API Key 并选择 LLM
# 首次编译
sage-wiki compile
# 搜索
sage-wiki search "attention mechanism"
# 提问
sage-wiki query "How does flash attention optimize memory?"
# 交互式终端面板
sage-wiki tui
# 在浏览器中查看 (需要 -tags webui 构建)
sage-wiki serve --ui
# 监听文件夹变化
sage-wiki compile --watch
```

### Vault 覆盖模式 (已有 Obsidian 仓库)

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# 编辑 config.yaml,设置源文件/忽略文件夹,添加 API Key,选择 LLM
# 首次编译
sage-wiki compile
# 监听仓库变化
sage-wiki compile --watch
```

### Docker

```bash
# 从 GitHub Container Registry 拉取
docker pull ghcr.io/xoai/sage-wiki:latest

# 或从 Docker Hub 拉取
docker pull xoai/sage-wiki:latest

# 挂载 wiki 目录运行
docker run -d -p 3333:3333 -v ./my-wiki:/wiki -e GEMINI_API_KEY=... ghcr.io/xoai/sage-wiki

# 或从源码构建
docker build -t sage-wiki .
docker run -d -p 3333:3333 -v ./my-wiki:/wiki -e GEMINI_API_KEY=... sage-wiki
```

可用标签: `:latest` (main 分支), `:v1.0.0` (发行版), `:sha-abc1234` (指定 commit)。支持多架构: `linux/amd64` 和 `linux/arm64`。

参阅[自托管指南](docs/guides/self-hosted-server.md)了解 Docker Compose、Syncthing 同步、反向代理和 LLM 提供商配置。

## 命令

| 命令 | 说明 |
|------|------|
| `sage-wiki init [--vault]` | 初始化项目 (全新或 vault 覆盖模式) |
| `sage-wiki compile [--watch] [--dry-run] [--batch] [--estimate] [--no-cache] [--prune]` | 将源文件编译为 wiki 文章 |
| `sage-wiki serve [--transport stdio\|sse]` | 启动 MCP 服务器供 LLM Agent 使用 |
| `sage-wiki serve --ui [--port 3333]` | 启动 Web UI (需要 `-tags webui` 构建) |
| `sage-wiki lint [--fix] [--pass name]` | 运行 lint 检查 |
| `sage-wiki search "query" [--tags ...]` | 混合搜索 (BM25 + 向量) |
| `sage-wiki query "question"` | 对 wiki 进行问答 |
| `sage-wiki tui` | 启动交互式终端面板 |
| `sage-wiki ingest <url\|path>` | 添加源文件 |
| `sage-wiki status` | 查看 wiki 统计和健康状态 |
| `sage-wiki provenance <source-or-concept>` | 查看源文件与文章的溯源映射 |
| `sage-wiki doctor` | 验证配置和连接性 |

## TUI

```bash
sage-wiki tui
```

功能完整的终端面板,包含 4 个标签页:

- **[F1] 浏览** -- 按分区浏览文章 (概念、摘要、输出)。方向键选择,Enter 阅读 glamour 渲染的 markdown,Esc 返回。
- **[F2] 搜索** -- 模糊搜索 + 分屏预览。输入关键词过滤,结果按混合分数排序,Enter 在 `$EDITOR` 中打开。
- **[F3] 问答** -- 流式对话问答。提出问题,获取 LLM 合成的带源引用回答。Ctrl+S 保存回答到 outputs/。
- **[F4] 编译** -- 实时编译面板。监听源文件目录变化并自动重新编译。可浏览已编译文件并预览。

标签页切换: 任意标签页按 `F1`-`F4`,在浏览/编译页按 `1`-`4`,`Esc` 返回浏览页。`Ctrl+C` 退出。

## Web UI

![Sage-Wiki Architecture](sage-wiki-webui.png)

sage-wiki 内置可选的浏览器界面,用于阅读和探索你的 wiki。

```bash
sage-wiki serve --ui
# 在 http://127.0.0.1:3333 打开
```

功能:
- **文章浏览器**: 渲染 markdown,语法高亮,可点击的 `[[wikilinks]]`
- **混合搜索**: 排序结果和摘要片段
- **知识图谱**: 交互式力导向图,可视化概念及其关联
- **流式问答**: 提问并获取 LLM 合成的带源引用回答
- **目录导航**: 支持滚动监听,或切换到图谱视图
- **深色/浅色模式**: 支持系统偏好检测
- **断链检测**: 缺失的文章链接显示为灰色

Web UI 使用 Preact + Tailwind CSS 构建,通过 `go:embed` 嵌入 Go 二进制文件。压缩后增加约 1.2 MB 体积。构建时省略 `-tags webui` 标志即可不包含 Web UI -- 二进制文件仍支持所有 CLI 和 MCP 操作。

选项:
- `--port 3333` -- 修改端口 (默认 3333)
- `--bind 0.0.0.0` -- 暴露到网络 (默认仅 localhost,无认证)

## 配置

`config.yaml` 由 `sage-wiki init` 创建。完整示例:

```yaml
version: 1
project: my-research
description: "Personal research wiki"

# 要监听和编译的源文件夹
sources:
  - path: raw               # 或 vault 文件夹如 Clippings/, Papers/
    type: auto               # 根据文件扩展名自动检测
    watch: true

output: wiki                 # 编译输出目录 (vault 覆盖模式下为 _wiki)

# 不读取也不发送给 API 的文件夹 (vault 覆盖模式)
# ignore:
#   - Daily Notes
#   - Personal

# LLM 提供商
# 支持: anthropic, openai, gemini, ollama, openai-compatible
# OpenRouter 或其他 OpenAI 兼容提供商:
#   provider: openai-compatible
#   base_url: https://openrouter.ai/api/v1
api:
  provider: gemini
  api_key: ${GEMINI_API_KEY}    # 支持环境变量展开
  # base_url:                   # 自定义端点 (OpenRouter, Azure 等)
  # rate_limit: 60              # 每分钟请求数

# 按任务指定模型 -- 高频任务用便宜模型,写作用高质量模型
models:
  summarize: gemini-3-flash-preview
  extract: gemini-3-flash-preview
  write: gemini-3-flash-preview
  lint: gemini-3-flash-preview
  query: gemini-3-flash-preview

# Embedding 提供商 (可选 -- 自动从 api 提供商检测)
# 可覆盖为不同的 embedding 提供商
embed:
  provider: auto              # auto, openai, gemini, ollama, voyage, mistral
  # model: text-embedding-3-small
  # api_key: ${OPENAI_API_KEY}  # 单独的 embedding API Key
  # base_url:                   # 单独的端点

compiler:
  max_parallel: 4             # 并发 LLM 调用数
  debounce_seconds: 2         # watch 模式防抖
  summary_max_tokens: 2000
  article_max_tokens: 4000
  auto_commit: true           # 编译后自动 git commit
  auto_lint: true             # 编译后自动 lint
  # mode: standard            # standard, batch 或 auto
  # estimate_before: false    # 编译前显示费用估算
  # prompt_cache: true        # 启用 prompt 缓存 (默认: true)
  # batch_threshold: 10       # auto-batch 模式的最小源文件数
  # token_price_per_million: 0  # 自定义价格 (0 = 使用内置价格)
  # timezone: Asia/Shanghai   # IANA 时区 (默认: UTC)
  # article_fields:           # 从 LLM 响应中提取的自定义 frontmatter 字段
  #   - language
  #   - domain

search:
  hybrid_weight_bm25: 0.7    # BM25 与向量搜索的权重
  hybrid_weight_vector: 0.3
  default_limit: 10
  # query_expansion: true     # LLM 查询扩展 (默认: true)
  # rerank: true              # LLM 重排序 (默认: true)
  # chunk_size: 800           # 索引的 chunk 大小 (100-5000 tokens)
  # graph_expansion: true     # 基于图的上下文扩展 (默认: true)
  # graph_max_expand: 10      # 图扩展最大文章数
  # graph_depth: 2            # 本体遍历深度 (1-5)
  # context_max_tokens: 8000  # 查询上下文 token 预算
  # weight_direct_link: 3.0   # 图信号: 概念间的本体关系
  # weight_source_overlap: 4.0 # 图信号: 共享源文档
  # weight_common_neighbor: 1.5 # 图信号: Adamic-Adar 共同邻居
  # weight_type_affinity: 1.0  # 图信号: 实体类型对加成

serve:
  transport: stdio            # stdio 或 sse
  port: 3333                  # 仅 SSE 模式

# 本体关系类型 (可选)
# 扩展内置类型的同义词或添加自定义关系类型。
# ontology:
#   relations:
#     - name: implements           # 为内置类型添加更多同义词
#       synonyms: ["thuc hien", "trien khai"]
#     - name: regulates            # 添加自定义关系类型
#       synonyms: ["regulates", "regulated by", "调控"]
```

### 可配置的关系类型

本体内置 8 种关系类型: `implements`、`extends`、`optimizes`、`contradicts`、`cites`、`prerequisite_of`、`trades_off`、`derived_from`。每种都有默认的关键词同义词用于自动提取。

你可以通过 `config.yaml` 中的 `ontology.relations` 自定义关系:

- **扩展内置类型** -- 为已有类型添加同义词 (如多语言关键词)。默认同义词保留,你的追加在后面。
- **添加自定义类型** -- 定义新的关系名称及其关键词同义词。关系名称必须为小写 `[a-z][a-z0-9_]*`。

零配置 = 与当前行为完全相同。现有数据库会在首次打开时自动迁移。参阅[完整指南](docs/guides/configurable-relations.md)了解领域特定示例、内置同义词表和提取原理。

## 费用优化

sage-wiki 会追踪每次编译的 token 用量并估算费用。三种降低费用的策略:

**Prompt 缓存** (默认开启) -- 在编译过程中复用系统提示词。Anthropic 和 Gemini 显式缓存; OpenAI 自动缓存。可节省 50-90% 的输入 token 费用。

**Batch API** -- 将所有源文件作为单个异步批次提交,费用降低 50%。支持 Anthropic 和 OpenAI。

```bash
sage-wiki compile --batch       # 提交批次,保存检查点,退出
sage-wiki compile               # 轮询状态,完成后获取结果
```

**费用估算** -- 编译前预览费用:

```bash
sage-wiki compile --estimate    # 显示费用明细后退出
```

或在配置中设置 `compiler.estimate_before: true` 以每次编译前提示。

**自动模式** -- 设置 `compiler.mode: auto` 和 `compiler.batch_threshold: 10`,编译 10 个以上源文件时自动使用 batch 模式。

## 搜索质量

sage-wiki 使用增强搜索管线处理问答查询,灵感来自对 [qmd](https://github.com/dmayboroda/qmd) 检索方案的分析:

- **Chunk 级别索引** -- 文章被切分为约 800 token 的 chunk,每个 chunk 拥有独立的 FTS5 条目和向量 embedding。搜索 "flash attention" 可以精准定位到 3000 token 的 Transformer 文章中的相关段落。
- **LLM 查询扩展** -- 单次 LLM 调用生成关键词改写 (用于 BM25)、语义改写 (用于向量搜索) 和假设回答 (用于 embedding 相似度)。强信号检测会在 BM25 首个结果已有高置信度时跳过扩展。
- **LLM 重排序** -- 前 15 个候选结果由 LLM 评分相关性。位置感知融合保护高置信检索结果 (排名 1-3 使用 75% 检索权重,排名 11+ 使用 60% 重排序权重)。
- **BM25 预过滤向量搜索** -- 向量比较仅限于 BM25 候选文档的 chunk,余弦计算上限约 250 次,不受 wiki 规模影响。
- **图增强上下文扩展** -- 检索后,4 信号图评分器通过本体发现相关文章: 直接关系 (x3.0)、共享源文档 (x4.0)、Adamic-Adar 共同邻居 (x1.5) 和实体类型亲和度 (x1.0)。这能找到结构上相关但被关键词/向量搜索遗漏的文章。
- **Token 预算控制** -- 查询上下文限制在可配置的 token 上限内 (默认 8000),每篇文章截断至 4000 token。贪心填充优先选择评分最高的文章。

| | sage-wiki | qmd |
|---|---|---|
| Chunk 搜索 | FTS5 + 向量 (双通道) | 仅向量 |
| 查询扩展 | 基于 LLM (词法/向量/HyDE) | 基于 LLM |
| 重排序 | LLM + 位置感知融合 | Cross-encoder |
| 图上下文 | 4 信号图扩展 + 1 跳遍历 | 无图 |
| 单次查询费用 | 免费 (Ollama) / 约 $0.0006 (云端) | 免费 (本地 GGUF) |

零配置 = 所有功能默认启用。使用 Ollama 或其他本地模型时,增强搜索完全免费 -- 重排序会自动禁用 (本地模型难以处理结构化 JSON 评分),但 chunk 级别搜索和查询扩展仍然有效。使用云端 LLM 时,额外费用可忽略 (约 $0.0006/次查询)。扩展和重排序均可通过配置开关。参阅[完整搜索质量指南](docs/guides/search-quality.md)了解配置、费用明细和详细对比。

## 自定义提示词

sage-wiki 使用内置提示词进行摘要和文章写作。如需自定义:

```bash
sage-wiki init --prompts    # 生成 prompts/ 目录及默认模板
```

这会创建可编辑的 markdown 文件:

```
prompts/
  summarize-article.md    # 文章摘要方式
  summarize-paper.md      # 论文摘要方式
  write-article.md        # 概念文章写作方式
  extract-concepts.md     # 概念识别方式
  caption-image.md        # 图片描述方式
```

编辑任意文件来改变 sage-wiki 处理该类型的方式。通过创建 `summarize-{type}.md` (如 `summarize-dataset.md`) 添加新的源文件类型。删除文件将恢复为内置默认值。

### 自定义 Frontmatter 字段

文章 frontmatter 来自两个来源: **确定性数据** (概念名、别名、源文件、时间戳) 始终由代码生成,而**语义字段**由 LLM 评估。

默认情况下,`confidence` 是唯一的 LLM 评估字段。添加自定义字段:

1. 在 `config.yaml` 中声明:

```yaml
compiler:
  article_fields:
    - language
    - domain
```

2. 更新 `prompts/write-article.md` 模板,要求 LLM 提供这些字段:

```
At the end of your response, state:
Language: (the primary language of the concept)
Domain: (the academic field, e.g., machine learning, biology)
Confidence: high, medium, or low
```

LLM 的回答会从文章正文中提取并自动合并到 YAML frontmatter 中。最终的 frontmatter 如下:

```yaml
---
concept: self-attention
aliases: ["scaled dot-product attention"]
sources: ["raw/transformer-paper.md"]
confidence: high
language: English
domain: machine learning
created_at: 2026-04-10T08:00:00+08:00
---
```

确定性字段 (`concept`、`aliases`、`sources`、`created_at`) 始终准确 -- 它们来自提取阶段,而非 LLM。语义字段 (`confidence` + 你的自定义字段) 反映 LLM 的判断。

## MCP 集成

![MCP Integration](sage-wiki-interfaces.png)

### Claude Code

添加到 `.mcp.json`:

```json
{
  "mcpServers": {
    "sage-wiki": {
      "command": "sage-wiki",
      "args": ["serve", "--project", "/path/to/wiki"]
    }
  }
}
```

### SSE (网络客户端)

```bash
sage-wiki serve --transport sse --port 3333
```

## 从 AI 对话中捕获知识

sage-wiki 作为 MCP 服务器运行,因此你可以直接从 AI 对话中捕获知识。将其连接到 Claude Code、ChatGPT、Cursor 或任何 MCP 客户端 -- 然后直接说:

> "把我们刚刚关于连接池的发现保存到我的 wiki"

> "记录这次调试会话中的关键决策"

`wiki_capture` 工具通过 LLM 从对话文本中提取知识项 (决策、发现、修正),写入源文件,并排队等待编译。噪音 (问候语、重试、死胡同) 会被自动过滤。

对于单条事实,`wiki_learn` 可直接存储。对于完整文档,`wiki_add_source` 可导入文件。运行 `wiki_compile` 处理所有内容为文章。

参阅完整配置指南: [MCP 知识捕获指南](docs/guides/mcp-knowledge-capture.md)

## 基准测试

基于一个真实 wiki 进行评估,该 wiki 从 1,107 个源文件编译而来 (49.4 MB 数据库,2,832 个 wiki 文件)。

在你自己的项目上运行 `python3 eval.py .` 即可复现。详见 [eval.py](eval.py)。

### 性能

| 操作 | p50 | 吞吐量 |
|------|----:|-------:|
| FTS5 关键词搜索 (top-10) | 411us | 1,775 qps |
| 向量余弦搜索 (2,858 x 3072d) | 81ms | 15 qps |
| 混合 RRF (BM25 + 向量) | 80ms | 16 qps |
| 图遍历 (BFS 深度 <= 5) | 1us | 738K qps |
| 环检测 (全图) | 1.4ms | -- |
| FTS 插入 (批量 100) | -- | 89,802 /s |
| 持续混合读取 | 77us | 8,500+ ops/s |

非 LLM 编译开销 (哈希 + 依赖分析) 低于 1 秒。编译器的实际耗时完全由 LLM API 调用决定。

### 质量

| 指标 | 分数 |
|------|-----:|
| 搜索召回率@10 | **100%** |
| 搜索召回率@1 | 91.6% |
| 源引用率 | 94.6% |
| 别名覆盖率 | 90.0% |
| 事实提取率 | 68.5% |
| Wiki 连通性 | 60.5% |
| 交叉引用完整性 | 50.0% |
| **综合质量分数** | **73.0%** |

### 运行评估

```bash
# 完整评估 (性能 + 质量)
python3 eval.py /path/to/your/wiki

# 仅性能
python3 eval.py --perf-only .

# 仅质量
python3 eval.py --quality-only .

# 机器可读 JSON
python3 eval.py --json . > report.json
```

需要 Python 3.10+。安装 `numpy` 可获得约 10 倍向量基准测试加速。

### 运行测试

```bash
# 运行完整测试套件 (生成合成测试数据,不需要真实数据)
python3 -m unittest eval_test -v

# 生成独立测试数据集
python3 eval_test.py --generate-fixture ./test-fixture
python3 eval.py ./test-fixture
```

24 个测试覆盖: 测试数据生成、CLI 模式 (`--perf-only`, `--quality-only`, `--json`)、JSON schema 验证、分数边界、搜索召回率、边界情况 (空 wiki、大数据集、缺失路径)。

## 架构

![Sage-Wiki Architecture](sage-wiki-architecture.png)

- **存储:** SQLite + FTS5 (BM25 搜索) + BLOB 向量 (余弦相似度)
- **本体:** 类型化实体-关系图,支持 BFS 遍历和环检测
- **搜索:** 增强管线,支持 chunk 级别 FTS5 + 向量索引、LLM 查询扩展、LLM 重排序、RRF 融合和 4 信号图扩展。回退为文档级别 BM25 + 向量 + 标签加成 + 时间衰减
- **编译器:** 5 阶段管线 (diff、摘要、提取概念、写作文章、图片),支持 prompt 缓存、batch API、费用追踪和源文件删除级联感知
- **MCP:** 16 个工具 (6 读、8 写、2 组合),通过 stdio 或 SSE 提供,包括 `wiki_capture` 知识提取和 `wiki_provenance` 源文件-文章映射
- **TUI:** bubbletea + glamour 4 标签终端面板 (浏览、搜索、问答、编译)
- **Web UI:** Preact + Tailwind CSS 通过 `go:embed` 嵌入,使用构建标签 (`-tags webui`)

零 CGO。纯 Go。跨平台。

## 许可证

MIT
