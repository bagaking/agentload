<!--
meta:
  目的: agentload 项目主计划——北极星目标、用户原话存档与解读、5W1H、SWOT、品类格局、技术硬约束、Roadmap 总览、已否决方案、未决问题。项目方向的唯一裁决文档（SSOT）。
  日期: 2026-07-26
  来源: 用户 2026-07-26 原话；三份战略提案的 judge 合议结论；critic 复核意见；品类调研（DOCREF_001..004）；仓库现状审计（AGENTS.md、docs/）。
-->

# agentload 项目主计划（PLAN）

## 0. 北极星目标

agentload 要成为本地 AI coding agent 监控品类里**最全面、最舒服、作用最大**的监控器。三个最高级由同一条不变式（invariant）变得可度量、且互相加固：

> **绝不虚构（never fabricate）**：缺失就是缺失（missing stays missing）、只监听 loopback、每一个呈现的数据要么有证据支撑、要么是被明确标注的空白。

- **最全面（widest truthful field of view）**：通过 VendorAdapter 观测 5+ 厂商 × 六类信号族（presence/resources、sessions/roles、movement、tokens/cost、quota windows、workspace context），外加用户同意后的 ground-truth 层；由代码生成的 **Evidence Coverage Matrix** 作为公开主干。成功判据：没有竞品能在同等或更高诚实度下覆盖更多 vendor×signal 单元格；零起「你的数字是错的」类公开 issue（该类 issue 已给所有做外推的竞品打上烙印）。
- **最舒服（quietly becomes infrastructure）**：popover 预热首绘 ≤150ms、零闪烁、不抢焦点；空闲 CPU <1%、永不出现在 macOS「重要耗能 App」列表；安装到可用 <60 秒；一切默认 opt-in；en/zh/ja 在包括 tray 在内的所有表面全量对齐；notarized + 自动更新。
- **作用最大（changes what the user does next）**：会话 block/ask/finish 后一个事件 tick（<5s）内浮出、一次按键可达；失控/卡死在钱包或配额窗口被烧掉之前给出有文档化阈值的提示；配额位置只由观测到的 burn + 用户自报限额渲染；本地 CLI/SSE 自动化面让其他工具能消费 agentload 的证据。
- **复合成功测试**：卸载 agentload 的用户会觉得「盯 agent 变慢了」；且没有任何用户抓到它声称过一件它没有观测到的事。

### 五大支柱（Pillars）

1. **诚实的广度**——每个 vendor×signal 单元格要么有证据、要么是明确标注的空白；代码生成的 Coverage Matrix 既是产品主干，也是一切广度工作的发布门（最全面但永不出错）。
2. **注意力优先于指标**——产品的首要工作是回答「现在哪个 agent 需要我」：基于证据的 attention states、needs-you 分诊、规则化通知、从信号到终端的一次按键直达（作用最大）。
3. **安静的原生舒适**——即时、可一瞥、能耗诚实、三语、consent-first；守望者近乎零成本，只通过用户自建的规则发声（最舒服）。
4. **诚实的预见与保护**——成本、配额窗口、失控提示只从观测证据、本地锁定的价格表、用户自报限额计算；每个阈值有文档且可见；信任护城河使预见变得可执行。
5. **复利平台**——VendorAdapter + 共享语义层 + conformance kit + 本地自动化 API，让每次新增比上一次更便宜；这是单人 + AI agents 唯一可持续的策略。

## 1. 用户原话（逐字记录）与解读

> 本章逐字保存用户指令，任何后续决策与此处冲突时以本章为准、并须回来更新本章。

### 1.1 北极星指令（2026-07-26）

> **"嗯 让这个项目成为相同品类里最全面 用起来做舒服 作用最大的一个吧"**

解读：

- 三个并列最高级：**最全面**（品类内覆盖广度第一）、**用起来做舒服**（体验第一）、**作用最大**（用户价值第一）。「做舒服」按上下文判定为「**最舒服**」的笔误——与前后「最全面/作用最大」的并列句式一致，取「用起来最舒服」义。
- 「相同品类」= 本地优先的 AI coding-agent 活动监控器（macOS 菜单栏 + web dashboard），即 DOCREF_001 所界定的竞争集合。
- 对决策的驱动：三个最高级直接映射为 §0 的三条可度量定义与五大支柱；Roadmap（§6）中每个 milestone 都必须能回答「推进了哪个最高级」。同时，用户没有说「最快上线」或「功能最多」——因此 judge 裁定的「诚实广度而非 checkbox 广度」「先不变式后功能」与该原话不矛盾。

### 1.2 审计指令（2026-07-26，同日）

> **"理解项目 看看有什么要优化的 包括系统设计 信息流 页面 和描述文档"**

解读：

- 四个审计维度：**系统设计**（架构/并发/死代码）、**信息流**（观测→语义层→UI 的数据通路）、**页面**（UI 正确性/性能/i18n）、**描述文档**（docs 卫生与准确）。
- 该指令触发了当日的全量审计与在途 hardening workflow（范围见 CURRENT.md「执行状态」），并直接导出 M01 的「先加固、后功能」（hardening-before-features）决策：在当前平铺 root Go package 与约 3,000 行 main.tsx 之上叠功能会放大所有后续估算（judge 合议确认）。
- 「理解项目」也导出了本 project_plan 的建立：先锁定方向（本文件），再执行。

## 2. 5W1H：为什么做这个项目、这个目标

- **Why**：AI coding agent 已成日常并发工作负载（调研见 DOCREF_004：20+ 并发 agent 的真实诉求），但监督手段落后——用户的头号焦虑是配额、灾难场景是失控烧钱、日常痛点是「哪个 agent 在等我」。品类内竞品或外推造假（被打上 misleading 烙印）、或单一 vendor、或被第一方吸收（opcode 之死）。「诚实 + 广度 + 注意力路由」的位置空着。
- **What**：一个绝不虚构的本地监控器：观测多厂商 agent 的证据，将其路由为用户的注意力决策（§0）。明确的反目标：不编排、不启动、不 review diff、不发网络请求。
- **Who**：为同时运行多个 coding agent 的开发者；由单人维护者 + AI agents 建造（因此复利平台是生存策略而非偏好，见支柱 5）。
- **When**：2026 年品类重心正从「用量统计」移向「注意力路由」（DOCREF_001/002）；第一方功能吸收半衰期以月计（Claude Code /usage 已吸收单厂商配额显示）——晚入场即无位置，抢跑靠诚实差异化而非速度。
- **Where**：macOS 菜单栏原生壳 + 本地 web dashboard；单二进制内嵌 UI；只在用户机器上、只读 loopback。darwin-native 能力（NSPopover、thermal、per-PID I/O）是差异化所在。
- **How**：M01 加固与结构重构 → M02 两大不变式（Coverage Matrix、能耗预算）+ vendor wave 1 → M03 注意力核心 → M04 诚实预见 → M05 舒适与 consent → M06 分发与自动化面（§6）。每步都受四大构建门与中立观测审计约束（CURRENT.md §1.2）。

## 3. SWOT（agentload vs 品类）

| | 内容 |
|---|---|
| **S 优势** | 中立观测语义已成体系（docs/neutral-observation-principles.md，「missing stays missing」已是代码事实）；多 vendor 证据映射（claude/codex/trae）而非单厂商；原生 NSPopover 壳 + 语义层架构 + en/zh/ja 三语已就位；local-only 隐私立场可验证（loopback、零外发）。 |
| **W 劣势** | 单人维护 ~24k LOC；平铺 root Go package 与 ~3,000 行 main.tsx 阻碍模块化（M01 解决）；无 LICENSE、无分发渠道、无自动更新——在 M06 前无法获得任何真实需求信号；无 attention states——品类 2026 重心尚未覆盖。 |
| **O 机会** | 竞品外推造假的信任危机（ccusage #298/#483、CCUM #48/#52）反衬「诚实」定位；attention 路由无人做好；conformance kit + adapter 让广度成本递减；本地自动化面（CLI/SSE）可廉价吸收远程查看需求而不破 local-only。 |
| **T 威胁** | 第一方吸收（/usage、VS Code agents 窗格）；vendor 格式漂移（gemini JSON→JSONL 迁移、codex schema 漂移）；App Sandbox 可能砍掉大部分进程证据（MAS 前景存疑）；广度陷阱（ccusage 赶工 codex 支持产出 91x token 虚报，#950/#1434；ClaudeBar ~48 open issues 维护塌方）。 |

结论：以 S（诚实语义）攻 O（信任危机），用 M01/M02 补 W（结构、不变式、预发布渠道议题见 §8），用漂移金丝雀、吸收巡检、demand-driven adapter 策略对冲 T。

## 4. 品类格局摘要

调研全文不在此内联，见四份 DOCREF（各文件含来源与时效评估）：

- `DOCREF_001.competitor_landscape_ai_agent_monitors.md` —— 直接竞品逐个画像（ccusage、CCUM、CodexBar、Agent Sessions、ClaudeBar、SessionWatcher、opcode 等）、死因分析（外推失信/第一方吸收/维护塌方）、空位判定。
- `DOCREF_002.agent_observability_reference.md` —— 相邻领域参照：OTel GenAI semconv（含「不得上报无法获得之物」规则）、hooks/OTLP 证据通道、agent 可观测性实践。
- `DOCREF_003.macos_menubar_ux_bar.md` —— 菜单栏产品的体验及格线（delight table-stakes 清单）：popover 礼仪、能耗纪律、consent UX（Bartender 失信案例）、通知礼仪。
- `DOCREF_004.user_needs_and_pain_points.md` —— 用户需求与痛点排序（jobs-to-be-done）：配额焦虑第一、失控烧钱最灾难、needs-you 分诊为日常高频。

## 5. 技术基座与硬约束

以下为**不可协商约束**，任何 sprint 不得违反（详细契约见引用文档，此处不重抄）：

1. **中立观测语义**——一切指标计算只经共享语义层（`metric_registry.go` / `metric_semantics.go` / `ui/src/lib/metricSemantics.ts`）；unknown/unavailable/not_configured 渲染为设计过的状态而非零、估值或沉默；无文档化阈值不得出现判断性文案。契约：docs/agent-load-metric-semantics.md、docs/neutral-observation-principles.md。
2. **local-only 隐私**——只监听 loopback（需网络审计）、零外发遥测；导出经路径消毒并附省略清单；一切 consent 表面展示确切变更（如 settings.json diff）并可一键回退。契约：docs/privacy-local-observation.md。
3. **单二进制 + 内嵌 UI**——Go 单二进制内嵌 `ui/dist`（Vite 构建）；原生壳为 NSPopover；UI 变更须遵守设计系统 token（docs/agent-load-ui-design-system.md）。
4. **App Store 意向**——分发策略以两档为前提设计（direct = 全矩阵，MAS = 标注子集）；公开表述受 docs/app-store-positioning.md 避讳清单约束；sandbox 可行性为未决问题（§8-Q1）。
5. **能耗纪律**——空闲 CPU <1%、30s 采样节拍下限、refresh_slot_id 合并、隐藏表面零请求；作为发布门由回归测试与 CI perf budget 守护。
6. **三语对齐**——每个新增用户可见字符串同时提供 en/zh/ja，由 `scripts/validate_locales.js` 强制，无豁免表面（含 tray、通知、onboarding）。

## 6. Roadmap 总览

排序逻辑（judge 合议裁定）：三案共识项即正确项；**不变式类（Matrix、perf gate）先于其所要门禁的工作**（事后补装等于全部重审）；attention 引擎作为平台原语前移；在 M01 的结构接缝就位之前冻结一切功能合入。每个 sprint 的 OKR/checklist/验收见对应 sprint 文件（SSOT，此处只列目标）。

| 里程碑 | 目标 | Sprint 文件 |
|---|---|---|
| **M01 地基**：加固基线与结构重构 | 在途 hardening 落地为打 tag 的、有性能基线的发布门；完成 Go package 拆分、VendorAdapter 抽象、main.tsx 模块拆分；期间功能合入冻结 | `M01_S01.hardening_release_gate.md` · `M01_S02.go_package_split_vendor_adapter.md` · `M01_S03.main_tsx_split_popover_fast_path.md` |
| **M02 诚实广度平台**：矩阵、能耗预算、vendor wave 1 | 先发两大全局不变式（Evidence Coverage Matrix、自身成本/能耗预算），再以 opencode、gemini-cli 验证「≤1 周/vendor」的接入成本并抽出 conformance kit | `M02_S01.evidence_coverage_matrix.md` · `M02_S02.energy_budget_and_module_costs.md` · `M02_S03.opencode_adapter_full_evidence.md` · `M02_S04.gemini_adapter_conformance_kit.md` |
| **M03 注意力核心**：states、分诊、动作、通知 | 把观测转成注意力路由：证据化 attention states（unknown 为一等状态）、needs-you 分诊与 tray glyph（含 tray i18n）、一次按键动作（显式 stop 须确认）、规则化本地通知（零默认打扰） | `M03_S01.attention_state_engine.md` · `M03_S02.needs_you_triage_and_tray.md` · `M03_S03.one_keystroke_actions.md` · `M03_S04.rule_based_notifications.md` |
| **M04 诚实预见与保护**：成本、配额、看门狗、归因 | 用唯一可辩护的姿势服务品类头号痛点：锁版本价格表 + 明标 estimate 的成本归集、诚实 unknown 的配额窗口、文档化阈值的失控/卡死看门狗（断路器永远是人）、机器影响归因与 worktree 冲突信号 | `M04_S01.cost_attribution_rollups.md` · `M04_S02.quota_window_observation.md` · `M04_S03.runaway_stall_watchdog.md` · `M04_S04.machine_attribution_worktree_collision.md` |
| **M05 舒适、consent 与保真**：onboarding、偏好、即时 popover、hooks/OTLP | 让普通人装得上、爱得起：60 秒首跑、live-preview 偏好、预热零闪 popover；再用建成的 consent 表面把 Claude Code 信号升级为 ground truth（diff 先示、一键回退、loopback OTLP） | `M05_S01.welcome_flow_launch_at_login.md` · `M05_S02.preferences_live_preview.md` · `M05_S03.instant_popover_prewarm.md` · `M05_S04.consent_hooks_otlp.md` |
| **M06 触达与持久**：分发、自动化面、digest、sandbox 决策 | 把影响放大到自身 UI 之外：notarize + Sparkle + Homebrew cask、loopback CLI/SSE/exec-on-event 自动化面、每日 review digest 与导出、限时 2 周的 App Store sandbox go/no-go spike | `M06_S01.notarized_sparkle_distribution.md` · `M06_S02.automation_surface.md` · `M06_S03.review_digest_export.md` · `M06_S04.app_store_sandbox_spike.md` |

横切验收主题（每次合入均适用）：四大构建门、中立观测审计、三语对齐、能耗发布门、隐私不变式、矩阵门禁、sprint 纪律——完整表述见 CURRENT.md §1.2（SSOT）。

## 7. 已否决方案（附理由）

| 否决项 | 理由（judge 裁定） |
|---|---|
| 无用户自报限额的外推配额/耗尽 ETA（自信预测模式） | 直接违反中立观测教义，且是品类已验证的信任杀手（ccusage #298/#483、CCUM #48/#52）：外推监控终会与厂商台账矛盾并被打上 misleading 烙印。只保留诚实变体（M04_S02）。 |
| 众包 plan 限额库（CCUM 式） | 需要网络外发（违反 local-only）且把集体猜测当数据（违反 never-fabricate）。用户自报限额并标注 user-supplied 已覆盖合理需求。 |
| 自动失控查杀 / 自动断路器 | 未经用户发起就对进程动手打破中立观测者身份，且误杀健康长任务的代价灾难性。看门狗只发信号，stop 永远显式确认（M03_S03/M04_S03）——断路器是人。 |
| 静默安装 hooks / 默认开启遥测摄取 | Bartender 失败模式：静默改配置一夜摧毁品类领导者的信任。保留为 consent-gated M05_S04：写入前展示确切 diff、一键回退、绝不捆绑进默认 onboarding。 |
| 远程查看 / relay（Telegram、Discord、tailnet） | 任何离开 localhost 的出货网络路径都与不可协商的 local-only 立场及 App Store 定位矛盾。exec-on-event + loopback SSE（M06_S02）让用户以自己的信任决策自建 relay，agentload 本体保持 loopback 纯净。 |
| vendor wave 2 里程碑化（Cursor/Copilot/Aider/Amp/Goose 齐上） | 广度当 checkbox 是品类致命模式（ccusage 赶工 codex 支持 91x token 虚报 #950/#1434），5+ 并行 adapter 会重演 ClaudeBar ~48 open issues 的单人维护陷阱。conformance kit 证明 ≤1 周接入后，按需求驱动逐个落。 |
| 12 个月分层留存与跨机历史合并 | 过早的深度：跨机合并语义有 provenance 猜测风险，分层留存在单机报表需求被验证前徒增压缩复杂度。先出用户侧导出（M06_S03），此后再议。 |
| MCP 子进程归因与深度 workspace 观测 | 依赖 lsof 重证据，对 sandbox 敌对（docs/apple-distribution-readiness.md）且价值/脆弱比一般。高价值切片（worktree 冲突信号）在 M04_S04 出货，其余等 M06_S04 sandbox spike 定论。 |
| 会话编排、启动、diff review、MCP 管理 | opcode 失败模式：拥有工作流招来第一方绞杀；diff review 属于编排器。agentload 只观测并路由注意力——永不启动、管理、合并。记为常设反目标，供 review 时拦截 scope creep。 |
| 本 Roadmap 周期内的 Windows/Linux 移植 | 差异化在 darwin-native（NSPopover、thermal、per-PID I/O、菜单栏壳）；移植会产出降级品并饿死 macOS 上的品类领先。跨平台 stub 保持诚实 unavailable，M06 后重议（critic 异议见 §8-Q13）。 |

## 8. 未决问题（Open Questions）

来源：critic 复核（blindSpots/challenges）。标记：〔用户决策〕需用户拍板；〔spike/调研〕需限时验证或案头调研；〔计划修订〕需在对应 sprint 落地前改写其 scope/验收。

| # | 问题 | 处理 |
|---|---|---|
| Q1 | **App Sandbox 可行性从未案头调研**：公开文档已表明 sandbox 封锁 BSD 进程 API、runningApplications 只见 GUI app——MAS 构建可能只剩 transcript 阅读器。拖到 M06_S04 会让 M01–M05 被一个大概率 no-go 的野心悄悄牵引。 | 〔spike/调研〕案头调研前移至 M02，M03/M04 模块边界按 MAS 存活证据集设计。 |
| Q2 | **厂商官方本地查询面未作为证据族调研**：codex app-server RPC（CodexBar 主策略）、Claude OAuth usage 端点（ground-truth 配额，但属单次显式同意的外发调用）、gemini-cli 本地 OTel 文件。Roadmap 把 local-only 与「只解析文件」画了等号。 | 〔spike/调研〕+〔用户决策〕OAuth 端点涉及外发，须用户裁决是否破例；RPC/OTel 文件属本地，纳入调研。 |
| Q3 | **第一方吸收未评估**：Claude Code 内建 /usage 已显示 5h/周窗口与重置计时，掏空 M04_S02 单厂商价值。 | 〔计划修订〕M04_S02 重构为跨厂商统一配额视图；吸收巡检仪式已列入 CURRENT.md 待办。 |
| Q4 | **agent 实际运行位置未测量**：devcontainer/Docker/SSH/云会话对进程观测不可见——「最全面」对一个可能过半的非本地进程人群未量化。 | 〔spike/调研〕矩阵将容器/远程作为一等标注空白；bind-mount transcript 根检测与 SSH 端口转发模式入调研。 |
| Q5 | **本地 HTTP 攻击面未做威胁建模**：无 Host 白名单、无 auth token，Origin 校验可被 DNS rebinding 绕过——只读 API 尚可容忍，一旦有 stop/hooks/exec-on-event 端点则不可接受。 | 〔计划修订〕Host 白名单 + auth token（或 unix socket）为 M03_S03 与 M06_S02 的硬前置。 |
| Q6 | **无 LICENSE、商业化未决**：仓库无任何 LICENSE，阻塞 Homebrew cask 与「public spine」定位；可持续性（SessionWatcher 收费 vs ccusage MIT）未审视。 | 〔用户决策〕LICENSE 与商业化决策提至 M01。 |
| Q7 | **轮询架构规模上限未研究**：需求证据是 20+ 并发 agent；per-PID 采样 O(n)、几十个活跃 JSONL 尾随、自身历史无盘量预算（opencode 用户报 ~5GB）。 | 〔计划修订〕M02_S02 增加 50 并发会话 + 多 GB 历史的负载门，发布磁盘预算与留存/压缩策略。 |
| Q8 | **TCC 权限清单未产出**：精确聚焦终端窗口需 Accessibility/Automation 弹窗，与「onboarding 零弹窗」舒适教义冲突。 | 〔计划修订〕M03_S03 交付 TCC 权限映射 + 无权限降级路径（app 级 activate）。 |
| Q9 | **i18n 市场匹配是断言不是调研**：ja 无需求证据、ko（重度采用市场）缺席，痛点来源全为英文。 | 〔用户决策〕locale 集合反映维护者而非市场，是否调整由用户定。 |
| Q10 | **商标/图标风险未审**：M02_S03 起渲染厂商图标与名称，涉 App Store 5.2.5 与 Anthropic/OpenAI/Google 商标。 | 〔计划修订〕M02_S03 出货前完成商标/图标审查。 |
| Q11 | **事件通道架构需先行 spike**：FSEvents 目录粒度且合并事件、transcript 在正常工作时持续追加（通道会常燃而非只在转换时触发）、per-file kqueue 有 fd 上限。「<5s 转换 + <0.2% 空闲 CPU」未经验证不得锁定架构。 | 〔spike/调研〕M03_S01 前置 spike。 |
| Q12 | **vendor 证据的移动地基**：gemini-cli 格式 JSON→JSONL 迁移中且默认 30 天/50 会话自动删除；codex 文件级配额 schema 无文档且在漂移（应标 inferred 而非 ground truth）；opencode 真实陷阱是 cost:0 伴真 token 计数 + compaction/revert 语义（须专项 fixture，否则渲染出被禁止的 $0）。 | 〔计划修订〕各 adapter 增加格式漂移金丝雀（schema 指纹 + 自动降档 + 「format changed」标注）；M02_S03/S04、M04_S02 验收相应改写。 |
| Q13 | **需求驱动策略在 M06 前不可证伪**：无可安装构建、无反馈渠道、（正确地）无遥测——需求信号在 Roadmap 结束前无法到达。要么分发前移（M02 出口加 signed+notarized 预发布渠道），要么承认需求门是表演。同理，Windows/Linux 否决中 Go 核心本可跨平台编译（壳才是 darwin-native），「等需求证据」缺少能产生证据的机制。 | 〔用户决策〕预发布渠道是否前移至 M02 出口。 |
| Q14 | **150ms 预热 popover 的双重风险**：WKWebView 预热撞 OS 回归类（macOS 26.2 evaluateJavaScript ~3s）且常驻 ~150–200MB 内存压能耗支柱。原生降级方案（glance 摘要全原生、React 只留浏览器 dashboard）需要决定日期，而不只是「存在」。 | 〔计划修订〕M05_S03 为降级方案设定决策日期与判据。 |
| Q15 | **「无竞品覆盖更多单元格」是未跑的研究**：Agent Sessions、CodexBar 只被摘要、未被逐格映射——M02_S01 的营销主干目前是假设。 | 〔计划修订〕竞品逐格 teardown 并入 M02_S01 验收。 |
| Q16 | **hooks 回退「byte-identical」验收不可能成立**：Claude Code 与用户会在安装与回退之间改写 settings.json。 | 〔计划修订〕M05_S04 验收改为「只移除自己新增的键 + 底层文件被改动时呈现标注的 drift 状态」。 |
| Q17 | **价格表时效策略缺失**：锁定价格表若无 staleness 状态，就是「因遗漏而错」的成本虚构。 | 〔计划修订〕M04_S01 增加命名来源（如 LiteLLM DB）、随版本更新节奏、「表已超过 N 天」渲染标签。 |

维护规则：每个 Q 项解决后，在此表标注去向（并入哪个 sprint / 由哪条 OPINIONS 决策关闭），并同步 CURRENT.md。
