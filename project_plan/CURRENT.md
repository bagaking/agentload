<!--
meta:
  目的: 项目执行状态的唯一驱动文档——核心标准与原则、当前状态、文档目录、质检记录。关键节点（计划调整/重大变更/验收/sprint 完成/与用户深谈后）必须更新本文件。
  日期: 2026-09-12
  来源: 全局「组织目录的最佳实践」；PLAN.md（北极星与 Roadmap）；judge 横切验收主题；仓库现行门禁（AGENTS.md、docs/agent-load-metric-semantics.md、docs/agent-load-ui-design-system.md、scripts/validate_locales.js）。
-->

# CURRENT — 标准、状态与质检

## 1. 核心标准与原则

### 1.1 组织目录最佳实践（全局约定，此处为项目内援引）

- **目录平铺**：所有文件直接放在 `project_plan/` 根目录，前缀区分，无子目录。
- **SSOT**：写之前先搜索已有信息，用引用代替抄写。方向裁决看 PLAN.md，sprint 细节看各 sprint 文件，决策沿革看 OPINIONS_001，调研内容看 DOCREF——互相引用、不互相复制。
- **Mini-Sprint 机制**：测试不达预期、回归不过、重构需求、计划偏差时，快建 `MXX_SXX.NNN.FIX|REFACTOR.xxx.md`，不打乱主计划；同时一般至多一个「活跃中」的 mini-sprint；sprint 偏差 >30% 须重调后续计划。
- **必更 CURRENT 规则**：任何 sprint/mini-sprint 的创建、完成、验收，以及计划调整、重大变更、与用户深入讨论后，都必须同步更新本文件（执行状态 + 质检记录）。
- **单文件上限**：sprint（含 mini-sprint）内容超过 256 行须重新规划拆分。

### 1.2 项目质量门（Quality Gates）

**常设门（每次合入，无例外）**：

1. `go vet ./...` 与 `go test ./...` 绿。
2. `npm --prefix ui run build` 绿，且内嵌 `ui/dist` 与 `ui/src` 构建产物一致（不带过期 chunk 出货）。
3. `node scripts/validate_locales.js` 绿——en/zh/ja 全表面对齐，覆盖 UI、通知和 onboarding；native tray 文案由原生壳层单独维护，尚未纳入此脚本。
4. `./build_macos_app.sh` 绿。
5. **中立观测合规审查**：任何涉及指标语义的变更须对照 docs/neutral-observation-principles.md 与 docs/agent-load-metric-semantics.md 复审；指标计算只准出现在共享语义层。
6. **设计系统 token 合规**：任何 UI 变更遵守 docs/agent-load-ui-design-system.md 的 token 体系。

**横切验收主题（judge 裁定，逐条落到 sprint 验收）**：

- **四大构建门逢合入必跑**——含只改文档但触及生成表面的 sprint。
- **中立观测审计**——指标计算全部路由语义层（本仓库无 CI，靠 `go test ./...` 中的语义层测试 + 合入前人工复审强制）；无文档化、用户可见阈值不得有判断性文案；unknown/unavailable/not_configured 渲染为设计过的状态，绝不用零/估值/沉默。
- **三语对齐**——每个新增用户可见字符串同发 en/zh/ja，由 validate_locales 强制。
- **能耗发布门**——空闲 CPU <1%；永不进「重要耗能 App」列表；发布前手测对照 M01 基线（perf budget 尚无自动化，见 M02_S02）；30s 节拍下限与 refresh_slot_id 合并由回归测试守住每个新 poller/事件通道。
- **隐私不变式**——仅 loopback 监听（网络审计）、零外发遥测、导出消毒并附省略清单、consent 表面展示确切变更（如 settings.json diff）且一键回退。
- **矩阵门禁**——vendor 与信号只按其证据诚实支持的档位出货；解析异常自动降档而非输出错数；矩阵单元格由 adapter 代码生成，永不手编。
- **Sprint 纪律**——量化验收未过不得进入下一 sprint；回归或 >30% 偏差开 FIX/REFACTOR mini-sprint 并更新本文件，不许静默漂移。

## 2. 执行状态（更新于 2026-09-12）

**今日已完成**：

- 全量审计完成（响应用户审计指令，原话见 PLAN.md §1.2）：系统设计、信息流、页面、描述文档四维。
- **Hardening 已落地并提交**：`1c66208` fix(core) 后端 11 项加固、`817b27c` fix(ui) 前端修复+性能+i18n、`70fc29f` docs 架构/API 文档、`dfe5a4b` fix(core) 审查修复。三路对抗审查完成：1 条高危已修，18 条 advisory 登记于 M01_S01「审查遗留项」。新版本 2026.07.26.214004 已装入 /Applications 并运行验证。
- 品类战略调研完成（产出 DOCREF_001..004）。
- 三案合议（judge）+ 批评复核（critic）完成，主计划落档（PLAN.md）；M01–M06 全部 23 个 sprint 文件落档。

**2026-09-14 完成**（mini-sprint `M01_S01.001.FIX`）：

- **指标语义变更**：`LiveTokenRateSample` 新增 `coverage` / `tracked_file_count` / `eligible_file_count`。采样超出文件上限时不再整体判 unavailable，而是出「带覆盖度标注的下限值」。删除 `file_capacity` 不可用理由及其三语文案。
- 修复吞吐 lane 冷启动时的空白面板（compact 空状态误按「任意 lane 有数据」判断）。
- 同一错误模式的第二、第三次发作一并修复：`watch_incomplete` 在已测到正值时不再清空指标（零值时仍诚实失败关闭，边界由测试锁定）；前端 lane 不再因缺趋势窗口而连带藏掉已可用的实时读数。
- 未归属反解：从 transcript 路径恢复项目名，但必须由 `resolveRepoBoundary` 证明路径确在仓库内，否则诚实记为 unassigned。
- **对抗式复审（opus 子代理）已完成**，7 条发现：2 条真 bug 已修（下限值落盘丢失 partial 标记；codex 日期路径被反解成捏造项目「14」），1 条部分采纳（本轮新引入的 stat 风暴已消，既有重复行走留作性能项），1 条经测试证伪，3 条判定不修。并发面无发现。详见 mini-sprint §4，新决策记入 OPINIONS D-008。
- 项目排序改为吞吐优先，无实测速率时退回活跃度。
- 面性设计铺开至 diagnostics / online-processes / system-resource-inspector / activity-process-trend / system-process；周期选择组件化为 `TrendRangeRail`（居中悬浮 + 滑动高亮）。
- 修正本文件 §1.2 两处失真表述：本仓库无 CI，相关门禁实为 `go test` + 人工复审。

**2026-09-15 完成**（sprint `M02_S05`，新开）：

- **用户要求接 gemini / cursor / grok**（原话见 PLAN.md §1）。落地前做了三方实机取证，结论是三家证据水平不在一个量级，**按同一档位发布会直接违反「绝不虚构」**，因此范围收敛为「grok 满证据 + cursor/gemini 维持诚实空壳」，已与用户确认。
- **grok 接成满证据 adapter**：Process / Discovery / Transcript / Usage 四槽全填。实机验证 48/48 transcript 解析成功、0 错误、19 个项目正确归属。
- **两个语义陷阱由测试锁死**：(1) grok 的 usage 是**逐轮增量而非累计**（实测 output 序列非单调），误判会重演 ccusage #950 的 91x 虚报；蓄意改成 cumulative 的夹具已验证会让测试失败。(2) 每个计数在 `usage.modelUsage.<model>` 下重复出现，不得二次累加——实测单会话 3 轮合计 28393，解析结果精确等于 28393。
- **cursor / gemini 的不支持是测出来的，不是没看**：cursor CLI transcript 每行只有 `role`/`message` 两键，33 个文件含时间戳或 token 的数量为 **0**；IDE 侧 13990 条消息的 `tokenCount` 全为 0、92 个会话 `usageData` 全空。gemini 实际在用 Antigravity（`agy`），90 个会话是 **protobuf-in-SQLite**，`steps` 表无任何 token 字段。二者 capability 槽保持 nil，并由 `TestVendorsWithoutEvidenceDeclareNoTranscriptOrUsageCapability` 守住。
- **顺带验证一条安全边界**：grok 的 `-- <prompt>` 形态会把整段用户输入（实测含真实 OAuth token / API key）放进 argv。现有 `sanitizeCommandForClient` 已挡住，未泄漏；已补回归测试锁死。
- **M02_S04 需改写**：其前提「gemini-cli 是 JSONL」被实测推翻，验收标准须按 protobuf-in-SQLite 现状重写，conformance kit 的第二样本改用 grok。详见 M02_S05 §7。

**当前活跃**：

- **M01 地基**（本轮架构与熵审查修复已落地，发布门已复跑）。
- **当前 sprint：`M01_S01.hardening_release_gate.md`**——落地在途 hardening、把脏工作树收敛为干净提交、四门验证、录基线指标、打 tag。
- M01 后续排队：M01_S02（Go 拆包 + VendorAdapter）、M01_S03（main.tsx 拆分 + popover 快路径）。

**近期待办（非 sprint 内）**：

- PLAN.md §8 未决问题中标〔用户决策〕的项（Q2/Q6/Q9/Q13）向用户提出。
- 建立**吸收巡检仪式**：每个 milestone 出口，审计 Claude Code / Codex / gemini 第一方新出了什么，重新校验受影响 sprint（源于 critic，见 OPINIONS_001 D-006）。

## 3. 文档目录

| 文件 | 用途 |
|---|---|
| `PLAN.md` | 主计划：北极星、用户原话、5W1H、SWOT、品类格局、硬约束、Roadmap、否决项、未决问题 |
| `CURRENT.md` | 本文件：标准与原则、执行状态、文档目录、质检记录 |
| `OPINIONS_001.strategy_decisions.md` | 战略决策记录（来源与语境版本齐备） |
| `DOCREF_001.competitor_landscape_ai_agent_monitors.md` | 直接竞品格局调研 |
| `DOCREF_002.agent_observability_reference.md` | 相邻 agent 可观测性参照（OTel GenAI、hooks/OTLP） |
| `DOCREF_003.macos_menubar_ux_bar.md` | 菜单栏产品体验及格线（delight table-stakes 清单） |
| `DOCREF_004.user_needs_and_pain_points.md` | 用户需求与痛点排序（jobs-to-be-done） |
| `M01_S01.hardening_release_gate.md` | 加固发布门与打 tag 基线 |
| `M01_S01.001.FIX.throughput_attribution_and_surface_unification.md` | 吞吐归属诚实性（partial coverage 语义）与 popover 面性统一 |
| `M01_S02.go_package_split_vendor_adapter.md` | Go 拆包与 VendorAdapter 抽象 |
| `M01_S03.main_tsx_split_popover_fast_path.md` | main.tsx 模块拆分与 popover 快路径 |
| `M02_S01.evidence_coverage_matrix.md` | Evidence Coverage Matrix（API/UI/docs 同源生成） |
| `M02_S02.energy_budget_and_module_costs.md` | 自身成本预算：模块开关、成本公示、CI perf gate |
| `M02_S03.opencode_adapter_full_evidence.md` | vendor wave 1a：opencode 全证据 adapter |
| `M02_S04.gemini_adapter_conformance_kit.md` | vendor wave 1b：gemini-cli adapter 与 conformance kit（**前提已被实测推翻，待按 M02_S05 §7 改写**） |
| `M02_S05.grok_adapter_full_evidence.md` | vendor 实机落地：grok 满证据；cursor/gemini 诚实分级 |
| `M03_S01.attention_state_engine.md` | 证据化会话 attention states 引擎 |
| `M03_S02.needs_you_triage_and_tray.md` | needs-you 分诊面、菜单栏 glyph、tray i18n |
| `M03_S03.one_keystroke_actions.md` | 一次按键动作：跳转/检视/续跑/显式停止 |
| `M03_S04.rule_based_notifications.md` | 规则化本地通知与严格礼仪 |
| `M04_S01.cost_attribution_rollups.md` | 成本归集与归因（锁版本价格表） |
| `M04_S02.quota_window_observation.md` | 配额窗口观测与诚实 unknown |
| `M04_S03.runaway_stall_watchdog.md` | 失控/卡死看门狗（文档化阈值） |
| `M04_S04.machine_attribution_worktree_collision.md` | 机器影响归因与 worktree 冲突信号 |
| `M05_S01.welcome_flow_launch_at_login.md` | 60 秒欢迎流程与 opt-in 登录启动 |
| `M05_S02.preferences_live_preview.md` | 偏好设置：live preview、导入导出、a11y |
| `M05_S03.instant_popover_prewarm.md` | 即时 popover：预热、零闪、焦点礼仪 |
| `M05_S04.consent_hooks_otlp.md` | consent-gated hooks 安装器与 loopback OTLP |
| `M06_S01.notarized_sparkle_distribution.md` | 分发：notarize、Sparkle、Homebrew cask |
| `M06_S02.automation_surface.md` | 自动化面：CLI、本地事件 API、exec-on-event |
| `M06_S03.review_digest_export.md` | 每日 review digest 与用户侧导出 |
| `M06_S04.app_store_sandbox_spike.md` | App Store sandbox spike：两档能力决策 |

## 4. 质检记录

> 各阶段完成后的质检要求在此累积，并标注来源 sprint / mini-sprint 引用，防止劣化。

| 日期 | 范围 | 结果 | 备注 |
|---|---|---|---|
| 2026-07-26 | hardening 前基线：`go vet` / `go test ./...` / `npm --prefix ui run build` + `./build_macos_app.sh` / `node scripts/validate_locales.js` | 全绿 | hardening workflow 启动前的参照点 |
| 2026-07-26 | hardening 落地后（`dfe5a4b`）：四道门复跑 + 三路对抗审查（Go 并发/语义/前端） | 全绿；审查 19 条：1 高危已修，18 advisory | 旧基线，不能替代当前验证 |
| 2026-09-12 | 熵审查修复：统一 throughput 采样时钟、区分局部解析错误与全局覆盖缺口、收敛 roots 投影、精简 popover 重复信息 | `go test ./...`、`go test -race ./...`、`npm --prefix ui run build`、`node scripts/validate_locales.js`、`go vet ./...`、`./build_macos_app.sh` 全部通过；Darwin FSEvents 集成测试在当前环境不可用时跳过 | UI dist 已按当前源码重建；native tray 文案仍是独立壳层，未纳入 UI locale validator |
| 2026-09-13 | 吞吐主视觉极致重塑（响应 Image #11/12 反馈）：1. 彻底移除所有嵌套深色卡片背景与 1px 线框（`.trend-lane` 与 `.trend-chart` 设置无框透明底），内容纯净呼吸在画布上；2. 滚动窗口与跨度严格归属于图表（紧贴图表上方并列呈现：左[跨度 1D..30D] 右[窗口 1m..15m]），逻辑极度自洽清晰；3. 核心实时流速（`93.3 token/秒`）大号亮蓝字体右对齐展现，左侧承载标题与 `MAX/P95/AVG`，消除高突兀高度；4. 下方项目排行榜基于全周期 Top 项目生成（行数与高度恒定固定，绝不上下跳变闪烁），鼠标悬停仅高亮该时刻的单点数值与平滑滑块 | `go test ./...` 全绿；`node scripts/validate_locales.js` 绿；`npm --prefix ui run build` 绿；`./scripts/package_macos_app.sh` 绿；已安装至 `/Applications/Agent Load.app` 并实机验证运行 | 告别黑框套叠、元素撞车与高度抖动，现代极简专业感十足，视觉张力极强 |

| 2026-09-14 | mini-sprint `M01_S01.001.FIX`：吞吐归属诚实性 + popover 面性统一 | `go vet ./...`、`go test ./...`、`npm --prefix ui run build`、`node scripts/validate_locales.js`、`./build_macos_app.sh` 全绿；新增 3 个测试；已装入 `/Applications` 并用 Playwright 实机截图验证 | 实测 tps 1116 token/秒（此前因 120>96 文件上限而整体不可用）；partial coverage 为新增指标语义，须同步 docs/agent-load-metric-semantics.md |

| 2026-09-14 | mini-sprint `M01_S01.001.FIX` 对抗式复审（opus 子代理，7 条发现） | 2 条真 bug 已修并补测试（persisted floor 丢标记、codex 日期路径捏造项目「14」）；1 条部分采纳；1 条经测试证伪；3 条判定不修；并发面无发现。`go vet`、`go test ./...`、`tsc --noEmit`、`./build_macos_app.sh`、`validate_locales.js` 全绿 | 已装机实测：`58.11 token/秒` + `coverage: partial` + `coverage_reason: watch_incomplete`，下限语义端到端成立。教训见 OPINIONS D-008：断言「应被拒绝」的测试，输入必须取自真实布局 |

| 2026-09-14 | 性能优化（profile 驱动） | `resolveRepoBoundary` memo：隔离基准 19096ns → 154ns（124x），pprof 中 `os.Stat` 占比 53.84% → 0.19%，由 TTL 过期测试锁定。lsof 缓存经复审判定对真实 5 分钟节奏无收益，已回退。「256KB 尾读」建议经计数实测证伪（0 次）。`go vet`、`go test ./...`、`tsc --noEmit`、`validate_locales.js`、`./build_macos_app.sh` 全绿，已装机验证（46 sessions、worktree 归属正常、快照 0.8ms） | 新增 `snapshot_benchmark_test.go`（需 `AGENTLOAD_BENCH_REAL=1`）。过程中 `git checkout transcripts.go` 误删 4 处未提交修改，由测试全数抓出并还原——教训见 OPINIONS D-009 |

| 2026-09-14 | 未归属成分核查 + 脱敏器文案 bug 修复 | 吞吐侧 0 未归属；快照侧未归属全部为「transcript 未落盘、无 cwd 证据」的诚实未知。查出并修复 `sanitizeEmbeddedAbsolutePaths` 把 `cwd/project` 误判为路径、吃掉分隔符与后续词（`and/or` → `andlocal-path`），真实路径脱敏不受影响。`go vet`、`go test ./...`、`./build_macos_app.sh` 全绿，已装机验证理由文案正确渲染 | 新增 `TestSanitizeTextForClientKeepsProseSlashesWhileRedactingPaths` 双向锁定（prose 保留 + 路径仍脱敏）。详见 mini-sprint §7 |

| 2026-09-14 | 两条实机分歧的根因修复（用户截图与质疑触发） | **(1) 归属路径分叉**：`minuteFactLocked` 传原始 session 映射、`publishLocked` 传恢复后的映射，同一批 token 写出两种桶（实时 0% vs 历史 78% 未归属）。已统一，`TestLiveTokenRateMinuteFactsAttributeLikeTheLiveSample` 锁定。**(2) 子代理身份合并**：Claude sidechain 行携带父会话 `sessionId`，被无条件采纳后十份子代理 transcript 塌成一行。已改为记为 `ParentThreadID`，新增 `jsonTrueField` 与两条对称回归测试。`go build`、`go test ./...`、`./build_macos_app.sh` 全绿，已装机实测 | 装机后实测：分钟事实 05:00Z 起未归属 **0.0%** 且带 `coverage: partial`（04:58Z 为 35.2% 且无标记）；claude 会话 21 → **37**，agentmux 1 → **15** 行，新增 subagent 角色 17 个。教训见 OPINIONS D-010（双写入路径必然分叉）与 D-011（父 ID 不是子身份；计数偏少要分发现/解析/聚合三段量）。**遗留数据债**：05:00Z 之前的 20.8MB 历史带错误归属且因单向 hash 无法回算，待用户决策丢弃或标注断点 |

| 2026-09-14 | 两条实机分歧的根因修复（用户截图与质疑触发） | **(1) 归属路径分叉**：`minuteFactLocked` 传原始 session 映射、`publishLocked` 传恢复后的映射，同一批 token 写出两种桶（实时 0% vs 历史 78% 未归属）。已统一，`TestLiveTokenRateMinuteFactsAttributeLikeTheLiveSample` 锁定。**(2) 子代理身份合并**：Claude sidechain 行携带父会话 `sessionId`，被无条件采纳后十份子代理 transcript 塌成一行。已改为记为 `ParentThreadID`，新增 `jsonTrueField` 与两条对称回归测试。`go build`、`go test ./...`、`./build_macos_app.sh` 全绿，已装机实测 | 装机后实测：分钟事实 05:00Z 起未归属 **0.0%** 且带 `coverage: partial`（04:58Z 为 35.2% 且无标记）；claude 会话 21 → **37**，agentmux 1 → **15** 行，新增 subagent 角色 17 个。教训见 OPINIONS D-010（双写入路径必然分叉）与 D-011（父 ID 不是子身份；计数偏少要分发现/解析/聚合三段量）。**遗留数据债**：05:00Z 之前的 20.8MB 历史带错误归属且因单向 hash 无法回算，待用户决策丢弃或标注断点 |

**质检步骤库（随 sprint 验收累积）**：

- 目前基础步骤 = §1.2 常设门六项。M01_S01 完成后追加：基线性能指标复核（空闲 CPU %、popover 打开 ms、snapshot p95 ms、二进制 MB，测量方法须记录在案）。后续每个 sprint 验收通过时，把其量化验收中可复用的检查项追加到本节并注明来源文件。
