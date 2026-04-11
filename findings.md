# Findings

## 规划阶段发现

### CLI 命令全集（20 个，spec 原列 10 个）
add-source, capture, compile, completion, diff, doctor, help, hub, ingest, init, learn, lint, list, ontology, query, search, serve, status, tui, write

### serve 命令仍存在
上游代码保留了 MCP server 的 `serve` 命令。非迁移残留，但需标注。

### compile flag 修正
全量编译用 `--fresh`，不是 `--all`。

## 执行阶段发现
（执行时填充）
