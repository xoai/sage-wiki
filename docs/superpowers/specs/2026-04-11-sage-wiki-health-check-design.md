# sage-wiki 全面健康检查设计文档

日期: 2026-04-11
分支: feature/chinese-localization
状态: 待实施

## 背景

sage-wiki 刚完成从 MCP 到 CLI-first 的大规模迁移（4 个 Phase）：
- Phase 1: CLI 命令补齐（9 个新命令 + JSON 输出格式化）
- Phase 2: Hub 多项目联邦管理（config extends、联邦搜索、hub CLI）
- Phase 3: Skill 路由迁移（wiki-improve 从 MCP 工具调用改为 CLI 命令）
- Phase 4: MCP 配置移除

需要对迁移后系统进行一次全面健康检查，确认代码、数据、配置、工具链全部正常。

## 目标

- **范围**: 代码迁移验证 + 知识库数据健康（全覆盖）
- **交付物**: 结构化健康报告（Markdown），每项标 PASS/WARN/FAIL + 严重度 + 修复建议
- **策略**: 报告优先，不即时修复。发现问题记录后另开任务处理

## 检查架构

三层执行，后层依赖前层结果：

```
┌─────────────────────────────────────────────────┐
│  L1 机械验证（自动化，无需 API 调用）            │
│  ┌───────────┬──────────┬──────────┬──────────┐ │
│  │ 1.集成    │ 2.迁移   │ 3.依赖   │ 4.错误   │ │
│  │  完整性   │  状态清洁│  健康    │  处理    │ │
│  └───────────┴──────────┴──────────┴──────────┘ │
│  补漏: A.Skill功能 E.文档一致性 F.Hub初始化      │
│        H.模板兼容性 J.集成测试 L.Git状态          │
├─────────────────────────────────────────────────┤
│  L2 数据验证（需 API + 真实数据）                │
│  ┌───────────┬──────────┬──────────┬──────────┐ │
│  │ 5.数据    │ 6.数据   │ 7.边界   │ 8.状态   │ │
│  │  管线     │  变异    │  条件    │  一致性  │ │
│  └───────────┴──────────┴──────────┴──────────┘ │
│  补漏: C.WebUI G.端到端流 I.eval.py M.混合搜索   │
├─────────────────────────────────────────────────┤
│  L3 压力/边界（构造异常场景）                    │
│  ┌───────────┬──────────┬──────────┬──────────┐ │
│  │ 9.并发    │10.恢复   │11.规模   │12.权限   │ │
│  │           │          │  /时间   │          │ │
│  └───────────┴──────────┴──────────┴──────────┘ │
│  补漏: D.Docker K.TUI                            │
└─────────────────────────────────────────────────┘
                      ↓
            结构化健康报告 (Markdown)
```

## L1 机械验证

### 维度 1：集成完整性

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| 1.1 | `go build ./...` | Bash | exit 0 |
| 1.2 | `go vet ./...` | Bash | 无警告 |
| 1.3 | `go test ./...` | Bash | 全绿 |
| 1.4 | `sage-wiki status` | CLI | 输出状态信息 |
| 1.5 | `sage-wiki status --format json` | CLI | 合法 JSON |
| 1.6 | `sage-wiki list` | CLI | 列出文件 |
| 1.7 | `sage-wiki list --format json` | CLI | 合法 JSON |
| 1.8 | `sage-wiki diff` | CLI | 输出差异或空 |
| 1.9 | `sage-wiki diff --format json` | CLI | 合法 JSON |
| 1.10 | `sage-wiki ontology query --type entity` | CLI | 输出实体列表 |
| 1.11 | `sage-wiki ontology query --type relation` | CLI | 输出关系列表 |
| 1.12 | `sage-wiki search "测试"` | CLI | 返回结果或空 |
| 1.13 | `sage-wiki hub status` | CLI | 输出 hub 状态 |
| 1.14 | `sage-wiki hub list` | CLI | 列出注册项目 |
| 1.15 | 所有命令 `--help` 可用 | CLI 逐个测试 | 每个都有 usage |

### 维度 2：迁移状态清洁

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| 2.1 | Go 代码中 MCP 工具名引用 | Grep `mcp__sage-wiki` | 0 匹配 |
| 2.2 | `internal/mcp/` 包被其他包 import | Grep `"sage-wiki/internal/mcp"` | 仅 main 或 0 |
| 2.3 | `~/.claude.json` 无 sage-wiki MCP 条目 | Read + 检查 | 无条目 |
| 2.4 | skill 文件中无 MCP 工具调用 | Grep wiki-improve skill | 0 MCP 引用 |
| 2.5 | settings.json 无 sage-wiki MCP matcher | Read | 无匹配 |
| 2.6 | `_mcp-mapping.json` 无 sage-wiki 条目 | Read | 无或已清理 |
| 2.7 | experience 文件无 MCP 残留引用 | Grep skill-experience/ | 0 残留 |

### 维度 3：依赖健康

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| 3.1 | `go mod verify` | Bash | all verified |
| 3.2 | `go mod tidy` 无 diff | Bash + git diff | 无变更 |
| 3.3 | SILICONFLOW_API_KEY 环境变量存在 | Bash | 非空 |
| 3.4 | MINIMAX_API_KEY 环境变量存在 | Bash | 非空 |
| 3.5 | SQLite DB 文件存在 | ls .sage/wiki.db | 存在 |
| 3.6 | DB FTS5 表存在 | sqlite3 查询 | 表存在 |
| 3.7 | DB 向量表存在 | sqlite3 查询 | 表存在 |
| 3.8 | config.yaml 可解析 | sage-wiki status | 无 parse error |
| 3.9 | prompts/ 目录模板完整（9 个） | ls + 对比 | 全部存在 |
| 3.10 | .manifest.json 存在且合法 | jq 验证 | 合法 JSON |

### 维度 4：错误处理

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| 4.1 | 无参数命令显示 usage | `sage-wiki` | 帮助信息 |
| 4.2 | search 无查询词 | `sage-wiki search` | 错误提示 |
| 4.3 | write 不存在的源 | `sage-wiki write nonexist` | 优雅报错 |
| 4.4 | ontology 无效子命令 | `sage-wiki ontology foo` | 错误提示 |
| 4.5 | hub search 无注册项目 | `sage-wiki hub search "x"` | 信息提示 |
| 4.6 | JSON 错误输出一致性 | 上述错误命令加 `--format json` | JSON 错误信封 |
| 4.7 | 不存在的 config 路径 | `sage-wiki --config /tmp/no.yaml status` | 清晰报错 |

### 补漏 A：Skill 功能验证

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| A.1 | wiki-improve SKILL.md 无 MCP 调用语法 | Grep | 0 `mcp__` 引用 |
| A.2 | wiki-improve 中 CLI 命令路径正确 | Read 检查 Bash 调用 | 路径指向 sage-wiki 二进制 |
| A.3 | /wiki skill 路由到 CLI | 检查 wiki skill 内容 | 调用 sage-wiki CLI |
| A.4 | skill 中引用的子命令全部存在 | 对比 skill 引用 vs CLI --help | 1:1 匹配 |

### 补漏 E：文档一致性

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| E.1 | README 中 CLI 命令列表 | Read 对比实际 | 涵盖所有新命令 |
| E.2 | CHANGELOG 记录迁移 | Read | 有 CLI 迁移条目 |

### 补漏 F：Hub 配置初始化

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| F.1 | hub init 或首次 add | CLI | 创建 hub config 文件 |
| F.2 | hub config 文件格式 | Read 生成文件 | 合法 YAML/JSON |
| F.3 | hub add 重复项目 | 添加同一项目两次 | 去重或报错 |

### 补漏 H：Prompt 模板兼容性

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| H.1 | 模板变量与代码一致 | Grep 模板 `{{.XXX}}` vs Go 代码传参 | 全部匹配 |
| H.2 | 新增模板（如有）在 config 中注册 | 对比 prompts/ vs config/代码 | 无遗漏 |

### 补漏 J：集成测试

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| J.1 | integration_test.go 通过 | `go test -v -run Integration` | PASS |
| J.2 | 集成测试覆盖迁移后功能 | Read 测试内容 | 覆盖新增 CLI 路径 |

### 补漏 L：Git 状态

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| L.1 | 工作目录干净 | `git status` | 无未提交变更 |
| L.2 | 与 upstream/main 偏移量 | `git log upstream/main..HEAD --oneline` | 已知偏移量 |
| L.3 | 无意外未跟踪文件 | `git status` | .gitignore 覆盖 |

## L2 数据验证

### 维度 5：数据管线完整性

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| 5.1 | `sage-wiki compile --all` 全量编译 | CLI | ≥27/27 成功，0 错误 |
| 5.2 | 编译产出 vs 上次基线对比 | diff 统计 | 摘要/概念/文章数 ≥ 基线 |
| 5.3 | `sage-wiki lint` | CLI | 问题数 ≤ 4036（基线） |
| 5.4 | `sage-wiki diff` 内容漂移检查 | CLI | 无意外差异 |
| 5.5 | `.manifest.json` 与实际文件一致 | 交叉比对 | 条目数 = 实际文件数 |
| 5.6 | 编译输出目录结构完整 | ls wiki/ | summaries/ concepts/ articles/ 存在 |

### 维度 6：数据变异

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| 6.1 | 各源类型都能编译 | 检查 regulation/ inbox/ 各有产出 | 无类型级别全失败 |
| 6.2 | 中文内容处理正确 | 抽检文章标题/摘要 | 无乱码/截断 |
| 6.3 | stripThinkTags fallback 有效 | 检查是否有空文章 | 0 空内容文章 |
| 6.4 | summary_max_tokens=8000 生效 | 抽检摘要长度 | 摘要非异常短 |
| 6.5 | type_signals 分类准确 | 抽检源文件 type 字段 | 与文件名/内容匹配 |

### 维度 7：边界条件 + Config extends

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| 7.1 | config extends 继承解析 | 构造 parent+child yaml 测试 | deepMerge 正确 |
| 7.2 | config extends 不存在的 parent | 指向不存在文件 | 清晰报错 |
| 7.3 | config extends 循环引用 | A extends B extends A | 不死循环，报错 |
| 7.4 | 空 raw/ 目录编译 | 临时清空 raw/ | 不 panic，0 产出 |
| 7.5 | 单文件 raw/ 编译 | 只留 1 个文件 | 1/1 成功 |
| 7.6 | ontology 空库查询 | 清空 ontology 后查询 | 空结果不 panic |
| 7.7 | merge.go 单元测试全绿 | `go test ./internal/config/...` | PASS |

### 维度 8：状态一致性

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| 8.1 | ontology 实体数 = 编译概念数 | 对比两者输出 | 一致 |
| 8.2 | 搜索索引与编译产出同步 | search 已知文章标题 | 命中 |
| 8.3 | FTS5 索引条目数 | sqlite3 count | ≥ 文章数 |
| 8.4 | 向量索引条目数 | sqlite3 count | ≥ 文章数 |
| 8.5 | wikilink 目标 vs ontology 实体 | 交叉对比 | 断链率记录 |
| 8.6 | config.yaml 的 relations 与代码默认一致 | 对比 config 和 relations.go | 无遗漏 |

### 补漏 C：Web UI 完整性

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| C.1 | `npm install` | Bash in web/ | exit 0 |
| C.2 | `npm run build` | Bash in web/ | 构建成功 |
| C.3 | TypeScript 类型检查 | `npx tsc --noEmit` | 无类型错误 |
| C.4 | Vite 输出路径正确 | 检查 dist/ 目录 | index.html 存在 |

### 补漏 G：端到端工作流

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| G.1 | add-source → 源文件出现在 list | CLI 链式调用 | 新源可见 |
| G.2 | compile → search 能查到新内容 | compile 后 search | 命中 |
| G.3 | compile → ontology 有新实体 | compile 后 ontology query | 新实体存在 |
| G.4 | learn → 知识条目持久化 | learn 后检查 | 条目存在 |
| G.5 | capture → 内容入库 | capture 后检查 | 内容可查 |

### 补漏 I：eval.py 评估套件

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| I.1 | eval.py 语法正确 | `python3 -m py_compile eval.py` | 无语法错误 |
| I.2 | eval_test.py 通过 | `python3 -m pytest eval_test.py` | PASS |
| I.3 | eval.py --help 可用 | Bash | 显示用法 |

### 补漏 M：混合搜索管线

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| M.1 | FTS5 搜索独立可用 | sqlite3 直接查 FTS5 表 | 返回结果 |
| M.2 | 向量搜索独立可用 | 代码路径或 CLI flag 验证 | 返回结果 |
| M.3 | 混合搜索结果合并 | `sage-wiki search` 结果含两路 | 结果非空 |

## L3 压力/边界

### 维度 9：并发

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| 9.1 | hub search 多项目并行查询 | 注册 2+ 项目后搜索 | 结果合并正确 |
| 9.2 | hub search 单项目退化 | 只注册 1 个项目 | 正常返回 |
| 9.3 | 并行编译 race 检测 | `go test -race ./...` | 无 data race |
| 9.4 | 联邦搜索超时处理 | 构造不可达项目 | 不无限等待 |

### 维度 10：恢复/降级

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| 10.1 | API key 缺失时行为 | 临时 unset key + compile | 清晰报错 |
| 10.2 | DB 文件缺失时行为 | 临时移走 wiki.db + status | 清晰报错 |
| 10.3 | DB 文件损坏时行为 | 临时写垃圾到 wiki.db | 报错不 panic |
| 10.4 | 网络不可达时 compile | mock 无效 endpoint | 超时后报错 |
| 10.5 | config.yaml 缺失时行为 | 临时移走 + status | 清晰报错 |
| 10.6 | 编译中断后重编译 | compile 中 Ctrl+C 再 compile | 数据一致 |

### 维度 11：规模/时间

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| 11.1 | 全量编译耗时 | 计时 compile --all | 记录基线 |
| 11.2 | 搜索响应时间 | 计时 search | < 2s |
| 11.3 | lint 执行时间 | 计时 lint | 记录基线 |
| 11.4 | manifest 过期检测 | 修改源文件后 diff | 检测到变更 |
| 11.5 | 大文件处理 | 检查最大源文件的编译 | 不超时 |

### 维度 12：权限

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| 12.1 | wiki.db 文件权限 | ls -la | 可读写 |
| 12.2 | raw/ 目录读权限 | ls -la | 可读 |
| 12.3 | wiki/ 输出目录写权限 | ls -la | 可写 |
| 12.4 | .sage/ 目录权限 | ls -la | 可读写 |
| 12.5 | config.yaml 读权限 | ls -la | 可读 |

### 补漏 D：Docker 构建

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| D.1 | `docker build .` | Bash | 构建成功 |
| D.2 | Dockerfile 中 Vite 路径一致 | Read 对比 | 路径匹配 |

### 补漏 K：TUI 子系统

| # | 检查项 | 方法 | 预期结果 |
|---|--------|------|---------|
| K.1 | TUI 包编译通过 | `go build ./internal/tui/...` | exit 0 |
| K.2 | TUI 单元测试通过 | `go test ./internal/tui/...` | PASS |
| K.3 | isatty 检测正确 | 非终端环境下不启用 TUI | 正确降级 |

## 报告格式

```markdown
# sage-wiki 健康检查报告
日期: YYYY-MM-DD
分支: feature/chinese-localization
检查版本: <commit hash>

## 总览
- 总检查项: N
- PASS: N (XX%)
- WARN: N (XX%)
- FAIL: N (XX%)
- SKIP: N (XX%)（因前序失败跳过）

## L1 机械验证
### 1. 集成完整性 [X/15 PASS]
| # | 检查项 | 状态 | 详情 |
...

（L2、L3 同格式）

## 发现汇总（按严重度排序）
| # | 严重度 | 维度 | 问题描述 | 修复建议 |
...

## 基线数据（首次记录）
- 编译: X/Y 成功
- 编译耗时: Xs
- Lint 问题数: N
- 本体实体数: N
- FTS5 条目数: N
- 向量条目数: N

## 下一步建议
1. 按严重度排序的修复任务列表
2. 是否需要深挖的维度
```

## 检查项统计

| 层级 | 维度数 | 检查项数 |
|------|--------|---------|
| L1 | 4 原始 + 6 补漏 | ~55 |
| L2 | 4 原始 + 4 补漏 | ~45 |
| L3 | 4 原始 + 2 补漏 | ~30 |
| **总计** | **24 检查域** | **~130** |

## 前序依赖

- L1 全部 PASS 才进入 L2（CLI 命令不通则数据验证无意义）
- L2 的编译检查（5.1）需要有效 API key（依赖 3.3/3.4 PASS）
- L3 的恢复测试需要能恢复现场（先备份再破坏）
- Docker 构建（D）依赖 Web UI 构建（C）通过
