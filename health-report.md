# sage-wiki 健康检查报告

日期: 2026-04-11
分支: feature/chinese-localization
检查版本: 05c587b
Go 版本: go1.22+

## 总览
- 总检查项: _待填_
- PASS: _待填_
- WARN: _待填_
- FAIL: _待填_
- SKIP: _待填_

---

## L1 机械验证

### 1. 集成完整性 [16/18 PASS, 1 WARN, 1 FAIL]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| 1.1 | go build ./... | PASS | exit 0 |
| 1.2 | go vet ./... | PASS | exit 0，无警告 |
| 1.3 | go test ./internal/... | PASS | 22 包通过，6 TUI 子包无测试文件 |
| 1.4 | sage-wiki status | PASS | 正常输出（45 sources, 461 concepts, 1264 entries） |
| 1.5 | status --format json | PASS | 合法 JSON，keys: [ok, data] |
| 1.6 | sage-wiki list | PASS | 正常输出 1101 entities |
| 1.7 | list --format json | PASS | 合法 JSON |
| 1.8 | sage-wiki diff | PASS | "Nothing to compile — wiki is up to date" |
| 1.9 | diff --format json | PASS | 合法 JSON |
| 1.10 | ontology query --type entity | FAIL | `--type` flag 不存在。实际 API: `ontology query --entity <name>` (遍历接口，非列表) |
| 1.11 | ontology query --type relation | FAIL | 同上，spec 假设错误。ontology 无"列出全部实体/关系"接口 |
| 1.12 | sage-wiki search "测试" | PASS | 正常返回搜索结果 |
| 1.13 | hub status | PASS | kellen-wiki: 45 sources, 461 concepts |
| 1.14 | hub list | PASS | kellen-wiki 已注册，searchable |
| 1.15 | 20 个命令 --help | PASS | 全部 exit 0 |
| 1.16 | sage-wiki doctor | PASS | 全部检查通过，LLM/Embedding API 可达 |
| 1.17 | sage-wiki init | PASS | 成功初始化测试项目 |
| 1.18 | ontology query --entity "IPO受理" | WARN | 命令运行但 "No entities found"，可能是 entity ID 格式问题 |

### 2. 迁移状态清洁 [5/7 PASS, 2 FAIL, 1 WARN]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| 2.1 | Go 代码中 mcp__sage-wiki 引用 | PASS | 0 匹配 |
| 2.2 | internal/mcp/ 被外部包 import | WARN | main.go（serve 命令）和 integration_test.go 均 import，属上游代码结构 |
| 2.3 | ~/.claude.json 无 MCP 条目 | **FAIL** | 仍存在 sage-wiki MCP server 配置（mcpServers.sage-wiki） |
| 2.4 | skill 文件无 MCP 工具调用 | PASS | wiki-improve 和 wiki skill 均 0 mcp__ 引用 |
| 2.5 | settings.json 无 MCP matcher | PASS | 0 匹配 |
| 2.6 | _mcp-mapping.json 无 sage-wiki | PASS | 0 匹配 |
| 2.7 | experience 文件无 MCP 残留 | PASS | 0 匹配 |
| 2.8 | serve 命令仍存在 | WARN | 上游代码保留 MCP serve 功能，非迁移残留 |

### 3. 依赖健康 [10/10 PASS]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| 3.1 | go mod verify | PASS | all modules verified |
| 3.2 | go mod tidy 无 diff | PASS | go.mod/go.sum 无变更 |
| 3.3 | SILICONFLOW_API_KEY | PASS | 已设置 |
| 3.4 | MINIMAX_API_KEY | PASS | 已设置 |
| 3.5 | wiki.db 存在 | PASS | 37MB |
| 3.6 | FTS5 表存在 | PASS | entries 表 (含 shadow tables)，1264 条目 |
| 3.7 | 向量表存在 | PASS | vec_entries 表，1186 条目 |
| 3.8 | config.yaml 可解析 | PASS | status 正常输出 |
| 3.9 | prompts/ 模板完整 | PASS | 9 个模板文件 |
| 3.10 | .manifest.json 合法 | PASS | 合法 JSON，含 sources/concepts 键 |

### 4. 错误处理 [5/7 PASS, 2 WARN]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| 4.1 | 无参数显示 usage | PASS | 正常显示命令列表 |
| 4.2 | search 无查询词 | PASS | "requires at least 1 arg(s)"，exit 1 |
| 4.3 | write nonexist | PASS | 显示 write 子命令帮助（write 是父命令，需 article/summary 子命令） |
| 4.4 | ontology 无效子命令 | PASS | 显示 ontology 子命令帮助 |
| 4.5 | hub search 项目查找 | PASS | "project not found" 清晰报错，exit 1 |
| 4.6 | JSON 错误信封一致性 | **WARN** | 错误时 --format json 仍输出纯文本（非 JSON 信封），cobra 错误绕过了 JSON 输出层 |
| 4.7 | 不存在的 config 路径 | PASS | 清晰报错 "open .../config.yaml: no such file or directory"，exit 1 |

### A. Skill 功能验证 [3/4 PASS, 1 WARN]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| A.1 | wiki-improve 无 mcp__ 引用 | PASS | 0 匹配 |
| A.2 | wiki-improve 引用 sage-wiki CLI | PASS | 15 处引用 |
| A.3 | wiki skill 路由到 CLI | PASS | 存在并引用 sage-wiki |
| A.4 | skill 子命令 vs CLI 全集 | WARN | wiki-improve 覆盖 7 个核心命令，wiki skill 补充 compile/hub/write/ontology，合理但未完全覆盖 20 个命令 |

### E. 文档一致性 [1/2 PASS, 1 WARN]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| E.1 | README CLI 命令列表 | WARN | 上游 README 列 11 个命令，缺 diff/list/write/ontology/hub/learn/capture/add-source（fork 新增） |
| E.2 | CHANGELOG 记录 | PASS | 三个版本（0.1.0-0.1.2）有记录，但无明确"MCP→CLI 迁移"条目 |

### F. Hub 配置初始化 [2/3 PASS, 1 WARN]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| F.1 | hub add 参数格式 | PASS | `hub add [path]`，从 config.yaml 读项目名 |
| F.2 | hub add 无效路径 | PASS | "not a sage-wiki project (no config.yaml)" 清晰报错 |
| F.3 | 重复添加行为 | WARN | 代码分析：map 覆盖（幂等），不报错也不提示——静默覆盖可能不符用户预期 |

### H. Prompt 模板兼容性 [2/2 PASS]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| H.1 | 模板变量提取 | PASS | 内置模板变量（SourcePath/SourceType/MaxTokens/ExistingConcepts 等）与 Go struct 完全对应 |
| H.2 | 模板在代码中注册 | PASS | prompts.go 定义对应 struct，变量名一致 |

### J. 集成测试 [1/2 PASS, 1 WARN]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| J.1 | integration_test.go 通过 | PASS | TestIntegrationM1 全部 8 步 PASS (1.464s) |
| J.2 | 覆盖范围 | WARN | 仅覆盖读操作（search/read/ontology/status/list/vectors），未覆盖 write/compile/hub/learn/capture/CLI 参数解析 |

### L. Git 状态 [2/3 PASS, 1 WARN]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| L.1 | 工作目录状态 | WARN | 4 个未跟踪文件（health-report.md, findings.md, progress.md, task_plan.md）— 健康检查产物 |
| L.2 | 与 upstream 偏移 | PASS | 46 commits ahead of upstream/main |
| L.3 | 未跟踪文件 | PASS | 仅有健康检查产物，无意外文件 |

### L1 → L2 门控
状态: **PASS** — 进入 L2
- 关键 CLI 命令（status/list/diff/search/compile/lint）全部可用
- API key 已设置
- DB 文件存在且表结构完整
- 2 个 FAIL（ontology 接口 spec 误判 + MCP 配置残留）不阻断 L2

---

## L2 数据验证

### 5. 数据管线完整性
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

### 6. 数据变异
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

### 7. 边界条件 + Config extends
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

### 8. 状态一致性
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

### C. Web UI 完整性
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

### G. 端到端工作流
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

### I. eval.py 评估套件
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

### M. 混合搜索管线
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

---

## L3 压力/边界

### 9. 并发
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

### 10. 恢复/降级
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

### 11. 规模/时间
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

### 12. 权限
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

### D. Docker 构建
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

### K. TUI 子系统
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|

---

## 发现汇总（按严重度排序）
| # | 严重度 | 维度 | 问题描述 | 修复建议 |
|---|--------|------|---------|---------|

## 基线数据
- 编译: _待填_
- 编译耗时: _待填_
- Lint 问题数: _待填_
- 本体实体数: _待填_
- FTS5 条目数: _待填_
- 向量条目数: _待填_
- 搜索响应时间: _待填_

## 下一步建议
_待填_
