<!--
meta:
  目的: 项目执行状态的唯一驱动文档——核心标准与原则、当前状态、文档目录、质检记录。关键节点（计划调整/重大变更/验收/sprint 完成/与用户深谈后）必须更新本文件。
  日期: 2026-07-26
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
3. `node scripts/validate_locales.js` 绿——en/zh/ja 全表面对齐，含 tray、通知、onboarding，无豁免。
4. `./build_macos_app.sh` 绿。
5. **中立观测合规审查**：任何涉及指标语义的变更须对照 docs/neutral-observation-principles.md 与 docs/agent-load-metric-semantics.md 复审；指标计算只准出现在共享语义层。
6. **设计系统 token 合规**：任何 UI 变更遵守 docs/agent-load-ui-design-system.md 的 token 体系。

**横切验收主题（judge 裁定，逐条落到 sprint 验收）**：

- **四大构建门逢合入必跑**——含只改文档但触及生成表面的 sprint。
- **中立观测审计**——指标计算全部路由语义层（CI guard 强制）；无文档化、用户可见阈值不得有判断性文案；unknown/unavailable/not_configured 渲染为设计过的状态，绝不用零/估值/沉默。
- **三语对齐**——每个新增用户可见字符串同发 en/zh/ja，由 validate_locales 强制。
- **能耗发布门**——空闲 CPU <1%；永不进「重要耗能 App」列表；CI perf budget 对照 M01 基线；30s 节拍下限与 refresh_slot_id 合并由回归测试守住每个新 poller/事件通道。
- **隐私不变式**——仅 loopback 监听（网络审计）、零外发遥测、导出消毒并附省略清单、consent 表面展示确切变更（如 settings.json diff）且一键回退。
- **矩阵门禁**——vendor 与信号只按其证据诚实支持的档位出货；解析异常自动降档而非输出错数；矩阵单元格由 adapter 代码生成，永不手编。
- **Sprint 纪律**——量化验收未过不得进入下一 sprint；回归或 >30% 偏差开 FIX/REFACTOR mini-sprint 并更新本文件，不许静默漂移。

## 2. 执行状态（更新于 2026-07-26）

**今日已完成**：

- 全量审计完成（响应用户审计指令，原话见 PLAN.md §1.2）：系统设计、信息流、页面、描述文档四维。
- **Hardening 已落地并提交**：`1c66208` fix(core) 后端 11 项加固、`817b27c` fix(ui) 前端修复+性能+i18n、`70fc29f` docs 架构/API 文档、`dfe5a4b` fix(core) 审查修复。三路对抗审查完成：1 条高危已修，18 条 advisory 登记于 M01_S01「审查遗留项」。新版本 2026.07.26.214004 已装入 /Applications 并运行验证。
- 品类战略调研完成（产出 DOCREF_001..004）。
- 三案合议（judge）+ 批评复核（critic）完成，主计划落档（PLAN.md）；M01–M06 全部 23 个 sprint 文件落档。

**当前活跃**：

- **M01 地基**（功能合入冻结期）。
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
| `M01_S02.go_package_split_vendor_adapter.md` | Go 拆包与 VendorAdapter 抽象 |
| `M01_S03.main_tsx_split_popover_fast_path.md` | main.tsx 模块拆分与 popover 快路径 |
| `M02_S01.evidence_coverage_matrix.md` | Evidence Coverage Matrix（API/UI/docs 同源生成） |
| `M02_S02.energy_budget_and_module_costs.md` | 自身成本预算：模块开关、成本公示、CI perf gate |
| `M02_S03.opencode_adapter_full_evidence.md` | vendor wave 1a：opencode 全证据 adapter |
| `M02_S04.gemini_adapter_conformance_kit.md` | vendor wave 1b：gemini-cli adapter 与 conformance kit |
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
| 2026-07-26 | hardening 落地后（`dfe5a4b`）：四道门复跑 + 三路对抗审查（Go 并发/语义/前端） | 全绿；审查 19 条：1 高危已修，18 advisory | 遗留项见 M01_S01「审查遗留项」；`go test -race` 亦通过（goA/goB 验证记录） |

**质检步骤库（随 sprint 验收累积）**：

- 目前基础步骤 = §1.2 常设门六项。M01_S01 完成后追加：基线性能指标复核（空闲 CPU %、popover 打开 ms、snapshot p95 ms、二进制 MB，测量方法须记录在案）。后续每个 sprint 验收通过时，把其量化验收中可复用的检查项追加到本节并注明来源文件。
