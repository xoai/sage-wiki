# sage-wiki Prompt 模板系统全链路可定制化设计

**日期**: 2026-04-09
**状态**: 待审核
**分支**: feature/chinese-localization

---

## 1. 问题陈述

sage-wiki 当前编译管线有三个结构性问题：

1. **模板系统覆盖不完整**: 5-pass 编译管线中，仅 Pass 1 (summarize) 使用 `prompts.Render()` 模板系统。Pass 2 (extract_concepts) 和 Pass 3 (write_article) 的 prompt 硬编码在 Go 源码中（`concepts.go:82-107`、`write.go:198-244`），`prompts/` 目录的 override 对它们无效。
2. **内容类型检测过于粗糙**: `DetectSourceType()` 仅按文件后缀判断（`.pdf`→"paper", `.md`→"article"），无法区分法规 PDF、研报 PDF、纪要 PDF 等语义差异巨大的文档。
3. **本体关系类型固化**: 8 种关系类型以 Go 常量 + SQLite CHECK 约束双重硬编码，无法通过配置扩展。

### 影响范围

| 内容类型 | 数量 | 当前处理方式 | 问题 |
|---------|------|------------|------|
| 投行法规 (.md) | 1008 | summarize_article | 通用结构，缺少法规专属字段（适用范围、生效日期、修订历史） |
| 投行法规 (.pdf) | 137 | summarize_paper | 同上 |
| 卖方研报 (.pdf) | ~25 | summarize_paper | 缺少研报字段（评级、目标价、核心假设） |
| 专家访谈 (.pdf) | ~38 | summarize_paper | 缺少访谈字段（专家背景、关键数据点、行业判断） |
| 交易公告 (.pdf) | 82 | summarize_paper | 缺少公告字段（交易结构、关键条款、审批进度） |

---

## 2. 方案概览

**一句话**: 让编译管线的全部 3 个 prompt（summarize/extract/write）都走模板系统，同时让内容类型检测和关系类型都变成配置驱动。

### 设计原则

- **上游友好**: 对上游有价值的改动（模板化、类型检测签名）提 PR；中文定制留 fork
- **配置驱动**: 内容类型和关系类型通过 config.yaml 管理，不改 Go 源码即可扩展
- **治理闭环**: 新类型有积累→统计→审核→收敛的机制，防止无限膨胀
- **向后兼容**: 未配置时行为与当前完全一致

---

## 3. Go 代码变更

### G1: DetectSourceType 增加内容感知 (`internal/extract/extract.go`)

**当前** (line 261-289):
```go
func DetectSourceType(path string) string {
    ext := strings.ToLower(filepath.Ext(path))
    switch ext { ... }
}
```

**改为**:
```go
func DetectSourceType(path string, contentHead string, typeSignals []config.TypeSignal) string {
    // 1. 先用 config 中的 type_signals 匹配（文件名关键词 + 内容前 500 字）
    for _, sig := range typeSignals {
        if matchesSignal(path, contentHead, sig) {
            return sig.Type
        }
    }
    // 2. fallback 到原来的后缀逻辑
    ext := strings.ToLower(filepath.Ext(path))
    switch ext { ... }
}

func matchesSignal(path, contentHead string, sig config.TypeSignal) bool {
    filename := strings.ToLower(filepath.Base(path))
    contentLower := strings.ToLower(contentHead)
    // 文件名关键词匹配
    for _, kw := range sig.FilenameKeywords {
        if strings.Contains(filename, strings.ToLower(kw)) {
            return true
        }
    }
    // 内容关键词匹配（至少命中 N 个）
    hits := 0
    for _, kw := range sig.ContentKeywords {
        if strings.Contains(contentLower, strings.ToLower(kw)) {
            hits++
        }
    }
    return hits >= sig.MinContentHits
}
```

**PR 归属**: A 类（上游通用改进）——签名变更 + fallback 逻辑，不含中文关键词

### G1b: DetectSourceType 测试 (`internal/extract/extract_test.go`)

覆盖场景：
- 无 typeSignals 时行为与原版一致
- 文件名匹配命中
- 内容关键词匹配（含 MinContentHits 阈值）
- 混合匹配优先级

### G2: 调用方传入 contentHead (`diff.go` / `pipeline.go` / `ingest.go`)

三个调用点需要修改：

| 文件 | 行号 | 变更 |
|------|------|------|
| `internal/compiler/diff.go` | ~73 | 读取文件前 500 字传入 |
| `internal/compiler/pipeline.go` | ~851 | 同上 |
| `internal/wiki/ingest.go` | ~114 | 同上 |

每个调用点的模式相同：
```go
contentHead := readHead(filePath, 500) // 新增 helper
sourceType := extract.DetectSourceType(filePath, contentHead, cfg.TypeSignals)
```

**PR 归属**: A 类（与 G1 同 PR）

### G3: write.go 改用模板系统 (`internal/compiler/write.go`)

**当前** (line 198-244): `buildArticlePrompt()` 用 `strings.Builder` 硬编码

**改为**:
```go
func buildArticlePrompt(concept ExtractedConcept, existing string, related []string, learnings string) string {
    prompt, err := prompts.Render("write_article", prompts.WriteArticleData{
        ConceptName:     formatConceptName(concept.Name),
        ConceptID:       concept.Name,
        Sources:         strings.Join(concept.Sources, ", "),
        RelatedConcepts: related,
        ExistingArticle: existing,
        Learnings:       learnings,
        Aliases:         strings.Join(concept.Aliases, ", "),
        SourceList:      strings.Join(quoteYAMLList(concept.Sources), ", "),
        RelatedList:     strings.Join(related, ", "),
    })
    if err != nil {
        // fallback 到当前硬编码逻辑，保证向后兼容
        return buildArticlePromptLegacy(concept, existing, related)
    }
    return prompt
}
```

关键点：
- `prompts.WriteArticleData` struct 已在 `prompts.go:167-180` 定义，无需新增
- 上游已有 `write-article.txt` 模板但未被使用，此 PR 让它生效
- fallback 到 legacy 实现确保无模板文件时不 break

**PR 归属**: A 类（让已有的模板系统真正生效，上游明确受益）

### G4: concepts.go 改用模板系统 (`internal/compiler/concepts.go`)

**当前** (line 82-107): `fmt.Sprintf` 硬编码

**改为**:
```go
prompt, err := prompts.Render("extract_concepts", prompts.ExtractData{
    ExistingConcepts: strings.Join(dedup, ", "),
    Summaries:        strings.Join(summaryTexts, "\n\n---\n\n"),
})
if err != nil {
    // fallback 到当前硬编码逻辑
    prompt = fmt.Sprintf(...)  // 保留原始代码作为 legacy fallback
}
```

- `prompts.ExtractData` struct 已在 `prompts.go:162-165` 定义
- 上游已有 `extract-concepts.txt` 模板但未使用

**PR 归属**: A 类（同 G3，同一个 PR）

### G5: ontology.go 关系类型配置化 (`internal/ontology/ontology.go`)

**当前** (line 21-29): 8 个 `const`

**改为**:
```go
// DefaultRelationTypes 保留默认值，config 可扩展
var DefaultRelationTypes = []string{
    "implements", "extends", "optimizes", "contradicts",
    "cites", "prerequisite_of", "trades_off", "derived_from",
}

// ValidRelation 检查关系类型是否有效（从 config 或默认值）
func ValidRelation(relation string, configured []RelationTypeDef) bool {
    for _, rt := range configured {
        if relation == rt.Name {
            return true
        }
        for _, syn := range rt.Synonyms {
            if relation == syn {
                return true
            }
        }
    }
    return false
}

// NormalizeRelation 将同义词归并到规范名
func NormalizeRelation(relation string, configured []RelationTypeDef) string {
    for _, rt := range configured {
        if relation == rt.Name {
            return rt.Name
        }
        for _, syn := range rt.Synonyms {
            if relation == syn {
                return rt.Name
            }
        }
    }
    return relation // 未识别的原样返回
}
```

**PR 归属**: B 类（提 Issue 讨论）——架构变更，需上游认可方向

### G6: db.go 移除 CHECK 约束 (`internal/storage/db.go`)

**当前** (line 195-197):
```sql
CHECK(relation IN ('implements','extends','optimizes','contradicts',
    'cites','prerequisite_of','trades_off','derived_from'))
```

**改为**: 移除 CHECK 约束，改为应用层验证。

```sql
relation TEXT NOT NULL,
-- application-layer validation via ontology.ValidRelation()
```

新增 migration 逻辑：检测旧表有 CHECK 约束时自动重建表（SQLite 不支持 ALTER TABLE DROP CONSTRAINT）。

**PR 归属**: B 类（与 G5 同 Issue）

### G7: config.go 新增类型配置 (`internal/config/config.go`)

新增 struct：

```go
type TypeSignal struct {
    Type             string   `yaml:"type"`
    FilenameKeywords []string `yaml:"filename_keywords"`
    ContentKeywords  []string `yaml:"content_keywords"`
    MinContentHits   int      `yaml:"min_content_hits"`
}

type RelationTypeDef struct {
    Name     string   `yaml:"name"`
    Synonyms []string `yaml:"synonyms,omitempty"`
}

type OntologyConfig struct {
    RelationTypes    []RelationTypeDef `yaml:"relation_types"`
    MaxRelationTypes int               `yaml:"max_relation_types"`
    MaxContentTypes  int               `yaml:"max_content_types"`
}
```

在 `Config` struct 中新增字段：
```go
type Config struct {
    // ... existing fields ...
    TypeSignals []TypeSignal    `yaml:"type_signals,omitempty"`
    Ontology    *OntologyConfig `yaml:"ontology,omitempty"`
}
```

`Defaults()` 中不设 TypeSignals（空=纯后缀模式，向后兼容）。`Validate()` 检查上限。

**PR 归属**: A 类（TypeSignals 部分，与 G1/G2 同 PR）+ B 类（Ontology 部分，与 G5/G6 同 Issue）

### G8: extractRelations 改用配置 (`internal/compiler/write.go`)

**当前** (line 308-318): `relationPatterns` 硬编码

**改为**: 从 config 读取关系类型列表，keyword 匹配逻辑保持不变，但关系类型从 config 获取。未识别的关系通过 `NormalizeRelation()` 同义词归并。

**PR 归属**: B 类（与 G5/G6 同 Issue）

---

## 4. Prompt 模板

所有模板放在 `~/claude-workspace/wiki/prompts/`，通过 `prompts.LoadFromDir()` override 内嵌模板。

### T1: summarize-regulation.md（法规专属）

```
你是一名法规分析师，负责为知识库创建法规文档摘要。所有输出必须使用中文。

源文件: {{.SourcePath}}
源类型: {{.SourceType}}

请按以下结构撰写摘要：

## 法规概要
法规全称、发布机构、文号、发布/生效日期。

## 适用范围
适用的主体类型、交易类型、市场范围。

## 核心条款
按重要性列出主要条款要点（不超过 10 条）。

## 关键定义
法规中定义的重要术语及其含义。

## 修订沿革
该法规修订/废止了哪些旧规，被哪些新规引用或修订（如文中提及）。

## 关键术语
逗号分隔列表，中英文对照。

摘要控制在 {{.MaxTokens}} token 以内。引用原文条款时标注条号。
```

### T2: summarize-research.md（研报专属）

```
你是一名卖方研究分析师，负责为知识库创建研究报告摘要。所有输出必须使用中文。

源文件: {{.SourcePath}}
源类型: {{.SourceType}}

请按以下结构撰写摘要：

## 报告概要
研报标题、发布机构、分析师、发布日期、覆盖标的。

## 核心观点
研报的主要投资逻辑和结论（评级、目标价如有）。

## 关键数据
支撑结论的核心数据点（财务指标、行业数据等），保留原始数字。

## 核心假设
结论依赖的关键假设（市场假设、增速假设等）。

## 风险因素
研报提及的主要风险。

## 产业链要点
涉及的上下游关系、竞争格局、技术路线（如适用）。

## 关键术语
逗号分隔列表，中英文对照。

摘要控制在 {{.MaxTokens}} token 以内。数字和百分比保留原文精度。
```

### T3: summarize-interview.md（专家访谈专属）

```
你是一名行业研究员，负责为知识库创建专家访谈纪要摘要。所有输出必须使用中文。

源文件: {{.SourcePath}}
源类型: {{.SourceType}}

请按以下结构撰写摘要：

## 访谈概要
访谈主题、专家背景（如文中提及）、访谈日期。

## 关键判断
专家对行业/公司/技术的核心判断和预测。

## 数据点
专家提供的具体数据（市场规模、渗透率、价格、产能等），保留原始数字。

## 产业链洞察
供应链关系、技术路线选择、竞争格局等行业结构信息。

## 争议与不确定性
专家表示不确定或行业存在分歧的领域。

## 关键术语
逗号分隔列表，中英文对照。

摘要控制在 {{.MaxTokens}} token 以内。区分事实陈述和专家判断。
```

### T4: summarize-announcement.md（交易公告专属）

```
你是一名投行分析师，负责为知识库创建上市公司公告摘要。所有输出必须使用中文。

源文件: {{.SourcePath}}
源类型: {{.SourceType}}

请按以下结构撰写摘要：

## 公告概要
公告标题、发布公司（股票代码）、发布日期、公告类型。

## 交易结构
交易标的、交易对手、交易方式（现金/股份/混合）、交易对价。

## 关键条款
业绩承诺、锁定期、特殊条款等核心条款。

## 审批进度
已完成和待完成的审批流程（董事会/股东会/证监会等）。

## 财务影响
对上市公司的财务影响（EPS、资产负债率等，如文中提及）。

## 关键术语
逗号分隔列表，中英文对照。

摘要控制在 {{.MaxTokens}} token 以内。金额保留原文精度，标注币种。
```

### T5-T6: 现有模板保持不变

`summarize-article.md` 和 `summarize-paper.md` 已经是中文版，作为通用 fallback 不需修改。

### T7: write-article.md（已有，确认将被 G3 激活）

当前已存在的中文版 write-article.md 将在 G3 实施后被真正使用。无需修改内容。

### T8: extract-concepts.md（已有，确认将被 G4 激活）

当前已存在的中文版 extract-concepts.md 将在 G4 实施后被真正使用。无需修改内容。

---

## 5. Config 变更 (`config.yaml`)

在现有 config.yaml 中新增：

```yaml
# 内容类型信号——按优先级匹配，第一个命中的生效
type_signals:
  - type: regulation
    filename_keywords: ["法规", "办法", "规定", "准则", "通知", "公告令", "指引"]
    content_keywords: ["第一条", "第二条", "为了规范", "根据《", "自发布之日起施行", "特此通知"]
    min_content_hits: 2
  - type: research
    filename_keywords: ["研报", "深度报告", "行业报告", "专题报告"]
    content_keywords: ["投资评级", "目标价", "盈利预测", "风险提示", "首次覆盖", "买入", "增持", "中性"]
    min_content_hits: 2
  - type: interview
    filename_keywords: ["纪要", "访谈", "调研", "专家"]
    content_keywords: ["专家表示", "Q:", "A:", "问:", "答:", "访谈纪要", "调研纪要"]
    min_content_hits: 2
  - type: announcement
    filename_keywords: ["公告", "报告书", "意见书"]
    content_keywords: ["上市公司", "股票代码", "证券简称", "重大资产重组", "交易对方"]
    min_content_hits: 2

# 本体配置
ontology:
  max_relation_types: 20
  max_content_types: 10
  relation_types:
    - name: implements
      synonyms: ["实现了", "implementation of"]
    - name: extends
      synonyms: ["扩展了", "基于", "extension of", "builds on"]
    - name: optimizes
      synonyms: ["优化了", "改进了", "提升了", "optimization of"]
    - name: contradicts
      synonyms: ["矛盾", "冲突", "挑战了", "conflicts with"]
    - name: cites
      synonyms: ["引用", "参见", "references"]
    - name: prerequisite_of
      synonyms: ["前提", "前置条件", "依赖于", "requires"]
    - name: trades_off
      synonyms: ["取舍", "权衡", "代价是", "at the cost of"]
    - name: derived_from
      synonyms: ["源自", "派生自", "based on"]
    # 新增关系类型
    - name: amends
      synonyms: ["修订", "修改", "废止", "替代", "supersedes"]
    - name: regulates
      synonyms: ["规范", "约束", "适用于", "governs"]
    - name: supplies
      synonyms: ["供应", "提供", "上游", "supplier of"]
    - name: competes_with
      synonyms: ["竞争", "替代方案", "competitor"]
```

---

## 6. wiki-improve 更新

在 wiki-improve skill 中新增以下功能：

### 6.1 未分类文件检测

编译后扫描，报告 `DetectSourceType` 返回默认类型（"article"/"paper"）的文件清单。按文件名聚类，提示用户是否需要新增 type_signal。

### 6.2 被拒关系频次统计

统计编译日志中 `NormalizeRelation` 未命中的关系词频次。高频词（>5 次）列出，提示用户是否新增关系类型或添加同义词。

### 6.3 收敛告警

当 config 中 relation_types 接近 max_relation_types（>80%）或 type_signals 接近 max_content_types 时，在 wiki-improve 报告中告警。

---

## 7. 治理机制

### 内容类型治理

```
新文件进入 raw/
  -> DetectSourceType 按 config 匹配
  -> 命中: 使用对应 summarize 模板
  -> 未命中: 用通用模板 + 记录到 unclassified log
  -> wiki-improve 聚类未分类文件
  -> CC（Claude Code）审核: 是否新增 type_signal + 模板？
  -> 用户确认 -> 更新 config.yaml + 创建新模板
```

### 关系类型治理

```
LLM 输出 article 文本
  -> extractRelations 用 config 中的关系类型匹配
  -> 命中同义词: NormalizeRelation 归并到规范名
  -> 完全未命中: 记录 rejected relation
  -> wiki-improve 统计高频 rejected
  -> CC 审核: 是否新增关系类型？同义词覆盖够吗？
  -> 用户确认 -> 更新 config.yaml
```

### 上限控制

- `max_relation_types: 20` — 超过时 Validate() 报错
- `max_content_types: 10` — 超过时 Validate() 报错
- wiki-improve 在 80% 时预警

---

## 8. PR/Issue 策略

### A 类 PR: "Use prompt template system for write and extract passes"

**范围**: G1 + G1b + G2 + G3 + G4 + G7(TypeSignals 部分)

**卖点**: 上游已经有 `WriteArticleData`/`ExtractData` struct 和 `write-article.txt`/`extract-concepts.txt` 模板，但 compiler 不使用它们。此 PR 让它们生效，并增加内容类型检测的可扩展性。

**向后兼容**: 全部有 fallback——无模板文件或无 type_signals 时行为完全不变。

### B 类 Issue: "Make ontology relation types configurable"

**范围**: G5 + G6 + G7(Ontology 部分) + G8

**理由**: 涉及 DB schema 变更和架构方向，需要上游认可。

### C 类留 fork

- 所有中文 prompt 模板（T1-T4, T5-T8 已有的中文版）
- config.yaml 中的中文关键词
- wiki-improve 的治理扩展

---

## 9. 测试计划

### 单元测试
- G1b: DetectSourceType 各场景
- G5: ValidRelation / NormalizeRelation 同义词归并
- G7: Config 新字段解析、Validate 上限检查

### 集成测试
- 现有 `go test ./...` 全部通过
- 新增: 无 config 时 fallback 行为测试

### 小批量验证
每种内容类型选 5 个文件编译，人工审核：
- 法规: 检查是否提取了条号、生效日期、修订沿革
- 研报: 检查是否保留了评级、目标价、核心假设
- 访谈: 检查是否区分了事实和专家判断
- 公告: 检查是否保留了交易金额和审批进度

---

## 10. 风险与缓解

| 风险 | 缓解 |
|------|------|
| 上游 PR 被拒 | A 类 PR 变更量小且有明确价值（激活已有但未使用的模板系统），被拒风险低。B 类走 Issue 先讨论 |
| 上游更新冲突 | G3/G4 改动集中在 buildArticlePrompt/概念提取两个函数，与其他功能无交叉。wiki-improve 更新时自动检测模板文件变化 |
| 内容类型误判 | MinContentHits 阈值 + 通用 fallback 双保险。误判的最坏结果是用通用模板，不会丢失内容 |
| 关系类型膨胀 | max_relation_types 硬上限 + 同义词归并 + wiki-improve 预警三层控制 |
| SQLite migration | 重建表的 migration 在事务中执行，失败自动回滚。数据量级 ~1300 entity 无性能问题 |
