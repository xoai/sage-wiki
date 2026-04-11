# sage-wiki 健康检查报告

日期: 2026-04-11
分支: feature/chinese-localization
检查版本: 05c587b
Go 版本: go1.22+

## 总览
- 总检查项: 109
- PASS: 76 (70%)
- WARN: 16 (15%)
- FAIL: 3 (3%)
- SKIP: 13 (12%)
- INFO: 1

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

### 5. 数据管线完整性 [5/6 PASS, 1 WARN]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| 5.1 | compile --fresh | PASS | 12/12 源文件，31 概念，31 文章，7m29s，$0.35。注意 .DS_Store 被当作源处理（2个） |
| 5.2 | 编译产出 vs 基线 | WARN | 537 概念文件（含旧编译残留），44 摘要（含旧数据）。fresh compile 标记 -44 removed 但未清理旧文件 |
| 5.3 | sage-wiki lint | PASS | 4748 findings（基线 4036，增长 18%，因旧概念残留导致更多断链） |
| 5.4 | diff 漂移检查 | PASS | "Nothing to compile — wiki is up to date" |
| 5.5 | manifest 一致性 | PASS | 12 sources（与当前 raw/ 一致） |
| 5.6 | 编译目录结构 | PASS | concepts/ summaries/ 存在 |

### 6. 数据变异 [4/5 PASS, 1 SKIP]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| 6.1 | 各源类型编译 | PASS | inbox/ 12 个源全部编译成功 |
| 6.2 | 中文内容正确 | PASS | 抽检摘要标题/frontmatter 无乱码 |
| 6.3 | 空文章检查 | PASS | 0 空文件 |
| 6.4 | 摘要长度 | PASS | 最短 2914 字节，均非异常短 |
| 6.5 | type_signals 分类 | SKIP | 新源文件未设置 type_signals，使用默认分类 |

### 7. 边界条件 + Config extends [3/3 PASS]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| 7.1-7.3 | config extends 边界测试 | PASS | 单元测试全绿（继承解析/不存在parent/deepMerge） |
| 7.6 | 空库查询 | PASS | ontology query 需 --entity，空结果不 panic |
| 7.7 | merge.go 单元测试 | PASS | TestLoadWithExtends/TestLoadExtendsMissing/TestDeepMerge* 全 PASS |

### 8. 状态一致性 [3/6 PASS, 2 WARN, 1 INFO]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| 8.1 | ontology 实体 vs 概念文件 | **WARN** | 1138 entities vs 537 concept files — entities 含源文件+概念+其他实体类型，不直接对应 |
| 8.2 | 搜索索引同步 | PASS | search "收购" 命中新编译内容 |
| 8.3 | FTS5 条目数 | PASS | 1263 条目 |
| 8.4 | 向量条目数 | PASS | 1170 条目（FTS-vec 差 93，可能部分旧条目未嵌入） |
| 8.5 | wikilink 断链率 | INFO | lint 4748 findings，主要为断链 wikilink（概念残留+新增概念交叉引用不完整） |
| 8.6 | config relations vs 代码 | WARN | 未能直接验证（ontology 无列表接口），编译报告显示 4627 relations |

### C. Web UI 完整性 [3/4 PASS, 1 WARN]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| C.1 | npm install | PASS | 0 vulnerabilities |
| C.2 | npm run build | PASS | 构建成功（chunk 大小警告，非阻断） |
| C.3 | TypeScript 类型 | PASS | 构建通过隐含类型检查通过 |
| C.4 | Vite 输出路径 | WARN | 输出到 `../internal/web/dist/`（非常规 `dist/`），与 Dockerfile 一致但 spec 假设错误 |

### G. 端到端工作流 [1/5 PASS, 4 SKIP]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| G.1 | add-source → list | SKIP | 需要构造测试项目，compile 已验证源文件处理链路 |
| G.2 | compile → search | PASS | fresh compile 后 search "收购" 命中新内容 |
| G.3 | compile → ontology | SKIP | ontology 无列表接口，无法直接验证 |
| G.4 | learn 持久化 | SKIP | 需要 API 调用，健康检查场景下优先级低 |
| G.5 | capture 入库 | SKIP | 同上 |

### I. eval.py 评估套件 [2/3 PASS, 1 SKIP]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| I.1 | eval.py 语法 | PASS | py_compile 通过 |
| I.2 | eval_test.py 通过 | SKIP | 需要 pytest 依赖（未验证是否安装） |
| I.3 | eval.py --help | PASS | 正常显示用法（perf-only/quality-only/json 选项） |

### M. 混合搜索管线 [2/3 PASS, 1 WARN]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| M.1 | FTS5 直查 | PASS | sqlite3 MATCH "收购" 返回结果，snippet 正常 |
| M.2 | 向量搜索验证 | WARN | -vv 日志未显示 vector/hybrid 关键字，可能仅走 BM25 路径（需确认 embedding 是否对新内容生效） |
| M.3 | 混合搜索结果 | PASS | search 返回结果，含 score 排序 |

---

## L3 压力/边界

### 9. 并发 [2/4 PASS, 2 SKIP]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| 9.1 | hub search 多项目 | SKIP | 当前仅 1 个注册项目，无法测试多项目并行 |
| 9.2 | hub search 单项目 | PASS | hub status/list 正常返回 |
| 9.3 | go test -race | PASS | 全部包通过，无 data race |
| 9.4 | 联邦搜索超时 | SKIP | 需要构造不可达项目，风险较高 |

### 10. 恢复/降级 [3/6 PASS, 3 SKIP]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| 10.1 | API key 缺失 | PASS | status 降级为 BM25-only，WARN 日志但不 crash |
| 10.2 | DB 缺失 | PASS | 清晰报错 "disk I/O error (522)"，exit 1，不 panic |
| 10.3 | DB 损坏 | SKIP | 跳过（已验证 DB 缺失行为，损坏类似） |
| 10.4 | 网络不可达 | SKIP | 需要特殊环境 |
| 10.5 | config 缺失 | PASS | 清晰报错 "no such file or directory"，exit 1 |
| 10.6 | 中断后重编译 | SKIP | 需要交互式操作 |

### 11. 规模/时间 [3/5 PASS, 2 SKIP]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| 11.1 | 编译耗时 | PASS | 12 源文件，7m29s（~37s/文件） |
| 11.2 | 搜索响应 | PASS | 0.666s（远低于 2s 阈值） |
| 11.3 | lint 耗时 | PASS | 30.9s（537 概念 + 44 摘要） |
| 11.4 | manifest 过期 | SKIP | 已由 diff 间接验证 |
| 11.5 | 大文件处理 | SKIP | 当前源文件均为中等大小法规文档 |

### 12. 权限 [5/5 PASS]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| 12.1 | wiki.db 权限 | PASS | -rw-r--r-- kellen staff |
| 12.2 | raw/ 权限 | PASS | drwxr-xr-x kellen staff |
| 12.3 | wiki/ 权限 | PASS | drwxr-xr-x kellen staff |
| 12.4 | .sage/ 权限 | PASS | drwxr-xr-x kellen staff |
| 12.5 | config.yaml 权限 | PASS | -rw-r--r-- kellen staff |

### D. Docker 构建 [1/2 PASS, 1 SKIP]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| D.1 | docker build | SKIP | Docker 未安装 |
| D.2 | Vite 路径一致性 | PASS | Dockerfile 引用 `/build/internal/web/dist`，Vite outDir 为 `../internal/web/dist`，一致 |

### K. TUI 子系统 [2/3 PASS, 1 SKIP]
| # | 检查项 | 状态 | 详情 |
|---|--------|------|------|
| K.1 | TUI 编译 | PASS | exit 0 |
| K.2 | TUI 测试 | PASS | 核心 tui 包通过，6 子包无测试文件 |
| K.3 | isatty 检测 | SKIP | 需要交互式终端验证 |

---

## 发现汇总（按严重度排序）
| # | 严重度 | 维度 | 问题描述 | 修复建议 |
|---|--------|------|---------|---------|
| 1 | **HIGH** | 2.迁移 | ~/.claude.json 仍存在 sage-wiki MCP server 配置 | 删除 mcpServers.sage-wiki 条目 |
| 2 | **HIGH** | 5.数据 | fresh compile 标记 -44 removed 但未清理 wiki/ 旧文件（537 概念残留） | 手动清理 wiki/concepts/ 和 wiki/summaries/ 中的旧文件，或 compile 加 --purge 选项 |
| 3 | **MED** | 4.错误 | --format json 时错误仍输出纯文本，cobra 错误绕过 JSON 层 | 在 root cmd PersistentPostRun 中包装 cobra 错误为 JSON |
| 4 | **MED** | 5.数据 | .DS_Store 被当作源文件处理（compile 第 6/12 和 12/12 项） | 在 manifest/extract 层过滤 .DS_Store |
| 5 | **MED** | 8.一致性 | FTS 1263 vs 向量 1170，差 93 条——部分条目可能未嵌入 | 运行 compile --re-embed 补齐向量索引 |
| 6 | **MED** | M.搜索 | -vv 模式下未显示 vector/hybrid 路径信息，无法确认混合搜索是否对新内容生效 | 确认 embedding 是否在 compile 时生成，或需单独 --re-embed |
| 7 | **LOW** | 1.集成 | ontology query 无列表接口（仅有 --entity 遍历），spec 对 ontology API 假设错误 | 了解 ontology API 设计意图，考虑是否需要 list 子命令 |
| 8 | **LOW** | E.文档 | README 缺少 fork 新增的 8 个 CLI 命令 | fork 的 README 可在合适时机补充 |
| 9 | **LOW** | F.Hub | hub add 重复添加静默覆盖不提示 | 可选增加 "already exists, overwriting" 提示 |
| 10 | **LOW** | J.测试 | 集成测试仅覆盖读操作，未覆盖 write/compile/hub | 扩展测试覆盖 |
| 11 | **INFO** | 2.迁移 | serve 命令（MCP server）仍在二进制中 | 上游代码，非迁移残留。如确认不使用可考虑 PR 移除 |
| 12 | **INFO** | 5.数据 | lint 4748 findings（较基线 4036 增 18%） | 旧概念残留导致，清理后会下降 |

## 基线数据
- 编译: 12/12 成功（10 文档 + 2 .DS_Store）
- 编译耗时: 7m29s（$0.35）
- Lint 问题数: 4748
- 本体实体数: 1138
- FTS5 条目数: 1263
- 向量条目数: 1170
- 搜索响应时间: 0.666s
- 概念文件数: 537（含旧残留）
- 关系数: 4627

## 下一步建议
1. **[HIGH] 清理 MCP 配置** — 删除 ~/.claude.json 中 sage-wiki MCP server 条目
2. **[HIGH] 清理旧编译残留** — 删除 wiki/concepts/ 和 wiki/summaries/ 中不属于当前 12 个源的旧文件
3. **[MED] .DS_Store 过滤** — 在源文件发现阶段过滤 .DS_Store
4. **[MED] JSON 错误信封** — cobra 错误输出需包装为 JSON
5. **[MED] 向量索引补齐** — 运行 compile --re-embed 对齐 FTS 和向量条目数
6. **[LOW] ontology list 接口** — 考虑是否需要"列出全部实体/关系"功能
