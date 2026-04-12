---
name: wiki-manage
description: sage-wiki 全生命周期管理——创建项目、日常维护（含 file-extract + facts 导入）、深度体检、底座更新、状态看板
user_invocable: true
---

# /wiki-manage — 知识库全生命周期管理

基于 sage-wiki CLI 的知识库维护编排工具。

## CLI 配置

```
SAGE=/Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki/sage-wiki
WIKI=/Users/kellen/claude-workspace/wiki
FILE_EXTRACT=~/claude-workspace/skills/file-extract
```

所有操作通过 sage-wiki CLI 执行（不使用 MCP）。

## 入口

先询问操作类型：

```
K老板，请选择操作：
1. 创建项目 — 初始化新 wiki 项目 + 注册到 hub
2. 日常维护 — 更新检查 + 提取 + 导入 + 编译 + lint + wikilink 修复
3. 深度体检 — 日常维护 + 概念覆盖度/去重 + Config 治理 + facts 审计
4. 更新底座 — 检查并合并 sage-wiki 上游更新 + 重新编译二进制
5. 状态看板 — 项目状态 + 覆盖率 + facts 统计一览

选择 (1-5):
```

## 工作流 1: 创建项目

交互式创建新 wiki 项目：

1. 询问项目路径和名称
2. `$SAGE init --project <path>`
3. 询问是否注册到 hub：`$SAGE hub add <path>`
4. 创建 raw/inbox/ 目录
5. 提示用户放入源文件后运行日常维护

## 工作流 2: 日常维护

### Step 0: 底座更新检查

同 wiki-improve Step 0（fetch upstream、构建、测试）。

### Step 0b: API 健康探针

```bash
$SAGE search "test" --project $WIKI --format json
```
成功→继续，失败→暂停报告。

### Step 0.4: 目录体系迁移

同 wiki-improve Step 0.4（检查 raw/ 目录合规性）。

### Step 0.5: Inbox 处理

同 wiki-improve Step 0.5（编译 + 分类 inbox 文件）。

### Step 0.6: 文件提取（新增）

检查是否有需要提取的文件（PDF/Office 等非文本格式）。

**模式选择：**
```
检测到 N 个非文本源文件：
1. 增量提取 — 仅提取新增/修改的文件
2. 全量提取 — 忽略缓存，全部重新提取

选择 (1/2):
```

增量提取：
```bash
cd $FILE_EXTRACT
python3 cli.py --batch $WIKI/raw --output $WIKI/.pre-extracted --phase ab --verbose
```

全量提取：
```bash
cd $FILE_EXTRACT
python3 cli.py --batch $WIKI/raw --output $WIKI/.pre-extracted --phase ab --fresh --verbose
```

Bash 后台运行（`run_in_background: true`）。完成后验证：
```bash
ls $WIKI/.pre-extracted/files/ | head -20
cat $WIKI/.pre-extracted/extract-meta.yaml
```

### Step 0.7: Facts 导入（新增）

提取完成后导入结构化数字：

```bash
$SAGE facts import --project $WIKI --format json
```

检查导入结果：added/skipped/errors。errors > 0 时列出失败文件。

### Step 0.8: Facts 质量快检（新增）

```bash
$SAGE facts stats --project $WIKI --format json
$SAGE coverage --project $WIKI --format json
```

快速展示：
- 总 facts 数 / 实体数 / period 覆盖
- 每个源文件的 extracted/compiled/facts 三层状态
- 有 extracted=yes 但 facts=0 的文件标记为"数字提取可能失败"

### Step 1: 编译检查

**模式选择：**
```
检测到 N 个待编译文件，选择编译模式：
1. 增量编译 — 仅编译新增/修改的文件
2. 全量编译 — 忽略缓存，全部重新编译

选择 (1/2):
```

增量：
```bash
echo y | $SAGE compile --project $WIKI
```

全量：
```bash
echo y | $SAGE compile --fresh --project $WIKI
```

### Step 2: Lint

```bash
$SAGE lint --project $WIKI --format json
```

9 个 pass（含 numeric-contradiction 和 orphan-facts）。可修复的自动修复。

### Step 3: Wikilink 修复

同 wiki-improve Step 3（7 种断链类型逐一处理）。

### Step 4: 报告

```
wiki-manage 日常维护报告
├── API 健康: embed ✓/✗
├── 更新: sage-wiki 已是最新 / 已更新到 xxx
├── 迁移: 无 / 迁移了 N 个目录
├── Inbox: 无 / 处理了 N 个文件
├── 提取: 无新文件 / 提取了 N 个文件（成功 S / 失败 F）
├── Facts: 导入 +N added, ~M skipped, !E errors
├── 编译: 新编译了 N 篇文章
├── Lint: 发现 N 个问题（numeric-contradiction: X, orphan-facts: Y）
├── Wikilink: 修复 N 个断链
└── 三层覆盖率: extracted X% / compiled Y% / facts Z%
```

## 工作流 3: 深度体检

执行工作流 2 全部步骤，额外增加：

### Facts 审计（新增）

```bash
$SAGE facts query --count-only --project $WIKI --format json
$SAGE coverage --project $WIKI --format json
```

检查项：
- 哪些源文件有 .md 但没 .numbers.yaml（提取遗漏）
- entity 碎片度：疑似同一实体的不同名称（用 facts-aliases.yaml 辅助检测）
- period 覆盖：哪些实体只有单一年份数据

### 概念覆盖度 + 去重

同 wiki-improve Step 5。

### Config 治理

同 wiki-improve Step 6（分类覆盖度 + 关系频次 + 收敛告警）。

## 工作流 4: 更新底座

仅执行 Step 0（上游更新检查 + 构建 + 测试），输出结果后结束。

## 工作流 5: 状态看板

快速一览，不做修改：

```bash
$SAGE status --project $WIKI --format json
$SAGE facts stats --project $WIKI --format json
$SAGE coverage --project $WIKI --format json
```

格式化输出：
```
sage-wiki 状态看板
──────────────────
项目: wiki
源文件: N 个 (raw/)
文章: M 个 (wiki/concepts/)
数据库: FTS5 X + Vector Y

三层覆盖率
──────────
提取: A/N (X%)
编译: B/N (Y%)
数字: C/N (Z%)

Facts 统计
──────────
总数: N 条
实体: M 个
期间: K 个
指标: L 种
来源: P 个
```

## 断点恢复

如果某个 Step 失败，记录当前进度到 `/tmp/wiki-manage-state.json`：

```json
{
  "workflow": 2,
  "last_completed_step": "0.7",
  "project": "/Users/kellen/claude-workspace/wiki",
  "timestamp": "2026-04-12T16:00:00"
}
```

下次运行时检测到未完成状态，询问：
```
检测到上次维护未完成（停在 Step 0.7）。
1. 从断点继续
2. 重新开始
选择 (1/2):
```

## 注意

- 所有 CLI 操作不使用 MCP
- file-extract 和 sage-wiki 是独立工具，通过文件契约（.pre-extracted/）交互
- 编译前确保 facts 已导入——facts 会注入到 summarize prompt 提升数字准确性
