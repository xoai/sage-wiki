---
name: wiki
description: 知识库 CLI 操作入口 — 搜索、状态、编译、lint、diff、list、ontology、facts、source、coverage 等操作，路由到 sage-wiki CLI
user_invocable: true
---

# /wiki — 知识库操作

通过 sage-wiki CLI 操作知识库。所有命令自动加 `--format json` 解析结果。

## 配置

```
SAGE=/Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki/sage-wiki
HUB_CONFIG=~/.sage-hub.yaml
DEFAULT_PROJECT=/Users/kellen/claude-workspace/wiki
```

## 命令路由

根据用户输入的子命令，构造 Bash 调用：

### 搜索

- `/wiki search <query>` → 联邦搜索（所有 searchable 项目）
  ```bash
  $SAGE hub search "<query>" --format json
  ```
- `/wiki search <query> -p <name>` → 指定项目搜索
  ```bash
  PROJECT_PATH=$($SAGE hub list --format json | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['projects']['<name>']['path'])")
  $SAGE search "<query>" --project "$PROJECT_PATH" --format json
  ```
- `/wiki search <query> --scope local|global|all` → 按 scope 搜索
  ```bash
  $SAGE search "<query>" --project $DEFAULT_PROJECT --scope <scope> --format json
  ```

### 状态

- `/wiki status` → 所有项目状态
  ```bash
  $SAGE hub status --format json
  ```
- `/wiki status <name>` → 指定项目状态
  ```bash
  $SAGE status --project <path> --format json
  ```

### 编译

- `/wiki compile` → 编译默认项目
  ```bash
  echo y | $SAGE compile --project $DEFAULT_PROJECT
  ```
- `/wiki compile --fresh` → 全量重编译
  ```bash
  echo y | $SAGE compile --fresh --project $DEFAULT_PROJECT
  ```

### Diff

- `/wiki diff` → 默认项目的待编译文件
  ```bash
  $SAGE diff --project $DEFAULT_PROJECT --format json
  ```

### Lint

- `/wiki lint` → 默认项目 lint（含 numeric-contradiction 和 orphan-facts pass）
  ```bash
  $SAGE lint --project $DEFAULT_PROJECT --format json
  ```
- `/wiki lint --fix` → 自动修复
  ```bash
  $SAGE lint --fix --project $DEFAULT_PROJECT
  ```

### List

- `/wiki list` → 列出所有实体
  ```bash
  $SAGE list --project $DEFAULT_PROJECT --format json
  ```

### Ontology

- `/wiki ontology query --entity "X"` → 查询关系图谱
  ```bash
  $SAGE ontology query --entity "X" --project $DEFAULT_PROJECT --format json
  ```

### Facts（新增）

- `/wiki facts query --entity "X"` → 查询某实体的数字
  ```bash
  $SAGE facts query --entity "X" --project $DEFAULT_PROJECT --format json
  ```
- `/wiki facts query --entity "X" --period "2024"` → 按实体+年份查询
  ```bash
  $SAGE facts query --entity "X" --period "2024" --project $DEFAULT_PROJECT --format json
  ```
- `/wiki facts query --label "营业收入"` → 按指标查询
  ```bash
  $SAGE facts query --label "营业收入" --project $DEFAULT_PROJECT --format json
  ```
- `/wiki facts stats` → 数字统计
  ```bash
  $SAGE facts stats --project $DEFAULT_PROJECT --format json
  ```
- `/wiki facts import` → 导入 .numbers.yaml
  ```bash
  $SAGE facts import --project $DEFAULT_PROJECT --format json
  ```
- `/wiki facts delete --source <file>` → 删除某文件的数字
  ```bash
  $SAGE facts delete --source "<file>" --project $DEFAULT_PROJECT
  ```

### Source（新增）

- `/wiki source list` → 源文件状态清单（提取/编译/数字三列）
  ```bash
  $SAGE source list --project $DEFAULT_PROJECT --format json
  ```
- `/wiki source show <path>` → 查看预提取内容或源文件元信息
  ```bash
  $SAGE source show "<path>" --project $DEFAULT_PROJECT --format json
  ```

### Coverage（新增）

- `/wiki coverage` → 三层覆盖率报告
  ```bash
  $SAGE coverage --project $DEFAULT_PROJECT --format json
  ```

### Hub 管理

- `/wiki hub list` → 列出注册项目
  ```bash
  $SAGE hub list --format json
  ```
- `/wiki hub add <path>` → 注册项目
  ```bash
  $SAGE hub add <path>
  ```

## 智能摘要

当用户问的不是直接 CLI 操作而是知识查询（如"什么是重大资产重组"），按以下策略：

1. 先用 `search` 找相关文章
2. 如有匹配，用 Read 读取文章内容
3. 检查 facts 表是否有相关数字：`$SAGE facts query --entity "<关键词>" --format json`
4. 综合文章+数字给出回答
5. 注明来源文章路径

## 时效提醒

- 查询结果中有 `staleness_warning` 时提醒用户
- `coverage` 显示 extracted=no 的文件建议运行 file-extract
- `coverage` 显示 facts=0 的文件建议运行 facts import

## 输出处理

- JSON 输出解析 `{"ok": true, "data": {...}}` 格式
- `ok: false` 时报告错误信息
- 搜索结果按 RRF 分数排序展示
- facts 结果按 entity + period 分组展示

## 注意

- `wiki_read` 不需要 CLI — 直接用 Read 工具读文件
- `wiki_commit` 不需要 CLI — 直接用 `git add . && git commit`
