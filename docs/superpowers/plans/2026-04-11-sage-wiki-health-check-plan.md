# sage-wiki 全面健康检查实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 对 sage-wiki MCP→CLI 迁移后的系统进行全面健康检查，生成结构化报告（PASS/WARN/FAIL），不做即时修复。

**Architecture:** 三层检查（L1 机械验证 → L2 数据验证 → L3 压力/边界），后层依赖前层结果。每个 Task 输出一段报告追加到 `health-report.md`。

**Tech Stack:** Go 1.22+, SQLite3, Node.js (web/), Python 3 (eval.py), Docker, Bash

**关键路径变量：**
- `SAGE_BIN`: `/Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki/sage-wiki`
- `SAGE_SRC`: `/Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki`
- `WIKI_DIR`: `/Users/kellen/claude-workspace/wiki`

**Spec 修正（实施前发现）：**
- `compile --all` 应为 `compile --fresh`
- CLI 实际有 20 个命令（含 doctor/query/ingest/init/serve/tui/completion/help），spec 只覆盖 10 个
- `serve` 命令（MCP 服务器）仍存在，需作为迁移状态检查项

---

## File Structure

| 文件 | 职责 |
|------|------|
| Create: `SAGE_SRC/health-report.md` | 最终健康检查报告（逐 Task 追加） |
| Read: `SAGE_SRC/cmd/sage-wiki/main.go` | CLI 命令注册，确认命令全集 |
| Read: `WIKI_DIR/config.yaml` | Wiki 配置 |
| Read: `WIKI_DIR/.sage/wiki.db` | SQLite 数据库（查询表结构） |
| Read: `WIKI_DIR/prompts/*.md` | Prompt 模板 |
| Read: `~/.claude/skills/wiki-improve/SKILL.md` | Skill 路由验证 |
| Read: `~/.claude/skills/wiki/SKILL.md` | Wiki skill 路由验证 |
| Read: `SAGE_SRC/README.md` | 文档一致性 |
| Read: `SAGE_SRC/CHANGELOG.md` | 文档一致性 |
| Read: `SAGE_SRC/Dockerfile` | Docker 构建验证 |

---

### Task 1: 环境准备 + 报告骨架

**Files:**
- Create: `SAGE_SRC/health-report.md`

- [ ] **Step 1: 记录检查环境信息**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
echo "# sage-wiki 健康检查报告" > health-report.md
echo "" >> health-report.md
echo "日期: $(date +%Y-%m-%d)" >> health-report.md
echo "分支: $(git branch --show-current)" >> health-report.md
echo "检查版本: $(git rev-parse --short HEAD)" >> health-report.md
echo "Go 版本: $(go version)" >> health-report.md
echo "" >> health-report.md
```

- [ ] **Step 2: 确认 sage-wiki 二进制是最新的**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
go build -o sage-wiki ./cmd/sage-wiki/
echo $?
# 预期: 0
```

- [ ] **Step 3: 写报告总览占位**

在 `health-report.md` 追加总览 section（最后填数字）：

```markdown
## 总览
- 总检查项: _待填_
- PASS: _待填_
- WARN: _待填_
- FAIL: _待填_
- SKIP: _待填_
```

- [ ] **Step 4: Commit 报告骨架**

```bash
git add health-report.md
git commit -m "chore: init health check report scaffold"
```

---

### Task 2: L1 — 集成完整性（维度 1）

**Files:**
- Modify: `SAGE_SRC/health-report.md`

- [ ] **Step 1: Go 工具链验证（1.1-1.3）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 1.1 go build
go build ./... 2>&1; echo "EXIT:$?"
# 预期: EXIT:0

# 1.2 go vet
go vet ./... 2>&1; echo "EXIT:$?"
# 预期: EXIT:0

# 1.3 go test（排除集成测试，需要 API）
go test ./internal/... 2>&1; echo "EXIT:$?"
# 预期: EXIT:0（或记录失败测试）
```

记录每项 PASS/FAIL 到 health-report.md。

- [ ] **Step 2: 全部 20 个命令 --help 验证（1.15）**

逐个运行 `./sage-wiki <cmd> --help`，确认每个返回 usage 信息：

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
for cmd in add-source capture compile completion diff doctor help hub ingest init learn lint list ontology query search serve status tui write; do
  ./sage-wiki $cmd --help > /dev/null 2>&1
  echo "$cmd: EXIT:$?"
done
# 预期: 全部 EXIT:0
```

- [ ] **Step 3: CLI 命令功能验证 — status/list/diff（1.4-1.9）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 1.4 status
./sage-wiki status --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"

# 1.5 status JSON
./sage-wiki status --project /Users/kellen/claude-workspace/wiki --format json 2>&1 | python3 -c "import sys,json; json.load(sys.stdin); print('VALID JSON')" 2>&1

# 1.6 list
./sage-wiki list --project /Users/kellen/claude-workspace/wiki 2>&1 | head -5; echo "EXIT:$?"

# 1.7 list JSON
./sage-wiki list --project /Users/kellen/claude-workspace/wiki --format json 2>&1 | python3 -c "import sys,json; json.load(sys.stdin); print('VALID JSON')" 2>&1

# 1.8 diff
./sage-wiki diff --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"

# 1.9 diff JSON
./sage-wiki diff --project /Users/kellen/claude-workspace/wiki --format json 2>&1 | python3 -c "import sys,json; json.load(sys.stdin); print('VALID JSON')" 2>&1
```

- [ ] **Step 4: CLI 命令功能验证 — ontology/search/hub（1.10-1.14）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 1.10 ontology query entity
./sage-wiki ontology query --type entity --project /Users/kellen/claude-workspace/wiki 2>&1 | head -5; echo "EXIT:$?"

# 1.11 ontology query relation
./sage-wiki ontology query --type relation --project /Users/kellen/claude-workspace/wiki 2>&1 | head -5; echo "EXIT:$?"

# 1.12 search
./sage-wiki search "测试" --project /Users/kellen/claude-workspace/wiki 2>&1 | head -5; echo "EXIT:$?"

# 1.13 hub status
./sage-wiki hub status --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"

# 1.14 hub list
./sage-wiki hub list --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"
```

- [ ] **Step 5: CLI 命令功能验证 — 新发现命令（doctor/query/ingest/init/tui）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# doctor（配置校验）
./sage-wiki doctor --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"

# query
./sage-wiki query "什么是知识库" --project /Users/kellen/claude-workspace/wiki 2>&1 | head -5; echo "EXIT:$?"
# 注意: query 可能需要 API，记录行为即可

# init（不在 wiki 目录执行，用 /tmp 测试）
./sage-wiki init --project /tmp/sage-wiki-test-init 2>&1; echo "EXIT:$?"
rm -rf /tmp/sage-wiki-test-init
```

- [ ] **Step 6: 记录结果到报告**

将 Step 1-5 的全部结果整理成表格追加到 `health-report.md` 的 `## L1 机械验证 > ### 1. 集成完整性` section。

---

### Task 3: L1 — 迁移状态清洁（维度 2）

**Files:**
- Modify: `SAGE_SRC/health-report.md`
- Read: `~/.claude.json`
- Read: `~/.claude/settings.json`
- Read: `~/.claude/skill-experience/_mcp-mapping.json`
- Read: `~/.claude/skills/wiki-improve/SKILL.md`

- [ ] **Step 1: Go 代码 MCP 残留扫描（2.1-2.2）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 2.1 MCP 工具名引用
grep -r "mcp__sage-wiki" --include="*.go" . | wc -l
# 预期: 0

# 2.2 internal/mcp 包被 import
grep -r '".*sage-wiki/internal/mcp"' --include="*.go" . | grep -v "internal/mcp/"
# 预期: 仅 main.go 的 serve 命令注册，或 0 匹配
```

- [ ] **Step 2: 配置文件 MCP 残留扫描（2.3, 2.5-2.7）**

读取以下文件检查：
- `~/.claude.json` — 搜索 `sage-wiki` 关键字，确认无 MCP server 条目
- `~/.claude/settings.json` — 搜索 `sage-wiki` 关键字，确认无 MCP matcher
- `~/.claude/skill-experience/_mcp-mapping.json` — 搜索 `sage-wiki` 条目
- `~/.claude/skill-experience/` 目录下所有 `.md` 文件 — grep `mcp__sage-wiki`

- [ ] **Step 3: Skill 文件 MCP 残留扫描（2.4）**

```bash
# wiki-improve skill
grep -c "mcp__" ~/.claude/skills/wiki-improve/SKILL.md
# 预期: 0

# wiki skill（如存在）
grep -c "mcp__" ~/.claude/skills/wiki/SKILL.md 2>/dev/null
# 预期: 0 或文件不存在
```

- [ ] **Step 4: serve 命令存在性记录（新发现）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
./sage-wiki serve --help 2>&1 | head -3
# 记录: serve 命令仍存在（上游代码，非迁移遗留），标 WARN
```

- [ ] **Step 5: 记录结果到报告**

整理到 `health-report.md` 的 `### 2. 迁移状态清洁` section。`serve` 命令存在标 WARN（上游功能，非残留但需注意）。

---

### Task 4: L1 — 依赖健康（维度 3）

**Files:**
- Modify: `SAGE_SRC/health-report.md`
- Read: `WIKI_DIR/.sage/wiki.db`（sqlite3 查询）
- Read: `WIKI_DIR/.manifest.json`

- [ ] **Step 1: Go 模块验证（3.1-3.2）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 3.1 go mod verify
go mod verify 2>&1
# 预期: "all modules verified"

# 3.2 go mod tidy 无 diff
go mod tidy 2>&1
git diff go.mod go.sum
# 预期: 空 diff
git checkout go.mod go.sum  # 恢复
```

- [ ] **Step 2: API key 验证（3.3-3.4）**

```bash
# 3.3 SILICONFLOW_API_KEY
[ -n "$SILICONFLOW_API_KEY" ] && echo "PASS: SILICONFLOW_API_KEY set" || echo "FAIL: SILICONFLOW_API_KEY missing"

# 3.4 MINIMAX_API_KEY
[ -n "$MINIMAX_API_KEY" ] && echo "PASS: MINIMAX_API_KEY set" || echo "FAIL: MINIMAX_API_KEY missing"
```

- [ ] **Step 3: 数据库验证（3.5-3.7）**

```bash
# 3.5 DB 文件存在
ls -la /Users/kellen/claude-workspace/wiki/.sage/wiki.db
# 预期: 文件存在，大小 > 0

# 3.6 FTS5 表
sqlite3 /Users/kellen/claude-workspace/wiki/.sage/wiki.db ".tables" | grep -i fts
# 预期: 包含 fts 相关表名

# 3.7 向量表
sqlite3 /Users/kellen/claude-workspace/wiki/.sage/wiki.db ".tables" | grep -i vec
# 预期: 包含 vec 相关表名
```

- [ ] **Step 4: 配置和模板验证（3.8-3.10）**

```bash
# 3.8 config.yaml 可解析（通过 status 间接验证，已在 Task 2 完成）

# 3.9 prompts 模板完整
ls /Users/kellen/claude-workspace/wiki/prompts/ | wc -l
# 预期: 9（caption-image, extract-concepts, summarize-*, write-article）
ls /Users/kellen/claude-workspace/wiki/prompts/

# 3.10 manifest 合法
python3 -c "import json; json.load(open('/Users/kellen/claude-workspace/wiki/.manifest.json')); print('VALID JSON')"
# 预期: VALID JSON
```

- [ ] **Step 5: 记录结果到报告**

整理到 `health-report.md` 的 `### 3. 依赖健康` section。

---

### Task 5: L1 — 错误处理（维度 4）

**Files:**
- Modify: `SAGE_SRC/health-report.md`

- [ ] **Step 1: 无参数 / 错误参数测试（4.1-4.5）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 4.1 无参数
./sage-wiki 2>&1 | head -3
# 预期: 显示 usage

# 4.2 search 无查询词
./sage-wiki search --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"
# 预期: 错误提示，非 panic

# 4.3 write 不存在的源
./sage-wiki write nonexist --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"
# 预期: 优雅报错

# 4.4 ontology 无效子命令
./sage-wiki ontology foo --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"
# 预期: unknown command 提示

# 4.5 hub search 无注册项目（如适用）
./sage-wiki hub search "test" --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"
# 预期: 信息提示
```

- [ ] **Step 2: JSON 错误信封验证（4.6）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 对上述错误命令加 --format json，检查输出
./sage-wiki search --project /Users/kellen/claude-workspace/wiki --format json 2>&1
./sage-wiki write nonexist --project /Users/kellen/claude-workspace/wiki --format json 2>&1
./sage-wiki ontology foo --project /Users/kellen/claude-workspace/wiki --format json 2>&1
# 预期: 每个都输出 JSON 格式的错误信息（或至少不输出乱码）
```

- [ ] **Step 3: 不存在的 config 路径（4.7）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
./sage-wiki --config /tmp/nonexistent-config.yaml status 2>&1; echo "EXIT:$?"
# 预期: 清晰报错，exit != 0
```

- [ ] **Step 4: 记录结果到报告**

整理到 `health-report.md` 的 `### 4. 错误处理` section。

---

### Task 6: L1 — 补漏检查（A/E/F/H/J/L）

**Files:**
- Modify: `SAGE_SRC/health-report.md`
- Read: `~/.claude/skills/wiki-improve/SKILL.md`
- Read: `~/.claude/skills/wiki/SKILL.md`
- Read: `SAGE_SRC/README.md`
- Read: `SAGE_SRC/CHANGELOG.md`

- [ ] **Step 1: Skill 功能验证（A.1-A.4）**

```bash
# A.1 wiki-improve 无 MCP 引用
grep -c "mcp__" ~/.claude/skills/wiki-improve/SKILL.md
# 预期: 0

# A.2 wiki-improve CLI 路径
grep -c "sage-wiki" ~/.claude/skills/wiki-improve/SKILL.md
# 预期: > 0（引用 CLI 命令）

# A.3 wiki skill 路由
cat ~/.claude/skills/wiki/SKILL.md 2>/dev/null | grep -c "sage-wiki"
# 预期: > 0 或文件不存在

# A.4 skill 子命令 vs CLI
# 从 skill 文件中提取引用的子命令列表，与 ./sage-wiki --help 对比
```

Read `~/.claude/skills/wiki-improve/SKILL.md`，提取所有 `sage-wiki <subcmd>` 引用，对比 CLI 实际命令列表。

- [ ] **Step 2: 文档一致性（E.1-E.2）**

Read `SAGE_SRC/README.md`，检查：
- E.1: 是否列出了 status/list/diff/write/ontology/search/hub/learn/capture/add-source 等新命令
- E.2: 注意 README 是上游文档，新增的 CLI 命令可能不在其中（这是 WARN 不是 FAIL）

Read `SAGE_SRC/CHANGELOG.md`，检查是否有 CLI 迁移记录。

- [ ] **Step 3: Hub 配置初始化（F.1-F.3）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# F.1 hub add 创建配置
./sage-wiki hub add --name test-project --path /tmp/test-wiki --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"
# 预期: 成功或显示用法

# F.2 检查生成的 hub config
# 根据 F.1 结果检查 hub config 文件位置和格式

# F.3 重复添加
# 重复执行 F.1 的命令，观察行为
```

- [ ] **Step 4: Prompt 模板兼容性（H.1-H.2）**

```bash
# H.1 提取模板变量
grep -h '{{\..*}}' /Users/kellen/claude-workspace/wiki/prompts/*.md | sort -u
# 与 Go 代码中的模板传参对比

# H.2 检查 prompts/ 列表 vs 代码引用
ls /Users/kellen/claude-workspace/wiki/prompts/
grep -rh "prompts/" /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki/internal/ --include="*.go" | grep -o '"[^"]*"' | sort -u
```

- [ ] **Step 5: 集成测试 + Git 状态（J.1-J.2, L.1-L.3）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# J.1 集成测试（可能需要 API，记录 skip 如果失败）
go test -v -run Integration -timeout 60s . 2>&1 | tail -10; echo "EXIT:$?"

# L.1 工作目录状态
git status --short

# L.2 与 upstream 偏移
git log upstream/main..HEAD --oneline | wc -l

# L.3 未跟踪文件
git status --short | grep "^??" | head -10
```

- [ ] **Step 6: 记录结果到报告**

整理到 `health-report.md` 的补漏 section。

---

### Task 7: L1 总结 + L2 门控判断

**Files:**
- Modify: `SAGE_SRC/health-report.md`

- [ ] **Step 1: 统计 L1 结果**

汇总 Task 2-6 的 PASS/WARN/FAIL 计数。

- [ ] **Step 2: L2 门控判断**

检查条件：
- 维度 1（集成）：CLI 命令能执行（1.4-1.14 中关键命令 PASS）
- 维度 3（依赖）：API key 存在（3.3-3.4 PASS）
- 维度 3（依赖）：DB 文件存在（3.5 PASS）

如果以上全部 PASS → 进入 L2。
如果有 FAIL → 记录"L2 被阻断"及原因，标记 L2 所有项为 SKIP。

- [ ] **Step 3: 记录门控结果**

追加到 `health-report.md`：

```markdown
## L1 → L2 门控
状态: PASS/BLOCKED
原因: ...
```

- [ ] **Step 4: Commit L1 报告**

```bash
git add health-report.md
git commit -m "chore: complete L1 mechanical verification"
```

---

### Task 8: L2 — 数据管线 + 数据变异（维度 5-6）

**前置条件:** Task 7 门控 PASS

**Files:**
- Modify: `SAGE_SRC/health-report.md`

- [ ] **Step 1: 编译输出目录结构（5.6）**

```bash
ls /Users/kellen/claude-workspace/wiki/wiki/
# 预期: summaries/ concepts/ articles/ 或类似结构
```

- [ ] **Step 2: 全量编译（5.1）— 耗时较长，需要 API**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
time ./sage-wiki compile --fresh --project /Users/kellen/claude-workspace/wiki 2>&1 | tail -20
# 记录: 成功数/总数、耗时、错误信息
# 预期: ≥27/27 成功，0 错误
```

注意：此步骤消耗 API 额度，如果用户希望跳过可标 SKIP。

- [ ] **Step 3: 编译基线对比（5.2）**

```bash
# 比较编译产出数量与上次基线（27 摘要 / 66 概念 / 66 文章）
find /Users/kellen/claude-workspace/wiki/wiki/ -name "*.md" -type f | wc -l
# 分别统计各子目录
```

- [ ] **Step 4: Lint（5.3）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
./sage-wiki lint --project /Users/kellen/claude-workspace/wiki 2>&1 | tail -5
# 记录问题数，对比基线 ~4036
```

- [ ] **Step 5: Diff 漂移检查（5.4）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
./sage-wiki diff --project /Users/kellen/claude-workspace/wiki 2>&1 | head -20
# 预期: 无意外差异（或仅有 fresh compile 带来的时间戳差异）
```

- [ ] **Step 6: Manifest 一致性（5.5）**

```bash
# 统计 manifest 条目数
python3 -c "import json; m=json.load(open('/Users/kellen/claude-workspace/wiki/.manifest.json')); print(f'Manifest entries: {len(m) if isinstance(m, list) else len(m.get(\"sources\", m.get(\"entries\", [])))}')"

# 统计实际 raw/ 文件数
find /Users/kellen/claude-workspace/wiki/raw/ -type f | wc -l
```

- [ ] **Step 7: 数据变异检查（6.1-6.5）**

```bash
# 6.1 各源类型编译产出
ls /Users/kellen/claude-workspace/wiki/raw/regulation/ | wc -l
ls /Users/kellen/claude-workspace/wiki/raw/inbox/ | wc -l

# 6.2 中文内容抽检
head -20 /Users/kellen/claude-workspace/wiki/wiki/articles/*.md 2>/dev/null | head -30
# 检查: 无乱码

# 6.3 空文章检查
find /Users/kellen/claude-workspace/wiki/wiki/ -name "*.md" -empty | wc -l
# 预期: 0

# 6.4 摘要长度抽检
wc -c /Users/kellen/claude-workspace/wiki/wiki/summaries/*.md 2>/dev/null | sort -n | head -5
# 预期: 最短摘要 > 100 字节
```

- [ ] **Step 8: 记录结果到报告 + Commit**

整理到 `health-report.md` L2 section。

```bash
git add health-report.md
git commit -m "chore: complete L2 data pipeline + variation checks"
```

---

### Task 9: L2 — 边界条件 + 状态一致性（维度 7-8）

**Files:**
- Modify: `SAGE_SRC/health-report.md`
- Read: `SAGE_SRC/internal/config/merge.go`
- Read: `SAGE_SRC/internal/ontology/relations.go`（或类似路径）

- [ ] **Step 1: Config extends 单元测试（7.7）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
go test -v ./internal/config/... 2>&1
# 预期: PASS
```

- [ ] **Step 2: Config extends 边界测试（7.1-7.3）**

```bash
# 7.1 构造 parent+child 测试
mkdir -p /tmp/sage-wiki-config-test
cat > /tmp/sage-wiki-config-test/parent.yaml << 'YAML'
llm:
  model: test-model
  max_tokens: 1000
YAML

cat > /tmp/sage-wiki-config-test/child.yaml << 'YAML'
extends: parent.yaml
llm:
  max_tokens: 2000
YAML

# 验证: 使用 Go test 或 CLI 加载 child.yaml

# 7.2 不存在的 parent
cat > /tmp/sage-wiki-config-test/bad.yaml << 'YAML'
extends: nonexistent.yaml
YAML

./sage-wiki status --config /tmp/sage-wiki-config-test/bad.yaml 2>&1; echo "EXIT:$?"
# 预期: 清晰报错

# 7.3 循环引用
cat > /tmp/sage-wiki-config-test/a.yaml << 'YAML'
extends: b.yaml
YAML
cat > /tmp/sage-wiki-config-test/b.yaml << 'YAML'
extends: a.yaml
YAML

./sage-wiki status --config /tmp/sage-wiki-config-test/a.yaml 2>&1; echo "EXIT:$?"
# 预期: 报错，不死循环

# 清理
rm -rf /tmp/sage-wiki-config-test
```

- [ ] **Step 3: 状态一致性检查（8.1-8.6）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 8.1 ontology 实体数 vs 编译概念数
ENTITY_COUNT=$(./sage-wiki ontology query --type entity --project /Users/kellen/claude-workspace/wiki 2>/dev/null | wc -l)
CONCEPT_COUNT=$(find /Users/kellen/claude-workspace/wiki/wiki/concepts/ -name "*.md" -type f 2>/dev/null | wc -l)
echo "Entities: $ENTITY_COUNT, Concepts: $CONCEPT_COUNT"
# 预期: 数值接近（可能不完全相等，取决于 header 行）

# 8.2 搜索索引同步
./sage-wiki search "法规" --project /Users/kellen/claude-workspace/wiki 2>&1 | head -3
# 预期: 有结果（假设有法规类文档）

# 8.3 FTS5 索引条目数
sqlite3 /Users/kellen/claude-workspace/wiki/.sage/wiki.db "SELECT COUNT(*) FROM wiki_fts;" 2>&1
# 注意: 表名可能不同，先查 .tables

# 8.4 向量索引条目数
sqlite3 /Users/kellen/claude-workspace/wiki/.sage/wiki.db "SELECT COUNT(*) FROM embeddings;" 2>&1
# 注意: 表名可能不同

# 8.6 config relations vs 代码默认
grep -A 20 "relations:" /Users/kellen/claude-workspace/wiki/config.yaml
```

Read `internal/ontology/` 目录找到 relations 定义文件，对比 config.yaml 中的 relations 列表。

- [ ] **Step 4: 记录结果到报告**

整理到 `health-report.md` 的维度 7-8 section。

---

### Task 10: L2 — Web UI + eval.py + 混合搜索（补漏 C/I/M）

**Files:**
- Modify: `SAGE_SRC/health-report.md`

- [ ] **Step 1: Web UI 构建（C.1-C.4）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki/web

# C.1 npm install
npm install 2>&1 | tail -3; echo "EXIT:$?"

# C.2 npm run build
npm run build 2>&1 | tail -5; echo "EXIT:$?"

# C.3 TypeScript 类型检查
npx tsc --noEmit 2>&1; echo "EXIT:$?"

# C.4 Vite 输出
ls dist/index.html 2>&1
# 预期: 存在
```

- [ ] **Step 2: eval.py 验证（I.1-I.3）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# I.1 语法检查
python3 -m py_compile eval.py 2>&1; echo "EXIT:$?"

# I.2 测试（可能需要依赖）
python3 -m pytest eval_test.py -v 2>&1 | tail -10; echo "EXIT:$?"
# 如果缺依赖标 WARN

# I.3 help
python3 eval.py --help 2>&1 | head -5; echo "EXIT:$?"
```

- [ ] **Step 3: 混合搜索管线（M.1-M.3）**

```bash
# M.1 FTS5 直查
sqlite3 /Users/kellen/claude-workspace/wiki/.sage/wiki.db "SELECT * FROM wiki_fts WHERE wiki_fts MATCH '法规' LIMIT 3;" 2>&1
# 注意: 表名和语法可能需要调整

# M.2 向量搜索（通过 CLI verbose 模式观察）
./sage-wiki search "金融监管" --project /Users/kellen/claude-workspace/wiki -vv 2>&1 | grep -i "vector\|fts\|hybrid" | head -5
# 预期: 日志中能看到 FTS 和 vector 两路搜索

# M.3 混合搜索结果
./sage-wiki search "金融监管" --project /Users/kellen/claude-workspace/wiki 2>&1 | head -10
# 预期: 有结果
```

- [ ] **Step 4: 记录结果到报告 + Commit**

```bash
git add health-report.md
git commit -m "chore: complete L2 supplementary checks (WebUI, eval, hybrid search)"
```

---

### Task 11: L2 — 端到端工作流（补漏 G）

**Files:**
- Modify: `SAGE_SRC/health-report.md`

- [ ] **Step 1: 工作流链路验证说明**

端到端测试需要构造测试数据并在真实环境执行。为避免污染正式 wiki 数据，在 /tmp 创建测试项目。

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 创建临时测试项目
./sage-wiki init --project /tmp/sage-wiki-e2e-test 2>&1; echo "EXIT:$?"
```

- [ ] **Step 2: add-source → list 链路（G.1）**

```bash
# 创建测试源文件
mkdir -p /tmp/sage-wiki-e2e-test/raw
echo "# 测试文档\n\n这是一个端到端测试文档。" > /tmp/sage-wiki-e2e-test/raw/test-doc.md

# add-source
./sage-wiki add-source /tmp/sage-wiki-e2e-test/raw/test-doc.md --project /tmp/sage-wiki-e2e-test 2>&1; echo "EXIT:$?"

# list 验证
./sage-wiki list --project /tmp/sage-wiki-e2e-test 2>&1
# 预期: 能看到 test-doc
```

- [ ] **Step 3: learn + capture 链路（G.4-G.5）**

```bash
# G.4 learn
./sage-wiki learn "端到端测试知识条目" --project /tmp/sage-wiki-e2e-test 2>&1; echo "EXIT:$?"

# G.5 capture
./sage-wiki capture "端到端测试捕获内容" --project /tmp/sage-wiki-e2e-test 2>&1; echo "EXIT:$?"
```

- [ ] **Step 4: 清理 + 记录**

```bash
rm -rf /tmp/sage-wiki-e2e-test
```

注意：compile → search → ontology 链路（G.2-G.3）需要 API 调用，已在 Task 8 的全量编译中间接验证。如需显式验证，在 L2 编译完成后运行 search 和 ontology query 确认新编译内容可查。

- [ ] **Step 5: 记录结果到报告**

整理到 `health-report.md` 的 `### 补漏 G: 端到端工作流` section。

---

### Task 12: L3 — 并发 + Race 检测（维度 9）

**前置条件:** L2 完成

**Files:**
- Modify: `SAGE_SRC/health-report.md`

- [ ] **Step 1: Go race 检测（9.3）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
go test -race ./... -timeout 120s 2>&1 | tail -20; echo "EXIT:$?"
# 预期: 无 data race 警告
# 注意: -race 会显著增加测试时间
```

- [ ] **Step 2: Hub 联邦搜索并发（9.1-9.2）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 9.2 单项目退化
./sage-wiki hub search "测试" --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"
# 预期: 正常返回（即使只有 0-1 个注册项目）

# 9.1 多项目（如果有多个注册项目）
# 如果只有 1 个项目，标 SKIP 并记录原因
```

- [ ] **Step 3: 联邦搜索超时（9.4）**

```bash
# 注册一个不可达项目
./sage-wiki hub add --name unreachable --path /tmp/nonexistent-wiki --project /Users/kellen/claude-workspace/wiki 2>&1

# 搜索（应在超时后返回，不应挂起）
timeout 30 ./sage-wiki hub search "测试" --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"
# 预期: 在合理时间内返回

# 清理: 移除不可达项目
./sage-wiki hub remove --name unreachable --project /Users/kellen/claude-workspace/wiki 2>&1
```

- [ ] **Step 4: 记录结果到报告**

整理到 `health-report.md` 的 `### 9. 并发` section。

---

### Task 13: L3 — 恢复/降级（维度 10）

**Files:**
- Modify: `SAGE_SRC/health-report.md`

**重要：每个测试前备份，测试后立即恢复。**

- [ ] **Step 1: API key 缺失（10.1）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 备份
SAVED_SF_KEY="$SILICONFLOW_API_KEY"
SAVED_MM_KEY="$MINIMAX_API_KEY"

# 测试
unset SILICONFLOW_API_KEY
unset MINIMAX_API_KEY
./sage-wiki status --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"
# 预期: 清晰报错或 status 仍能显示（不依赖 API）

# 恢复
export SILICONFLOW_API_KEY="$SAVED_SF_KEY"
export MINIMAX_API_KEY="$SAVED_MM_KEY"
```

注意：Bash 工具每次调用是新 shell，环境变量恢复需在同一命令中完成。

- [ ] **Step 2: DB 缺失 + 损坏（10.2-10.3）**

```bash
WIKI=/Users/kellen/claude-workspace/wiki
SAGE=/Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 10.2 DB 缺失
cp "$WIKI/.sage/wiki.db" "$WIKI/.sage/wiki.db.bak" && \
mv "$WIKI/.sage/wiki.db" "$WIKI/.sage/wiki.db.tmp" && \
$SAGE/sage-wiki status --project "$WIKI" 2>&1; echo "EXIT:$?" && \
mv "$WIKI/.sage/wiki.db.tmp" "$WIKI/.sage/wiki.db"
# 预期: 清晰报错，不 panic

# 10.3 DB 损坏
cp "$WIKI/.sage/wiki.db" "$WIKI/.sage/wiki.db.bak2" && \
echo "GARBAGE" > "$WIKI/.sage/wiki.db" && \
$SAGE/sage-wiki status --project "$WIKI" 2>&1; echo "EXIT:$?" && \
cp "$WIKI/.sage/wiki.db.bak2" "$WIKI/.sage/wiki.db"
# 预期: 报错，不 panic

# 清理备份
rm -f "$WIKI/.sage/wiki.db.bak" "$WIKI/.sage/wiki.db.bak2"
```

- [ ] **Step 3: Config 缺失（10.5）**

```bash
WIKI=/Users/kellen/claude-workspace/wiki
SAGE=/Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

mv "$WIKI/config.yaml" "$WIKI/config.yaml.bak" && \
$SAGE/sage-wiki status --project "$WIKI" 2>&1; echo "EXIT:$?" && \
mv "$WIKI/config.yaml.bak" "$WIKI/config.yaml"
# 预期: 清晰报错
```

- [ ] **Step 4: 记录结果到报告**

整理到 `health-report.md` 的 `### 10. 恢复/降级` section。
项 10.4（网络不可达）和 10.6（中断后重编译）标 SKIP（需要特殊环境构造，风险高于收益）。

---

### Task 14: L3 — 规模/时间 + 权限 + Docker + TUI（维度 11-12 + D/K）

**Files:**
- Modify: `SAGE_SRC/health-report.md`
- Read: `SAGE_SRC/Dockerfile`

- [ ] **Step 1: 性能基线（11.1-11.3）**

注意：11.1 全量编译耗时已在 Task 8 Step 2 记录。如果 Task 8 跳过了编译，此处记录 SKIP。

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 11.2 搜索响应时间
time ./sage-wiki search "金融" --project /Users/kellen/claude-workspace/wiki 2>&1 | tail -3
# 预期: real < 2s

# 11.3 lint 时间
time ./sage-wiki lint --project /Users/kellen/claude-workspace/wiki 2>&1 | tail -3
```

- [ ] **Step 2: Manifest 过期检测（11.4）**

```bash
WIKI=/Users/kellen/claude-workspace/wiki
SAGE=/Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# 记录当前 diff 状态
$SAGE/sage-wiki diff --project "$WIKI" 2>&1 | wc -l

# touch 一个源文件改时间戳
touch "$WIKI/raw/regulation/$(ls $WIKI/raw/regulation/ | head -1)" 2>/dev/null

# 再次 diff
$SAGE/sage-wiki diff --project "$WIKI" 2>&1 | wc -l
# 预期: diff 输出增加（检测到变更）
```

- [ ] **Step 3: 权限检查（12.1-12.5）**

```bash
WIKI=/Users/kellen/claude-workspace/wiki
ls -la "$WIKI/.sage/wiki.db"      # 12.1
ls -ld "$WIKI/raw/"               # 12.2
ls -ld "$WIKI/wiki/"              # 12.3
ls -ld "$WIKI/.sage/"             # 12.4
ls -la "$WIKI/config.yaml"        # 12.5
# 预期: 用户 kellen 有 rw 权限
```

- [ ] **Step 4: Docker 构建验证（D.1-D.2）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# D.2 检查 Dockerfile 中 Vite 路径
grep -n "dist\|build\|web" Dockerfile
# 对比 web/vite.config.ts 中的 outDir

# D.1 Docker build（可能较慢）
docker build -t sage-wiki-test . 2>&1 | tail -10; echo "EXIT:$?"
docker rmi sage-wiki-test 2>/dev/null
# 如果 docker 不可用，标 SKIP
```

- [ ] **Step 5: TUI 子系统（K.1-K.3）**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki

# K.1 TUI 包编译
go build ./internal/tui/... 2>&1; echo "EXIT:$?"

# K.2 TUI 测试
go test ./internal/tui/... 2>&1; echo "EXIT:$?"

# K.3 isatty（非交互环境下 tui 命令行为）
echo "" | ./sage-wiki tui --project /Users/kellen/claude-workspace/wiki 2>&1; echo "EXIT:$?"
# 预期: 检测到非 TTY，优雅退出或显示提示
```

- [ ] **Step 6: 记录结果到报告 + Commit**

```bash
git add health-report.md
git commit -m "chore: complete L3 stress/boundary checks"
```

---

### Task 15: 报告整合 + 最终统计

**Files:**
- Modify: `SAGE_SRC/health-report.md`

- [ ] **Step 1: 统计全部结果**

遍历 health-report.md 中所有检查项，统计：
- PASS 数量
- WARN 数量
- FAIL 数量
- SKIP 数量

- [ ] **Step 2: 填充总览数字**

回到报告顶部的"总览" section，填入实际数字。

- [ ] **Step 3: 编写发现汇总**

按严重度排序所有 WARN 和 FAIL 项，整理到 `## 发现汇总` section：

```markdown
## 发现汇总（按严重度排序）
| # | 严重度 | 维度 | 问题描述 | 修复建议 |
|---|--------|------|---------|---------|
| 1 | ... | ... | ... | ... |
```

- [ ] **Step 4: 编写基线数据**

```markdown
## 基线数据
- 编译: X/Y 成功
- 编译耗时: Xs
- Lint 问题数: N
- 本体实体数: N
- FTS5 条目数: N
- 向量条目数: N
- 搜索响应时间: Xs
```

- [ ] **Step 5: 编写下一步建议**

基于发现汇总，按优先级排列修复任务建议。

- [ ] **Step 6: Final commit**

```bash
git add health-report.md
git commit -m "docs: complete sage-wiki comprehensive health check report

24 check domains, ~130 items across L1/L2/L3 layers.
Post MCP→CLI migration validation."
```

---

## Task 依赖关系

```
Task 1 (Setup)
  └── Task 2 (L1: 集成)
  └── Task 3 (L1: 迁移)
  └── Task 4 (L1: 依赖)
  └── Task 5 (L1: 错误)
  └── Task 6 (L1: 补漏)
        └── Task 7 (L1 门控)
              ├── Task 8 (L2: 管线+变异) ──┐
              ├── Task 9 (L2: 边界+一致性) │
              ├── Task 10 (L2: UI+eval+搜索)│
              └── Task 11 (L2: E2E)        │
                    ├── Task 12 (L3: 并发)  │
                    ├── Task 13 (L3: 恢复)  │
                    └── Task 14 (L3: 规模等)│
                          └── Task 15 (整合) ◄┘
```

Task 2-6 可并行执行。Task 8-11 可并行执行（除 G.2-G.3 依赖 Task 8 编译结果）。Task 12-14 可并行执行。
