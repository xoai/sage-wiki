# file-extract × sage-wiki 集成实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 一次性完成 file-extract 与 sage-wiki 的集成，交付 9 个新 CLI 命令 + 4 个命令增强 + 2 个 SKILL.md + 端到端测试 + 4 个 PR

**Architecture:** file-extract (Python CLI) 通过文件契约 (.pre-extracted/) 向 sage-wiki (Go CLI) 传递提取内容和结构化数字。sage-wiki 新增 facts 表存储数字，新增 source/coverage 命令，新增 2 个 linter pass。wiki skill 提供对话式查询，wiki-manage skill 编排维护工作流。

**Tech Stack:** Go 1.22+ / Python 3.11+ / SQLite WAL / YAML / Claude Code SKILL.md

**Spec:** `docs/superpowers/specs/2026-04-12-file-extract-integration-design.md` (v3)

---

## 依赖图

```
Phase 0 (file-extract CLI)
    ↓
Phase 1 (sage-wiki PR1: preextract + source)
    ↓
Phase 2 (sage-wiki PR2a: facts 存储)
    ↓
Phase 3 (sage-wiki PR2b: facts CLI + 质量)
    ↓
Phase 4 (Skills: wiki + wiki-manage)
    ↓
Phase 5 (端到端测试 + 系统自检)
    ↓
Phase 6 (GitHub: PR 提交)
```

Phase 0 是 P0 前置阻塞（V15）。Phase 1-3 串行（PR 依赖）。Phase 4 可在 Phase 3 完成后并行。Phase 5 全部完成后才执行。

---

## Phase 0: file-extract CLI 入口 + 仓库化

### Task 0.1: CLI 入口点

**Files:**
- Create: `~/claude-workspace/skills/file-extract/cli.py`
- Modify: `~/claude-workspace/skills/file-extract/pipeline.py` (确认 main 函数可外部调用)
- Test: 手动 Bash 验证

- [ ] **Step 1: 创建 cli.py**

```python
#!/usr/bin/env python3
"""file-extract CLI — 通用文件提取工具的命令行入口。"""

import argparse
import sys
import os

def main():
    parser = argparse.ArgumentParser(
        prog="file-extract",
        description="通用文件提取工具——将文件提取为结构化文本 + 数字登记簿"
    )
    parser.add_argument("--batch", required=True,
                        help="源文件目录（如 raw/）")
    parser.add_argument("--output", required=True,
                        help="输出目录（如 .pre-extracted/）")
    parser.add_argument("--phase", choices=["a", "b", "ab"], default="ab",
                        help="执行阶段：a=机械提取, b=AI数字提取, ab=全部（默认）")
    parser.add_argument("--profile", default="finance",
                        help="提取 profile（默认 finance）")
    parser.add_argument("--fresh", action="store_true",
                        help="忽略缓存，全量重新提取")
    parser.add_argument("--config", default=None,
                        help="配置文件路径")
    parser.add_argument("--verbose", "-v", action="store_true",
                        help="详细输出")

    args = parser.parse_args()

    # 验证目录存在
    if not os.path.isdir(args.batch):
        print(f"错误：源文件目录不存在: {args.batch}", file=sys.stderr)
        sys.exit(1)

    os.makedirs(args.output, exist_ok=True)

    # 调用 pipeline
    from pipeline import Pipeline
    pipeline = Pipeline(
        input_dir=args.batch,
        output_dir=args.output,
        profile=args.profile,
        fresh=args.fresh,
        verbose=args.verbose,
    )

    if args.phase in ("a", "ab"):
        pipeline.run_phase_a()
    if args.phase in ("b", "ab"):
        pipeline.run_phase_b()

    # 写 extract-meta.yaml
    import yaml
    from datetime import datetime
    meta = {
        "schema_version": "1.0",
        "extractor": "file-extract",
        "extractor_version": "0.1.0",
        "extracted_at": datetime.now().isoformat(),
    }
    with open(os.path.join(args.output, "extract-meta.yaml"), "w") as f:
        yaml.dump(meta, f, default_flow_style=False)

if __name__ == "__main__":
    main()
```

- [ ] **Step 2: 验证 CLI 可调用**

```bash
cd ~/claude-workspace/skills/file-extract
python3 cli.py --help
# 期望：显示 argparse 帮助信息，无报错
```

- [ ] **Step 3: 验证 --batch 参数校验**

```bash
python3 cli.py --batch /nonexistent --output /tmp/test-out
# 期望：错误退出，输出"源文件目录不存在"
```

- [ ] **Step 4: Commit**

```bash
cd ~/claude-workspace/skills/file-extract
git add cli.py
git commit -m "feat: add CLI entry point for wiki-manage integration"
```

### Task 0.2: pyproject.toml + 契约文档

**Files:**
- Create: `~/claude-workspace/skills/file-extract/pyproject.toml`
- Create: `~/claude-workspace/skills/file-extract/docs/output-format.md`
- Create: `~/claude-workspace/skills/file-extract/LICENSE`

- [ ] **Step 1: 创建 pyproject.toml**

```toml
[build-system]
requires = ["setuptools>=68.0"]
build-backend = "setuptools.backends._legacy:_Backend"

[project]
name = "file-extract"
version = "0.1.0"
description = "通用文件提取工具——将文件提取为结构化文本 + 数字登记簿"
license = {text = "MIT"}
requires-python = ">=3.11"
dependencies = [
    "pyyaml>=6.0",
    "markitdown>=0.1",
]

[project.scripts]
file-extract = "cli:main"
```

- [ ] **Step 2: 创建 docs/output-format.md（契约文档）**

```markdown
# file-extract 输出格式规范

版本：1.0

## 目录结构

file-extract 输出到指定目录（默认 `.pre-extracted/`），结构如下：

\`\`\`
.pre-extracted/
├── extract-meta.yaml         # 提取元数据（版本、时间戳）
├── files/                    # 逐文件产出
│   └── {relative-path}.md           # 提取文本（带 frontmatter）
│   └── {relative-path}.numbers.yaml # 结构化数字
│   └── {relative-path}.meta.yaml    # 元数据
├── conflicts.yaml            # 跨文件数字矛盾
├── extract-manifest.yaml     # 提取状态追踪
└── extract-report.yaml       # 提取报告
\`\`\`

## 路径映射规则

files/ 下的路径必须与源文件目录下的相对路径完全一致：

| 源文件 | 输出文件 |
|--------|---------|
| `raw/inbox/投资建议书.pdf` | `.pre-extracted/files/inbox/投资建议书.pdf.md` |
| `raw/regulation/注册办法.pdf` | `.pre-extracted/files/regulation/注册办法.pdf.md` |

## .md frontmatter

\`\`\`yaml
---
pre_extracted: true
confidence: high          # high/medium/low
engine: markitdown
original_path: /abs/path/to/source.pdf
original_hash: sha256:abc123...
---
\`\`\`

## .numbers.yaml schema

见 spec Section 4。schema_version: "1.0"。
```

- [ ] **Step 3: 创建 LICENSE (MIT)**

- [ ] **Step 4: Commit**

```bash
git add pyproject.toml docs/ LICENSE
git commit -m "feat: add pyproject.toml, output format contract, and LICENSE"
```

### Task 0.3: file-extract 端到端冒烟测试

**Files:**
- Test: Bash 脚本验证

- [ ] **Step 1: 准备测试数据**

```bash
mkdir -p /tmp/fe-test/raw/inbox
echo "# 测试文档\n\n营业收入 5.2 亿元，净利润 0.8 亿元。" > /tmp/fe-test/raw/inbox/test.md
```

- [ ] **Step 2: 运行 Phase A**

```bash
cd ~/claude-workspace/skills/file-extract
python3 cli.py --batch /tmp/fe-test/raw --output /tmp/fe-test/.pre-extracted --phase a --verbose
# 期望：.pre-extracted/files/inbox/test.md.md 存在，带 frontmatter
```

- [ ] **Step 3: 验证输出路径映射**

```bash
ls /tmp/fe-test/.pre-extracted/files/inbox/
# 期望：test.md.md 存在
cat /tmp/fe-test/.pre-extracted/extract-meta.yaml
# 期望：schema_version: "1.0"
```

- [ ] **Step 4: 清理**

```bash
rm -rf /tmp/fe-test
```

---

## Phase 1: sage-wiki PR1 — 预提取支持 + source 命令

### Task 1.1: preextract.go — 预提取内容读取

**Files:**
- Create: `internal/extract/preextract.go`
- Create: `internal/extract/preextract_test.go`
- Modify: `internal/extract/extract.go` (Extract 函数头部加预提取检查)

- [ ] **Step 1: 写 preextract_test.go（5 个测试场景）**

```go
package extract

import (
    "os"
    "path/filepath"
    "testing"
)

func TestTryPreExtracted_Found(t *testing.T) {
    // 创建临时目录模拟 .pre-extracted/
    dir := t.TempDir()
    preDir := filepath.Join(dir, ".pre-extracted", "files", "inbox")
    os.MkdirAll(preDir, 0755)

    content := "---\npre_extracted: true\nconfidence: high\nengine: markitdown\n---\n# Test content"
    os.WriteFile(filepath.Join(preDir, "test.pdf.md"), []byte(content), 0644)

    sc, err := TryPreExtracted(dir, "raw/inbox/test.pdf")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if sc == nil {
        t.Fatal("expected SourceContent, got nil")
    }
    if sc.PreExtracted != true {
        t.Error("expected PreExtracted=true")
    }
    if sc.Confidence != "high" {
        t.Errorf("expected confidence=high, got %s", sc.Confidence)
    }
}

func TestTryPreExtracted_NotFound(t *testing.T) {
    dir := t.TempDir()
    sc, err := TryPreExtracted(dir, "raw/inbox/missing.pdf")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if sc != nil {
        t.Error("expected nil for missing pre-extracted file")
    }
}

func TestTryPreExtracted_LowConfidence(t *testing.T) {
    dir := t.TempDir()
    preDir := filepath.Join(dir, ".pre-extracted", "files", "inbox")
    os.MkdirAll(preDir, 0755)

    content := "---\npre_extracted: true\nconfidence: low\nengine: markitdown\n---\n# Low quality"
    os.WriteFile(filepath.Join(preDir, "test.pdf.md"), []byte(content), 0644)

    sc, err := TryPreExtracted(dir, "raw/inbox/test.pdf")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    // low confidence 应返回 nil，让 Go 引擎处理
    if sc != nil {
        t.Error("expected nil for low confidence")
    }
}

func TestTryPreExtracted_CorruptedFrontmatter(t *testing.T) {
    dir := t.TempDir()
    preDir := filepath.Join(dir, ".pre-extracted", "files", "inbox")
    os.MkdirAll(preDir, 0755)

    content := "not yaml frontmatter\n# Just content"
    os.WriteFile(filepath.Join(preDir, "test.pdf.md"), []byte(content), 0644)

    sc, err := TryPreExtracted(dir, "raw/inbox/test.pdf")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if sc != nil {
        t.Error("expected nil for corrupted frontmatter")
    }
}

func TestTryPreExtracted_NoPreExtractedDir(t *testing.T) {
    dir := t.TempDir()
    // 不创建 .pre-extracted/ 目录
    sc, err := TryPreExtracted(dir, "raw/inbox/test.pdf")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if sc != nil {
        t.Error("expected nil when .pre-extracted/ doesn't exist")
    }
}
```

- [ ] **Step 2: 跑测试确认全部 FAIL**

```bash
cd ~/claude-workspace/projects/KarpathyWiki/sage-wiki
go test ./internal/extract/ -run TestTryPreExtracted -v
# 期望：5 个 FAIL（TryPreExtracted 未定义）
```

- [ ] **Step 3: 实现 preextract.go**

```go
package extract

import (
    "os"
    "path/filepath"
    "strings"

    "gopkg.in/yaml.v3"
)

// PreExtractFrontmatter holds parsed frontmatter from pre-extracted files.
type PreExtractFrontmatter struct {
    PreExtracted bool   `yaml:"pre_extracted"`
    Confidence   string `yaml:"confidence"`
    Engine       string `yaml:"engine"`
    OriginalPath string `yaml:"original_path"`
    OriginalHash string `yaml:"original_hash"`
}

// TryPreExtracted checks for a pre-extracted .md file and returns its content.
// Returns nil, nil if no pre-extracted file exists or confidence is low.
func TryPreExtracted(projectDir string, rawRelPath string) (*SourceContent, error) {
    // raw/inbox/test.pdf → inbox/test.pdf
    relPath := rawRelPath
    if strings.HasPrefix(relPath, "raw/") {
        relPath = relPath[4:]
    }

    preDir := filepath.Join(projectDir, ".pre-extracted", "files")
    mdPath := filepath.Join(preDir, relPath+".md")

    data, err := os.ReadFile(mdPath)
    if err != nil {
        if os.IsNotExist(err) {
            return nil, nil
        }
        return nil, err
    }

    // 解析 frontmatter
    content := string(data)
    fm, body, err := parseFrontmatter(content)
    if err != nil {
        return nil, nil // 损坏的 frontmatter，降级到 Go 引擎
    }

    if !fm.PreExtracted || fm.Confidence == "low" {
        return nil, nil
    }

    sc := &SourceContent{
        Path:         rawRelPath,
        Text:         body,
        PreExtracted: true,
        Confidence:   fm.Confidence,
        ExtractEngine: fm.Engine,
    }
    return sc, nil
}

func parseFrontmatter(content string) (*PreExtractFrontmatter, string, error) {
    if !strings.HasPrefix(content, "---\n") {
        return nil, content, fmt.Errorf("no frontmatter")
    }
    end := strings.Index(content[4:], "\n---")
    if end < 0 {
        return nil, content, fmt.Errorf("no frontmatter end")
    }
    fmStr := content[4 : 4+end]
    body := content[4+end+4:]

    var fm PreExtractFrontmatter
    if err := yaml.Unmarshal([]byte(fmStr), &fm); err != nil {
        return nil, content, err
    }
    return &fm, body, nil
}
```

- [ ] **Step 4: 给 SourceContent 加新字段**

在 `internal/extract/extract.go` 的 SourceContent struct 中添加：

```go
type SourceContent struct {
    Path          string
    Type          string
    Text          string
    Frontmatter   string
    Chunks        []Chunk
    ChunkCount    int
    PreExtracted  bool   // 是否来自预提取
    Confidence    string // high/medium/low
    ExtractEngine string // 使用的提取引擎
}
```

- [ ] **Step 5: 跑测试确认全部 PASS**

```bash
go test ./internal/extract/ -run TestTryPreExtracted -v
# 期望：5 个 PASS
```

- [ ] **Step 6: 在 Extract() 函数头部集成预提取检查**

在 `internal/extract/extract.go` 的 `Extract()` 函数开头加：

```go
func Extract(path string, sourceType string) (*SourceContent, error) {
    // 预提取检查：如果 .pre-extracted/ 有高质量提取结果，直接使用
    // projectDir 需要从 path 推断或作为参数传入
    // 注意：此处需要 projectDir，考虑增加 ExtractWithProject 函数

    ext := strings.ToLower(filepath.Ext(path))
    // ... 原有逻辑
```

- [ ] **Step 7: Commit**

```bash
git add internal/extract/preextract.go internal/extract/preextract_test.go internal/extract/extract.go
git commit -m "feat: add pre-extracted content support with confidence filtering"
```

### Task 1.2: source show + source list 命令

**Files:**
- Create: `cmd/sage-wiki/source_cmd.go`
- Modify: `cmd/sage-wiki/main.go` (注册命令)

- [ ] **Step 1: 创建 source_cmd.go**

实现 `source show <path>` 和 `source list` 两个子命令。source show 返回预提取内容或原文元信息。source list 显示所有源文件的 pre-extracted/compiled/facts 三列状态。

- [ ] **Step 2: 在 main.go 注册**

```go
rootCmd.AddCommand(sourceCmd)
```

- [ ] **Step 3: 验证命令**

```bash
cd ~/claude-workspace/projects/KarpathyWiki/sage-wiki
go build -o sage-wiki ./cmd/sage-wiki/
./sage-wiki source --help
./sage-wiki source list --project ~/claude-workspace/wiki --format json
```

- [ ] **Step 4: Commit**

```bash
git add cmd/sage-wiki/source_cmd.go cmd/sage-wiki/main.go
git commit -m "feat: add source show and source list commands"
```

### Task 1.3: PR1 构建验证

- [ ] **Step 1: 全量构建 + 测试**

```bash
go build -o sage-wiki ./cmd/sage-wiki/
go test ./... -v
go vet ./...
```

- [ ] **Step 2: 验证 graceful fallback**

```bash
# 无 .pre-extracted/ 目录时编译应正常工作
./sage-wiki compile --project ~/claude-workspace/wiki
```

---

## Phase 2: sage-wiki PR2a — facts 存储层

### Task 2.1: facts 表 schema + CRUD

**Files:**
- Modify: `internal/storage/db.go` (添加 CREATE TABLE facts)
- Create: `internal/facts/facts.go` (Fact struct + CRUD)
- Create: `internal/facts/facts_test.go`

- [ ] **Step 1: 写 facts_test.go**

测试场景：Insert + Query + Upsert 去重 + DeleteBySource + QueryByEntity + QueryByPeriod

- [ ] **Step 2: 跑测试确认 FAIL**

- [ ] **Step 3: 在 db.go 加 CREATE TABLE facts**

在现有 schema 迁移逻辑中追加 facts 表（spec Section 5 schema，含 quote_hash）。

- [ ] **Step 4: 实现 facts.go**

Fact struct + Insert (upsert with quote_hash) + Query (多 filter) + DeleteBySource + Stats

- [ ] **Step 5: 跑测试确认 PASS**

- [ ] **Step 6: Commit**

### Task 2.2: facts import — YAML 解析 + 规范化

**Files:**
- Create: `internal/facts/import.go`
- Create: `internal/facts/import_test.go`

- [ ] **Step 1: 写 import_test.go**

测试场景：正常导入 + upsert 不重复 + schema 版本检查 + 逐文件事务 + label_aliases 规范化 + entity_aliases 规范化 + 损坏 YAML 处理

- [ ] **Step 2: 跑测试确认 FAIL**

- [ ] **Step 3: 实现 import.go**

ImportDir() 函数：扫描 .pre-extracted/files/ 下所有 .numbers.yaml → 读 extract-meta.yaml 检查版本 → 逐文件读 YAML → 查 facts-aliases.yaml 规范化 entity/label → upsert 写入 → 返回 ImportReport{Added, Updated, Skipped, Errors}

- [ ] **Step 4: 跑测试确认 PASS**

- [ ] **Step 5: Commit**

---

## Phase 3: sage-wiki PR2b — facts CLI + 质量增强

### Task 3.1: facts 命令组 (query/stats/delete)

**Files:**
- Create: `cmd/sage-wiki/facts_cmd.go`
- Modify: `cmd/sage-wiki/main.go`

- [ ] **Step 1: 实现 facts import/query/stats/delete 子命令**

query flags: --entity, --period, --label, --number-type, --source, --entity-type, --limit, --count-only, --fuzzy, --scope
stats: 实体分布、period 分布、碎片度
delete: --source <file> 或 --all

- [ ] **Step 2: 注册到 main.go**

- [ ] **Step 3: 验证**

```bash
go build -o sage-wiki ./cmd/sage-wiki/
./sage-wiki facts --help
./sage-wiki facts import --help
./sage-wiki facts query --help
```

- [ ] **Step 4: Commit**

### Task 3.2: coverage 命令

**Files:**
- Create: `cmd/sage-wiki/coverage_cmd.go`

- [ ] **Step 1: 实现三层覆盖率报告**

- extracted: 读 .pre-extracted/ vs raw/，缺失层报 N/A
- compiled: 读 .manifest.json
- facts: 查 facts 表
- staleness_warning: 检查 diff（未编译文件提醒）+ 全局导入时间戳

- [ ] **Step 2: 验证**
- [ ] **Step 3: Commit**

### Task 3.3: lint 增强 — 2 个新 pass

**Files:**
- Modify: `internal/linter/passes.go` (新增 NumericContradictionPass + OrphanFactsPass)
- Modify: `internal/linter/runner.go` (注册新 pass)

- [ ] **Step 1: 实现 NumericContradictionPass**

读 .pre-extracted/conflicts.yaml，每个冲突生成一个 Finding。

- [ ] **Step 2: 实现 OrphanFactsPass**

查 facts 表中 source_file 不存在于 raw/ 的记录。

- [ ] **Step 3: 在 runner.go 注册**

- [ ] **Step 4: 测试**

```bash
./sage-wiki lint --project ~/claude-workspace/wiki --format json
```

- [ ] **Step 5: Commit**

### Task 3.4: compile 增强 — facts 注入 + 删除检测

**Files:**
- Modify: `internal/compiler/summarize.go` (注入 facts 到 prompt)
- Modify: `internal/compiler/pipeline.go` (删除检测)

- [ ] **Step 1: summarize.go 注入 facts**

compile 每个源文件时，查 facts 表该文件的数字，按 spec Section 4 策略注入到 user prompt。

- [ ] **Step 2: pipeline.go 删除检测**

compile 开始时检查 manifest 中的文件是否仍存在于 raw/，不存在的标记提醒。

- [ ] **Step 3: 构建验证**

```bash
go build -o sage-wiki ./cmd/sage-wiki/
go test ./... -v
```

- [ ] **Step 4: Commit**

### Task 3.5: search/query scope 增强

**Files:**
- Modify: 相关命令文件

- [ ] **Step 1: search 加 --scope local|global|all**
- [ ] **Step 2: query 加 --scope + source_project 返回**
- [ ] **Step 3: Commit**

---

## Phase 4: Skills

### Task 4.1: wiki skill (SKILL.md)

**Files:**
- Create: `skills/wiki/SKILL.md`

- [ ] **Step 1: 写 SKILL.md**

包含：description（wiki 专属触发词）、配置（SAGE/WIKI 变量）、核心能力（6 个）、快捷命令路由表、智能摘要逻辑、时效提醒逻辑、scope 自动推断。

- [ ] **Step 2: 部署到本地测试**

```bash
cp skills/wiki/SKILL.md ~/.claude/skills/wiki/SKILL.md
```

- [ ] **Step 3: 测试触发**

在 CC 中测试 `/wiki search 测试` 和 `/wiki facts --entity test` 是否正常路由。

- [ ] **Step 4: Commit**

### Task 4.2: wiki-manage skill (SKILL.md)

**Files:**
- Create: `skills/wiki-manage/SKILL.md`

- [ ] **Step 1: 基于 wiki-improve 改造**

保留 wiki-improve 的 Step 0-4 基础结构，新增：
- 入口菜单（5 个工作流）
- 工作流 1 创建项目（交互式）
- 工作流 2 增量/全量模式选择 + Step 0.6/0.7/0.8
- 工作流 3 深度体检增强（facts 审计）
- 工作流 5 状态看板
- 断点恢复机制

- [ ] **Step 2: 部署到本地测试**

```bash
cp skills/wiki-manage/SKILL.md ~/.claude/skills/wiki-manage/SKILL.md
```

- [ ] **Step 3: 测试工作流 2（日常维护）增量模式**

在 CC 中运行 `/wiki-manage`，选择"日常维护"，验证 Step 0.6-0.8 流程。

- [ ] **Step 4: Commit**

---

## Phase 5: 端到端测试 + 系统自检

### Task 5.1: 端到端管线测试（E2E）

**目的：** 验证完整数据流：raw 文件 → file-extract → facts import → compile → query → 结果正确

**Files:**
- Create: `tests/e2e/integration_test.sh`

- [ ] **Step 1: 准备测试 wiki 项目**

```bash
mkdir -p /tmp/e2e-wiki/raw/inbox
# 放入一个包含数字的测试文档
cat > /tmp/e2e-wiki/raw/inbox/test-report.md << 'EOF'
# 希烽光电 2023年报

营业收入 5.2 亿元，同比增长 15.3%。
净利润 0.8 亿元，毛利率 35.2%。
向 Finisar 销售光芯片 367.28 万美元。
EOF

# 初始化 wiki
SAGE=~/claude-workspace/projects/KarpathyWiki/sage-wiki/sage-wiki
$SAGE init --project /tmp/e2e-wiki
```

- [ ] **Step 2: 运行 file-extract**

```bash
cd ~/claude-workspace/skills/file-extract
python3 cli.py --batch /tmp/e2e-wiki/raw --output /tmp/e2e-wiki/.pre-extracted --phase ab --verbose

# 验证输出
ls /tmp/e2e-wiki/.pre-extracted/files/inbox/
# 期望：test-report.md.md + test-report.md.numbers.yaml
cat /tmp/e2e-wiki/.pre-extracted/extract-meta.yaml
# 期望：schema_version: "1.0"
```

- [ ] **Step 3: 运行 facts import**

```bash
$SAGE facts import /tmp/e2e-wiki/.pre-extracted --project /tmp/e2e-wiki --format json
# 期望：报告导入了 N 条 facts
```

- [ ] **Step 4: 运行 compile**

```bash
echo y | $SAGE compile --project /tmp/e2e-wiki
# 期望：编译成功，生成 wiki 文章
```

- [ ] **Step 5: 验证数据完整性**

```bash
# 5a: facts query 返回数字
$SAGE facts query --entity "希烽光电" --project /tmp/e2e-wiki --format json
# 期望：返回包含"营业收入 5.2亿"的记录

# 5b: source show 返回原文
$SAGE source show raw/inbox/test-report.md --project /tmp/e2e-wiki --format json
# 期望：返回预提取文本

# 5c: coverage 三层完整
$SAGE coverage --project /tmp/e2e-wiki --format json
# 期望：extracted=1/1, compiled=1/1, facts=1/1

# 5d: source list 状态一致
$SAGE source list --project /tmp/e2e-wiki --format json
# 期望：test-report.md 三列全 ✓
```

- [ ] **Step 6: 验证增量更新**

```bash
# 新增一个文件
cat > /tmp/e2e-wiki/raw/inbox/new-doc.md << 'EOF'
# 新文档
额外数据：收入 10 亿元。
EOF

# 增量提取
python3 ~/claude-workspace/skills/file-extract/cli.py \
  --batch /tmp/e2e-wiki/raw --output /tmp/e2e-wiki/.pre-extracted

# 增量导入
$SAGE facts import /tmp/e2e-wiki/.pre-extracted --project /tmp/e2e-wiki --format json
# 期望：新增记录，旧记录不重复

# 增量编译
echo y | $SAGE compile --project /tmp/e2e-wiki
```

- [ ] **Step 7: 验证删除清理**

```bash
# 删除源文件
rm /tmp/e2e-wiki/raw/inbox/new-doc.md

# facts delete
$SAGE facts delete --source "raw/inbox/new-doc.md" --project /tmp/e2e-wiki
# 期望：删除该文件的 facts

# lint orphan check
$SAGE lint --project /tmp/e2e-wiki --format json
# 期望：无 orphan-facts（已清理）
```

- [ ] **Step 8: 清理**

```bash
rm -rf /tmp/e2e-wiki
```

### Task 5.2: 系统自检测试

**目的：** 验证 sage-wiki 自身的质量检查能力——lint 所有 pass 正常、coverage 报告准确、stats 数据一致

- [ ] **Step 1: 在现有 wiki 项目上运行全套检查**

```bash
SAGE=~/claude-workspace/projects/KarpathyWiki/sage-wiki/sage-wiki
WIKI=~/claude-workspace/wiki

# 状态
$SAGE status --project $WIKI --format json

# 覆盖率（如果有 .pre-extracted/）
$SAGE coverage --project $WIKI --format json

# Lint 全 pass（含新增的 2 个 pass）
$SAGE lint --project $WIKI --format json

# facts stats（如果已导入）
$SAGE facts stats --project $WIKI --format json
```

- [ ] **Step 2: 验证 lint 新 pass 不误报**

对现有 wiki（无 .pre-extracted/、无 facts）运行 lint，新增 pass 应该静默跳过而非报错。

```bash
$SAGE lint --project $WIKI --format json | python3 -c "
import sys, json
data = json.load(sys.stdin)
for p in data.get('passes', []):
    if p['name'] in ('numeric-contradiction', 'orphan-facts'):
        assert p.get('skipped') or p.get('findings_count', 0) == 0, f'{p[\"name\"]} should be clean on empty wiki'
        print(f'{p[\"name\"]}: OK (skipped or no findings)')
"
```

- [ ] **Step 3: 验证 Go 测试套件全部通过**

```bash
cd ~/claude-workspace/projects/KarpathyWiki/sage-wiki
go test ./... -v -count=1
go vet ./...
# 期望：全部 PASS，无 vet 警告
```

### Task 5.3: wiki skill 集成测试

**目的：** 在 CC 会话中验证 wiki skill 的核心场景

- [ ] **Step 1: 测试 /wiki 快捷命令**

在 CC 中依次测试：
```
/wiki status
/wiki diff
/wiki search 测试
/wiki lint
```

- [ ] **Step 2: 测试 facts 查询（需要先导入数据）**

```
/wiki facts 希烽光电
/wiki coverage
```

- [ ] **Step 3: 记录问题并修复**

### Task 5.4: wiki-manage 集成测试

**目的：** 在 CC 会话中验证 wiki-manage 的 5 个工作流

- [ ] **Step 1: 测试工作流 5（状态看板）**

```
/wiki-manage → 选择 5
```
验证看板输出格式正确。

- [ ] **Step 2: 测试工作流 2（日常维护-增量）**

```
/wiki-manage → 选择 2 → 增量
```
验证 Step 0.6-0.8 + Step 1-4 流程完整。

- [ ] **Step 3: 测试工作流 4（更新底座）**

```
/wiki-manage → 选择 4
```
验证底座更新检查正常。

---

## Phase 6: GitHub 提交

### Task 6.1: file-extract 推到 GitHub

- [ ] **Step 1: 创建 GitHub repo**

```bash
cd ~/claude-workspace/skills/file-extract
gh repo create kellen/file-extract --public --source=. --push
```

### Task 6.2: sage-wiki PR1 提交

- [ ] **Step 1: 创建分支**

```bash
cd ~/claude-workspace/projects/KarpathyWiki/sage-wiki
git checkout -b feature/pre-extract-support
```

- [ ] **Step 2: Cherry-pick PR1 相关 commits**

- [ ] **Step 3: Push + 创建 PR**

```bash
git push -u origin feature/pre-extract-support
gh pr create --title "feat: support pre-extracted content from external tools" --body "..."
```

### Task 6.3: sage-wiki PR2 提交

等 PR1 合并后执行。如果被要求拆分，按 PR2a/PR2b 处理。

### Task 6.4: sage-wiki PR3 + PR4

PR1+PR2 合并后提交文档和 Skills PR。

---

## 自检清单

| Spec 要求 | Plan Task |
|-----------|-----------|
| 文件契约 + 路径映射 | Task 0.2 (output-format.md) + Task 1.1 (preextract.go) |
| facts 表 + upsert | Task 2.1 |
| facts import + 规范化 | Task 2.2 |
| 9 个新 CLI 命令 | Task 1.2 + 3.1 + 3.2 |
| 4 个命令增强 | Task 3.3 + 3.4 + 3.5 |
| wiki skill | Task 4.1 |
| wiki-manage skill | Task 4.2 |
| 端到端测试 | Task 5.1 |
| 系统自检 | Task 5.2 |
| skill 集成测试 | Task 5.3 + 5.4 |
| 4 个 PR | Task 6.1-6.4 |
| summarize facts 注入 | Task 3.4 |
| S1-S10 + V1-V15 应对 | 分散在各 Task 中 |
