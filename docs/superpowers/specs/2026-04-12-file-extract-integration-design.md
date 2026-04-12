# file-extract × sage-wiki 集成设计规范

> 状态：brainstorm v3 完成（含 25 轮 scenario 验证），待 writing-plans
> 日期：2026-04-12（v3 修订）
> 变更：v2 基础上整合 15 轮 spec 验证 scenario（V1-V15），修复 2 个 CRITICAL 阻塞、5 个 HIGH 设计缺陷

## 1. 概述

将 file-extract（通用文件提取工具，独立 Python 项目）集成到 sage-wiki（Go 知识库系统），实现三层内容消费：原文访问、结构化数字查询、综合知识搜索。

### 核心原则

- **松耦合**：file-extract 不知道 sage-wiki 存在，sage-wiki 通过文件契约读取 file-extract 的输出
- **graceful fallback**：无预提取内容时，sage-wiki 原有 Go 引擎照常工作
- **全产出消费**：file-extract 的 .md、.numbers.yaml、conflicts.yaml 都被 sage-wiki 消费
- **质量优先**：数字准确性、内容完整性、可追溯性是核心目标
- **可 PR 回上游**：所有 sage-wiki 改动定位为通用功能，不绑定 file-extract

### 职责边界

- **file-extract**：单文件级别精确提取（文本 + 元数据 + 结构化数字）
- **sage-wiki**：跨文件级别知识连接（内容关联 + 数字存储 + 本体构建 + 查询服务）
- **wiki skill**：对话式知识助手，面向下游消费（template-writing、用户查询）
- **wiki-manage skill**：全生命周期维护编排（创建/维护/体检/底座/看板）

## 2. 集成方式

### 文件契约（方式 A）

两个系统通过磁盘文件传递数据，互不调用：

```
file-extract 写文件到 .pre-extracted/
     ↓（磁盘文件，格式契约）
sage-wiki 读文件从 .pre-extracted/
```

- 谁先跑谁后跑由外部编排（wiki-manage skill 或用户手动）
- sage-wiki 通过 config.yaml `pre_extracted_dir` 知道去哪读（默认 `.pre-extracted`）
- 不存在 `.pre-extracted/` 时自动降级到内建 Go 引擎

### 契约版本协商

`.pre-extracted/extract-meta.yaml`：

```yaml
schema_version: "1.0"
extractor: "file-extract"
extractor_version: "0.1.0"
extracted_at: "2026-04-12T14:30:00"
```

sage-wiki 读到 schema_version 不匹配时 warn，不阻断。

### 契约文档

- sage-wiki 侧：`docs/custom-extractors.md` — 定义文件格式要求
- file-extract 侧：`docs/output-format.md` — 遵守契约并标注版本

### 路径映射规则（V2 修复）

.pre-extracted/files/ 下的路径必须与 raw/ 下的相对路径完全一致：

```
raw/inbox/投资建议书.pdf
  → .pre-extracted/files/inbox/投资建议书.pdf.md
  → .pre-extracted/files/inbox/投资建议书.pdf.numbers.yaml
  → .pre-extracted/files/inbox/投资建议书.pdf.meta.yaml

raw/regulation/股票发行/注册办法.pdf
  → .pre-extracted/files/regulation/股票发行/注册办法.pdf.md
  → .pre-extracted/files/regulation/股票发行/注册办法.pdf.numbers.yaml
```

sage-wiki 匹配逻辑：对 manifest 中的每个源文件路径（如 `raw/inbox/投资建议书.pdf`），去掉 `raw/` 前缀，在 `.pre-extracted/files/` 下查找对应 `.md` 文件。此规则写入两侧契约文档。

## 3. 架构总览

```
┌─────────────────────────────────────────────────────────────────┐
│                wiki-manage skill (维护编排层)                     │
│  1.创建  2.维护  3.体检  4.底座  5.看板                          │
│                                                                  │
│  维护流程：                                                      │
│  file-extract(Bash) → facts import(CLI) → compile → lint → fix  │
└────────┬──────────────────────────────┬──────────────────────────┘
         │                              │
   调用 Python CLI                调用 Go CLI
         │                              │
         ▼                              ▼
┌────────────────────┐  ┌──────────────────────────────────────────┐
│  file-extract      │  │  sage-wiki                                │
│  (独立 GitHub repo) │  │  (fork，PR 回上游)                        │
│                    │  │                                           │
│  Phase A: 机械提取 │  │  导入层（新增）                           │
│  → .md + .meta     │  │  ├ preextract.go → 读 .md                │
│                    │  │  ├ facts/import.go → 读 .numbers.yaml    │
│  Phase B: AI 数字  │  │  └ linter pass → 读 conflicts.yaml       │
│  → .numbers.yaml   │  │                                           │
│                    │  │  存储层（新增）                           │
│  聚合              │  │  └ facts 表 (SQLite)                      │
│  ├ conflicts.yaml  │  │                                           │
│  └ extract-meta    │  │  查询层（新增）                           │
└────────────────────┘  │  ├ facts query/stats/delete               │
    不知道               │  ├ source show/list                       │
    sage-wiki 存在       │  ├ coverage                               │
                         │  └ hub facts/coverage                     │
                         │                                           │
                         │  Skill 层（新增，放 skills/ 目录）       │
                         │  ├ wiki/SKILL.md — 对话式知识助手        │
                         │  └ wiki-manage/SKILL.md — 维护编排       │
                         └──────────────────────────────────────────┘

┌──────────────────────────────────────┐
│  wiki skill (下游消费层)              │
│  对话式触发，面向 template-writing    │
│  多命令编排 + 智能摘要 + 时效提醒    │
└──────────────────────────────────────┘
```

## 4. 数据流

### file-extract 产出 → sage-wiki 消费映射

| file-extract 产出 | sage-wiki 入口 | 消费方式 |
|---|---|---|
| `files/{path}.md`（带 frontmatter） | `extract.Extract()` | 解析 frontmatter，confidence≠low 则替代 Go 引擎 |
| `files/{path}.numbers.yaml` | `facts import` | 规范化后写入 SQLite facts 表 |
| `conflicts.yaml` | linter 新增 pass | 报告数字矛盾，不入知识库 |
| `extract-meta.yaml` | `facts import` / `compile` | 版本检查 + 提取时间戳 |
| `files/{path}.meta.yaml` | 不读 | 质量信号已在 .md frontmatter 中 |
| `number-registry.yaml` | 不读 | facts 表替代此功能 |
| `extract-manifest.yaml` | `coverage` 命令读取 | 提取覆盖率检查 |
| `extract-report.yaml` | 不读 | 运维诊断，看板可选展示 |

### .md frontmatter 格式

```yaml
---
pre_extracted: true
confidence: high          # high/medium/low
engine: markitdown        # 使用的提取引擎
original_path: /abs/path/to/source.pdf
original_hash: sha256:abc123...
---
```

### .numbers.yaml schema

```yaml
numbers:
  - value: "367.28万美元"           # 原文表述
    numeric: 3672800                # 纯数字（基本单位）
    sign: positive
    number_type: monetary           # config 可配置枚举
    certainty: exact                # exact/approximate/range
    entity: "希烽光电"              # 所属主体（unknown fallback）
    entity_type: company            # config 可配置枚举
    period: "2023"                  # 时间范围（unknown fallback）
    period_type: actual             # config 可配置枚举
    semantic_label: "向Finisar销售金额"
    source_file: "投资建议书.pdf"
    source_location: "P15 表格"
    context_type: table             # table/paragraph/footnote/heading/list
    exact_quote: "2023年向Finisar销售光芯片367.28万美元"
    verified: true
    hallucination_suspect: false
    extraction_method: ai_enrichment
```

- entity/entity_type/period/period_type 为 required 字段，无法判断时填 "unknown"
- finance profile 额外新增 entity_role: issuer/buyer/seller/target/investor/manager

## 5. sage-wiki CLI 命令

### 新增命令（9 个）

| # | 命令 | 用途 | PR | 关键设计 |
|---|------|------|-----|---------|
| N1 | `source show <path> [--json]` | 原文内容+provenance | PR1 | 有 pre-extracted→返回文本；无且文本格式→返回原文；无且二进制→返回元信息+提示"请先运行提取"（V11） |
| N2 | `source list [--json]` | 源文件状态清单 | PR1 | pre-extracted/compiled/facts 三列 |
| N3 | `facts import <dir>` | 导入 .numbers.yaml | PR2 | upsert 语义，逐文件事务，规范化 entity/label，记录 schema_version |
| N4 | `facts query [flags] [--json]` | 多维 facts 查询 | PR2 | --entity/--period/--label/--number-type/--source/--entity-type + --limit/--count-only + 返回 source_project + exact_quote |
| N5 | `facts stats [--json]` | facts 汇总统计 | PR2 | 实体分布、period 分布、碎片度报告 |
| N6 | `facts delete --source <file>` | 按源文件删除 facts | PR2 | 级联清理 |
| N7 | `coverage [--entity X] [--json]` | 三层覆盖率 | PR2 | 每层独立报告，缺失层报 N/A（V9）；含 staleness_warning（V8）；检查全局导入过期（V10） |
| N8 | `hub facts query [flags]` | 跨项目 facts 查询 | PR2+ | 结果标注 source_project |
| N9 | `hub coverage [flags]` | 跨项目覆盖率 | PR2+ | 聚合所有项目 |

### 现有命令增强（4 个）

| # | 命令 | 增强 | PR |
|---|------|------|-----|
| E1 | `search` | `--scope local\|global\|all` 标志 | PR2 |
| E2 | `query` | 同上 + 返回 source_project | PR2 |
| E3 | `lint` | 新增 numeric-contradiction pass + orphan-facts pass | PR2 |
| E4 | `compile` | 检测已删除源文件 → 提示清理关联数据 | PR2 |

### facts 表 SQL schema

```sql
CREATE TABLE IF NOT EXISTS facts (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  source_file     TEXT NOT NULL,
  source_project  TEXT DEFAULT 'local',
  value           TEXT NOT NULL,
  numeric         REAL,
  sign            TEXT DEFAULT 'positive',
  number_type     TEXT,
  certainty       TEXT DEFAULT 'exact',
  entity          TEXT DEFAULT 'unknown',
  entity_type     TEXT,
  period          TEXT DEFAULT 'unknown',
  period_type     TEXT DEFAULT 'unknown',
  semantic_label  TEXT,
  source_location TEXT,
  context_type    TEXT,
  exact_quote     TEXT,
  verified        BOOLEAN DEFAULT 0,
  extraction_method TEXT,
  schema_version  TEXT,
  imported_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
  quote_hash    TEXT,                   -- SHA256(exact_quote)[:16]，V7 修复
  UNIQUE(source_file, entity, semantic_label, period, numeric, quote_hash)
);

CREATE INDEX idx_facts_entity ON facts(entity);
CREATE INDEX idx_facts_period ON facts(period);
CREATE INDEX idx_facts_source ON facts(source_file);
CREATE INDEX idx_facts_label  ON facts(semantic_label);
```

### Schema 泛化策略（PR 友好）

枚举值不硬编码到 Go 代码，放 config.yaml：

```yaml
facts:
  number_types: [monetary, percentage, ratio, count, measure]
  entity_types: [company, industry, market, product, region, fund, government, person, other]
  period_types: [actual, forecast, estimate, guidance, consensus, unknown]
  label_aliases:
    净利润: ["Net Profit", "Net Income", "归母净利润"]
    营业收入: ["Revenue", "Sales", "营收", "主营业务收入"]
  entity_aliases:
    希烽光电: ["XiFeng Optics", "希烽", "XiFeng"]
```

Go 代码校验枚举时读 config，上游默认通用集合，fork 通过 config 扩展金融枚举。

### 中英文概念关联机制

两阶段规范化（V1 修复：解决鸡生蛋问题）：

**阶段 1：import 时（config 驱动）**
1. **entity 规范化**：查 config entity_aliases → 命中用 canonical name，未命中保留原名
2. **semantic_label 规范化**：查 config label_aliases → 命中用 canonical label，未命中保留原 label

**阶段 2：compile 时（ontology 驱动）**
3. compile 构建 ontology 后，回扫 facts 表中未规范化的 entity
4. 查 ontology entities + aliases → 命中则更新 facts 表中的 entity 为 canonical name
5. 仍未命中的标记 `_unresolved` 前缀，深度体检时报告

**查询兜底**：facts query 加 `--fuzzy` 标志，搜不到精确匹配时尝试 aliases 反查

**aliases 存储（V12 修复）**：label_aliases 和 entity_aliases 从 config.yaml 独立为 `facts-aliases.yaml`，config.yaml 只保留：

```yaml
facts:
  enabled: true
  aliases_file: "facts-aliases.yaml"   # 默认值
  number_types: [monetary, percentage, ratio, count, measure]
  entity_types: [company, industry, market, product, region, fund, government, person, other]
  period_types: [actual, forecast, estimate, guidance, consensus, unknown]
```

### summarize 注入 facts 策略（V6 修复）

compile 阶段 summarize 每个源文件时，从 facts 表查询该文件的数字，注入到 LLM prompt：

**注入格式**（放在 user prompt 的 context section）：

```
## 该文件的关键数字（共 N 条，按类型分组展示 top 项）

### 金额类
| 指标 | 数值 | 来源位置 |
|------|------|---------|
| 营业收入 | 5.2亿元 | P8 表格 |
| 净利润 | 0.8亿元 | P8 表格 |

### 百分比类
| 指标 | 数值 | 来源位置 |
| 毛利率 | 35.2% | P12 段落 |

要求：摘要中引用数字时使用上述精确数值，不要四舍五入或推测。
```

**筛选规则**：
- 每个 number_type 取 top 10 by certainty（exact 优先）
- 总量不超过 50 条（避免 prompt 过长）
- hallucination_suspect=true 的不注入

## 6. wiki skill（对话式知识助手）

**定位**：面向 template-writing 和用户的知识查询入口。用自然语言触发，多命令编排，智能摘要。

**位置**：`sage-wiki repo: skills/wiki/SKILL.md`，PR 回上游。

### 触发方式

- 自然语言：wiki 专属语义触发（"知识库中查"、"wiki 里找"、"查原文"、"这个数字的出处"），避免与 ifind/china-stock-analysis 等 skill 冲突（V4 修复）
- `/wiki` 前缀：`/wiki search X`、`/wiki facts --entity X` 等快捷方式（无冲突）
- 项目自动识别：根据当前工作目录向上搜索 config.yaml

### 核心能力

| 能力 | 说明 | 涉及 CLI |
|------|------|---------|
| 主题聚合 | 某主体下的全部内容+数字，完整获取 | facts query + search + coverage |
| 回源追溯 | 某个数字/结论的原文出处 | facts query → source show |
| 覆盖率检查 | 某主题的信息是否完整 | coverage + diff |
| 智能摘要 | 大结果集先统计再按需展开 | facts query --count-only → 分布统计 → 子集查询 |
| 时效提醒 | 查询后检查 diff，有未处理文件时提醒 | diff |
| scope 控制 | local / global / all | --scope flag |

### 使用场景示例

| 用户说 | skill 做什么 |
|--------|-------------|
| "目标公司的财务数据" | facts query --entity X → 按 period 分组展示 → 检查 coverage |
| "这个数字哪来的" | facts query 定位 → source show 返回原文 + exact_quote |
| "有没有关于并购审批的材料" | search + coverage → 列出已有内容 + 缺口 |
| "两个报告的营收数字对不上" | facts query 同 label 不同 source → 展示 exact_quote 对比 |

## 7. wiki-manage skill（维护编排层）

**定位**：全生命周期管理。基于 wiki-improve 改造。

**位置**：`sage-wiki repo: skills/wiki-manage/SKILL.md`，PR 回上游（通用部分），fork 扩展 file-extract 步骤。

### 入口菜单

```
请选择操作：
1. 创建项目 — 交互式搭建 wiki 目录+配置+首次编译
2. 日常维护 — 提取→导入→编译→lint→wikilink修复
3. 深度体检 — 日常维护 + 覆盖度/去重/config治理/facts审计
4. 更新底座 — 合并 sage-wiki 上游 + 重编二进制
5. 状态看板 — wiki + facts + 提取三层状态
```

### 工作流 1：创建项目

```
对话式（在项目文件夹中）：
1. 检测当前目录是否已有 wiki 子目录
2. 确认项目名称
3. 确认原始文件目录（raw/）
4. 是否链接全局 wiki？→ config.yaml extends
5. sage-wiki init + hub add
6. 创建统一目录结构
7. 如果 raw/ 有文件 → 提取估算 → 用户确认 → 编译
8. 输出创建报告
```

### 工作流 2：日常维护

```
模式选择（进入时询问）：
  a. 增量（默认）— 只处理新增/修改的文件
  b. 全量重建 — 重新提取+编译所有文件（模板修改后/迁移后使用）

确认提示：
  "检测到 N 个待处理文件。模式：增量/全量？
   步骤：提取→导入→编译→lint→wikilink，确认？(y/跳过某步骤请说明)"

Step 0:   底座更新检查（同 wiki-improve）
Step 0b:  API 健康探针
Step 0.4: 目录体系迁移（首次运行）
Step 0.5: Inbox 处理
Step 0.6: file-extract 提取（新增，fork 扩展）
  → 检查 raw/ vs .pre-extracted/，识别新增/修改文件
  → 增量：python3 -m file_extract --batch raw/ --output .pre-extracted/
  → 全量：python3 -m file_extract --batch raw/ --output .pre-extracted/ --fresh
Step 0.7: facts import（新增）
  → 增量：sage-wiki facts import .pre-extracted/ --json（upsert，只更新变化文件）
  → 全量：sage-wiki facts delete --all → sage-wiki facts import .pre-extracted/ --json
  → 报告新增/更新/跳过
Step 0.8: 源文件清理检查（新增）
  → 检测已删除源文件 → 提示清理 facts + wiki 文章
Step 1:   编译
  → 增量：sage-wiki compile --project ./
  → 全量：sage-wiki compile --fresh --project ./
  → 预提取文件优先，注入 facts 到 summarize prompt
Step 2:   Lint（新增 orphan-facts + numeric-contradiction pass）
Step 3:   Wikilink 修复
Step 4:   报告（增加 facts 统计）
```

### 工作流 3：深度体检

```
现有（同 wiki-improve）：概念覆盖度/去重 + config 治理
新增：
- facts 覆盖率审计（哪些源文件有 .md 但没 .numbers.yaml）
- entity 碎片度报告（疑似同一实体的不同名称）
- 跨项目 facts 一致性（同 entity+label+period 不同 value）
- orphan facts 检测（facts 指向不存在的源文件）
- unresolved entity/label 统计（未被规范化的项）
```

### 工作流 4：更新底座（同 wiki-improve，不变）

### 工作流 5：状态看板

```
Wiki 看板 — {项目名}
├── 链接: 全局 wiki ✓/✗
├── 源文件: N 个
│   ├── 提取: X/N (K 个待提取)
│   ├── 编译: Y/N (M 个待编译)
│   └── facts: Z/N (J 个已导入)
├── Facts: 总条数 / 实体数 / 矛盾数
│   ├── Entity 碎片度: K 组疑似重复
│   └── 时间分布: 2022=X, 2023=Y, 2024=Z
├── 概念: 文章数 / 关系数
├── Lint: N 个未修复
└── 上次维护: 时间
```

### 断点恢复

维护状态文件 `.wiki-manage-state.json`：

```json
{
  "step": "0.7",
  "completed": ["0", "0b", "0.5", "0.6"],
  "started_at": "2026-04-12T14:30:00",
  "project": "/path/to/wiki"
}
```

重新进入时从断点恢复，不重复已完成步骤。

## 8. 多项目架构

### 项目层级

```
全局 wiki（~/claude-workspace/wiki/）
  法规 / 研报 / 访谈 等通用知识
  ↕ 双向链接
项目 wiki A（~/claude-workspace/projects/ProjectA/wiki/）
项目 wiki B（~/claude-workspace/projects/ProjectB/wiki/）
```

### 链接机制

项目 config.yaml：

```yaml
extends: "/Users/kellen/claude-workspace/wiki"  # 全局 wiki 路径
```

编译时从全局库导入：entities + facts + ontology，标记 `source_project: _global`。

导入时记录全局 wiki 编译时间戳到项目 wiki.db metadata 表（V10 修复）。status/coverage 命令比对时间戳，全局更新后提醒"全局数据已更新，建议重编项目 wiki"。

extends 目标不存在时（V5 修复）：wiki.db 不存在或为空 → warn + 跳过导入，继续编译。wiki-manage 创建项目时如果配了 extends，检查全局 wiki 是否已编译。

### 知识图谱三种视角

| 视角 | 命令 | 数据范围 |
|------|------|---------|
| 仅项目 | `--scope local`（默认在项目目录下） | 项目自有数据 |
| 仅全局 | `--scope global` | 全局 wiki 数据 |
| 合并 | `--scope all` | 项目 + 全局，标注来源 |

## 9. 统一目录架构

全局 wiki 和项目 wiki 使用完全相同的结构：

```
{wiki-root}/
├── raw/                          # 源文件
│   ├── {type}/{topic}/           # 分类存放
│   └── inbox/                    # 待分类
├── .pre-extracted/               # file-extract 输出
│   ├── extract-meta.yaml         # 契约版本
│   ├── files/                    # 逐文件产出
│   │   ├── {path}.md
│   │   ├── {path}.numbers.yaml
│   │   └── {path}.meta.yaml
│   ├── conflicts.yaml
│   ├── extract-manifest.yaml
│   └── extract-report.yaml
├── wiki/                         # sage-wiki 编译产出
│   ├── concepts/
│   └── summaries/
├── prompts/                      # 自定义模板（项目可继承全局）
├── config.yaml                   # extends: 全局 wiki 路径
├── .manifest.json
├── .wiki-manage-state.json       # 维护断点状态
└── wiki.db                       # SQLite（含 facts 表）
```

## 10. GitHub 策略

### 仓库关系

```
kellen/file-extract (独立 repo)
    ↓ 产出文件
kellen/sage-wiki (fork) ←PR── xoai/sage-wiki (上游)
    ├── Go 代码改动（PR1/PR2/PR3）
    └── skills/wiki/ + skills/wiki-manage/（PR4）
```

### PR 计划

| 顺序 | 分支 | 内容 | 大小 |
|------|------|------|------|
| PR1 | `feature/pre-extract-support` | preextract.go + source show/list | ~150 行 |
| PR2 | `feature/facts-layer` | facts 表 + import/query/stats/delete + coverage + lint 增强 + compile 增强 | ~500 行 |
| PR3 | `feature/docs` | docs/custom-extractors.md + docs/structured-facts.md | ~100 行 |
| PR4 | `feature/skills` | skills/wiki/SKILL.md + skills/wiki-manage/SKILL.md | ~300 行 |

PR1 → PR2 串行提交（PR2 依赖 PR1）。PR3/PR4 在 PR1+PR2 合并后提交。

如果 PR2 被要求拆分（V3），预案：PR2a（facts 存储+CRUD+import ~300 行）+ PR2b（CLI 命令+coverage+lint+compile ~200 行）。

### 测试策略（V13 修复）

每个 PR 必须附测试：

| PR | 测试文件 | 测试场景 |
|----|---------|---------|
| PR1 | preextract_test.go | 有提取/无提取/低confidence/损坏frontmatter/路径映射 |
| PR2a | facts_test.go | CRUD + upsert去重 + query filter组合 + alias规范化 |
| PR2a | import_test.go | YAML解析 + 版本检查 + 逐文件事务 + 错误恢复 |
| PR2b | coverage_test.go | 三层覆盖率 + 缺失层N/A + staleness |
| PR2b | lint passes 测试 | numeric-contradiction + orphan-facts |

### file-extract 仓库化

推到 GitHub 作为独立项目：
- pyproject.toml（pip install 支持 + CLI 入口点）
- LICENSE（MIT）
- README.md（面向通用开发者，不绑定 sage-wiki）
- docs/output-format.md（契约文档）

## 11. file-extract 改动清单

### 已完成

- pipeline.py: frontmatter 输出 + Phase B 后 cross-file entity merge
- numext/validator.py: entity/period required + 交叉验证 + 枚举检查
- numext/extractor.py: normalize_entity + deduplicate 加 entity + merge_entities
- aggregate.py: detect_conflicts key (entity, label, period) + 容差比较

### 待完成

| # | 文件 | 内容 | 优先级 |
|---|------|------|--------|
| 1 | `cli.py` | CLI 入口：argparse（--batch/--output/--phase/--profile/--config） | **P0 前置阻塞**（V15） |
| 2 | `pyproject.toml` | pip install + entry_points: `file-extract = "file_extract.cli:main"` | **P0** |
| 3 | `prompts/base.yaml` | Phase B prompt template + schema | P1 |
| 4 | `prompts/profiles/finance.yaml` | 金融 profile（entity_role + few-shot） | P1 |
| 5 | `docs/output-format.md` | 输出格式规范（契约文档，含路径映射规则） | P1 |
| 6 | `LICENSE` | MIT | P2 |
| 7 | `README.md` | 面向通用开发者 | P2 |

## 12. Scenario 分析发现

### 第一轮：集成场景（10 轮，brainstorm 阶段）

| # | 场景 | 严重度 | 设计应对 |
|---|------|--------|---------|
| S1 | Schema 版本漂移静默丢数据 | HIGH | extract-meta.yaml schema_version + import 时检查 |
| S2 | Entity 名称碎片化导致聚合不完整 | CRITICAL | config entity_aliases/label_aliases + 两阶段规范化 + stats 碎片度报告 |
| S3 | 增量重跑 facts 重复 | HIGH | UNIQUE 约束 + upsert 语义 |
| S4 | 源文件删除后残留数据 | CRITICAL | facts delete --source + compile 级联清理 + lint orphan-facts |
| S5 | 跨项目 label 口径冲突 | CRITICAL | 返回 source_project + exact_quote + 编译时冲突检测 |
| S6 | 大结果集灌满 CC 上下文 | HIGH | --limit/--count-only + wiki skill 智能摘要 |
| S7 | 维护中断恢复 | HIGH | .wiki-manage-state.json + 逐文件事务 |
| S8 | 查询结果时效性盲区 | CRITICAL | wiki skill 交叉检查 diff + coverage staleness_warning |
| S9 | PR 回上游泛化约束 | MEDIUM | 枚举放 config.yaml 不硬编码 |
| S10 | 并发读写 facts | MEDIUM | WAL + 逐文件事务 |

### 第二轮：Spec 验证（15 轮，spec 完成后）

| # | 场景 | 严重度 | 设计应对 | 已修入 spec |
|---|------|--------|---------|------------|
| V1 | entity 规范化鸡生蛋（ontology 在 compile 后才有） | HIGH | 两阶段规范化：import 用 config aliases，compile 后用 ontology 回扫 | ✓ Section 5 |
| V2 | .pre-extracted/ 路径映射规则未定义 | CRITICAL | 路径 = raw/ 下相对路径，写入契约文档 | ✓ Section 2 |
| V3 | PR2 太大可能被要求拆分 | MEDIUM | 预拆为 PR2a(存储)+PR2b(命令)，或等反馈再拆 | ✓ Section 10 |
| V4 | wiki skill 触发词与 ifind 冲突 | HIGH | description 用 wiki 专属语义，避免泛化词 | ✓ Section 6 |
| V5 | extends 目标 wiki.db 不存在时行为未定义 | MEDIUM | warn + 跳过导入 | ✓ Section 8 |
| V6 | summarize.go 注入 facts 的 prompt 未定义 | HIGH | 按 number_type 分组 top 10，user prompt context section，指令"引用精确数值" | ✓ Section 4 补充 |
| V7 | UNIQUE 去重键误杀合法数据 | MEDIUM | 去重键加 quote_hash | ✓ Section 5 schema |
| V8 | sage-wiki init 参数未调研 | MEDIUM | writing-plans 时调研 init --help | Section 14 |
| V9 | coverage 缺失层行为未定义 | MEDIUM | 每层独立报告，缺失层报 N/A | ✓ Section 5 N7 |
| V10 | 全局 wiki 更新后项目导入过期 | HIGH | 导入时记录时间戳，status/coverage 检查过期 | ✓ Section 8 |
| V11 | source show 处理二进制文件 | LOW | 有提取→文本，无提取→元信息+提示 | ✓ Section 5 N1 |
| V12 | config.yaml facts 块膨胀 | MEDIUM | aliases 独立为 facts-aliases.yaml | ✓ Section 5 |
| V13 | Go 测试策略未定义 | HIGH | 每个 PR 附测试清单 | ✓ Section 10 补充 |
| V14 | 勾选制 CC 交互适配 | LOW | 默认全选 + 自然语言例外 | ✓ Section 7 |
| V15 | file-extract CLI 入口点不存在 | CRITICAL | P0 前置任务：cli.py + pyproject.toml entry_points | ✓ Section 11 |

**统计：** 25 轮共 6 CRITICAL + 9 HIGH + 7 MEDIUM + 3 LOW

## 13. 设计决策记录

| # | 决策 | 理由 |
|---|------|------|
| 1 | 集成方式选文件契约（方式 A） | 最松耦合、PR 友好、不引入进程管理复杂度 |
| 2 | .pre-extracted/ 路径放 config.yaml 不放环境变量 | 跟着项目走，不依赖系统环境 |
| 3 | entity/period 为 required + unknown fallback | schema 一致，下游过滤简单 |
| 4 | facts 表放 sage-wiki SQLite | 下游通过 CLI 统一查询 |
| 5 | conflicts.yaml 喂 linter 不喂 ontology | 矛盾是质量问题不是知识 |
| 6 | 查询全走 CLI，不做 MCP | 用户 skill 体系全部通过 CLI 调用 |
| 7 | compile 前注入 facts 到 summarize prompt | LLM 能引用精确数字 |
| 8 | facts import 用 upsert 语义 | 增量重跑不产生重复 |
| 9 | 枚举值放 config.yaml 不硬编码 | PR 友好，fork 可扩展金融枚举 |
| 10 | 中英文规范化在 import 时做，不在查询时做 | 数据入库就干净，查询天然一致 |
| 11 | wiki skill 定位对话式助手，不是 CLI 路由 | 用户主要在 CC 对话中使用 |
| 12 | wiki / wiki-manage 放 sage-wiki repo | 可 PR 回上游，其他用户也能用 |
| 13 | 跨项目知识编译时导入 | 统一数据流方向，避免运行时跨库 |
| 14 | 每个 PR 独立分支 | review 聚焦，减少冲突 |
| 15 | file-extract 独立 GitHub repo | PR3 文档可引用，不绑定 sage-wiki |
| 16 | facts import 逐文件事务 | 平衡恢复友好和一致性，WAL 保证并发读 |
| 17 | article_max_tokens 调至 16000 | MiniMax M2.5 支持 196K 输出，给 LLM 足够空间包含数字 |
| 18 | entity 规范化分两阶段（import+compile） | 解决鸡生蛋：import 时 ontology 为空，compile 后回扫（V1） |
| 19 | .pre-extracted/ 路径 = raw/ 相对路径 | 契约明确，避免匹配失败（V2） |
| 20 | UNIQUE 去重键加 quote_hash | 保留同数字不同出处（V7） |
| 21 | aliases 独立为 facts-aliases.yaml | 减少 config.yaml 膨胀，PR 友好（V12） |
| 22 | wiki skill 用 wiki 专属触发词 | 避免与 ifind 等 skill 路由冲突（V4） |
| 23 | 全局导入记录时间戳 | 检测过期，提醒重编（V10） |
| 24 | file-extract CLI 入口为 P0 前置 | wiki-manage 的核心流程依赖 Bash 调用（V15） |

## 14. 待继续细化（writing-plans 阶段）

- Hub facts query / hub coverage 的遍历实现
- 编译时全量导入全局库的 imported_entries 表 schema
- wiki skill SKILL.md 的完整 description 和触发词列表
- wiki-manage 从 wiki-improve 的具体改造差异清单
- sage-wiki init 参数调研（V8）
- compile 阶段 facts 回扫规范化的具体实现（V1 阶段 2）

### 已在本轮解决（从待细化移除）

- ~~summarize.go 注入数字的 prompt 设计~~ → Section 4 补充（V6）
- ~~file-extract CLI 入口点设计~~ → Section 11 P0 任务（V15）
- ~~Go 测试覆盖策略~~ → Section 10 测试策略（V13）
