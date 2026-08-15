<!--
meta:
  目的: 需求池 review、idea 记录与推进方法；把 roadmap 的意图和当前代码证据重新对齐。
  来源: project_plan/PLAN.md、project_plan/CURRENT.md、project_plan/OPINIONS_001.strategy_decisions.md、docs/、当前代码与测试结构。
  规则: 本文是 review 结果，不替代各 sprint 的验收 SSOT；需求状态变化先改对应 sprint，再回写 CURRENT。
-->

# 需求池 Review 与推进方法

## 1. 对项目的理解

Agent Load 是一个只在本机运行的 macOS 菜单栏观测器。它从进程、transcript、系统资源和可选的本地 telemetry 获取证据，经过 Go 聚合与共享 metric semantic layer，向 native tray、popover、dashboard、诊断页和导出面提供快照。它不启动 agent、不编排工作流、不做 diff review，也不把不可观测的事实补成数字。

产品的核心不是“再做一张用量表”，而是把三个判断交给用户：现在有哪些 agent 在工作、哪个 agent 需要我、机器和证据边界发生了什么。`missing stays missing` 是产品可信度和功能范围的共同约束。项目已经在吞吐归属、并发末点、worktree 归属、历史归档、诊断证据和 adapter 能力声明上反复用真实语料修正过设计；因此后续工作应该沿用“先量真实形状，再做最小垂直切片”的节奏。

当前代码已经具备这些地基：十个 vendor 的 registry 与能力槽、transcript 增量/缓存扫描、macOS 文件事件 watcher、历史 JSONL 与 gzip 归档、系统资源与热压观测、metric semantics、诊断 evidence gap、DNS rebinding 防护，以及 Trend/Diagnostics/System 等 UI 模块。当前仍是单一 Go 根 package 的主体，`main.tsx` 仍约 3,000 行，能力矩阵只完成 code→doc 一端，attention state、动作、通知、成本/配额、偏好、分发和自动化面尚未形成产品闭环。

## 2. 需求池结论

### 已吸收或已被实现覆盖

这些条目不应继续按“从零开发”排期；如需保留，应改为回归门或文档修订：

- vendor registry 的实际代码已经覆盖 claude、codex、trae、grok、gemini、opencode、cursor、hermes、openclaw、pi；其中 cursor 仍是诚实的进程/宿主上下文空壳，不能被描述成 transcript 或 usage 支持。`M02_S03`、`M02_S04` 的原始“接入新 vendor”叙述需改成现状校验、conformance 和证据档位。
- 活跃 session 反解、并发视图、worktree/file URL 归属、历史压缩、扫描开销诊断和诊断页 evidence flow 已分别由 `M02_S05.001`–`.007` 吸收。它们现在的价值是保护已有语义，而不是继续扩大功能。
- Host 白名单和 Origin 校验已在 `server.go` 实现并有测试；PLAN 的 Q5 不能再写成“完全没有防护”，应改为“写操作/未来事件 API 的认证与权限边界仍需设计”。
- 诊断页已有从 insight 的 `metric_key` 到 evidence signal 的关联、截断披露、重复去除和渲染层验证；后续诊断需求应先复用这条 join path，不再新增平行 anomaly builder。

### 现在推进（最高优先级）

**A. 收口地基与可测发布门**

1. 收口 `M01_S01`：建立基线 tag、明确冻结边界和发布命令记录。
2. 按实测边界完成 `M01_S02` 的包拆分；不要恢复已被证伪的七包目标。保留 `internal/snapshot` 与语义守卫，后续只拆能消除真实依赖环的包。
3. 完成 `M01_S03` 的 popover fast path：用构建产物大小、热首绘和隐藏 surface 请求数验收，而不是只看 lazy import 是否存在。
4. 把四大门变成可重复脚本或 CI；在没有 CI 前，把命令、版本、bundle hash 和实测页面层证据写入质检记录。

**B. 做第一个完整的用户价值闭环：needs-you**（已完成第一版垂直切片，进入真实 watcher 验证）

以 `M03_S01` 为主线，但先做一个受限 vertical slice：

`transcript tail / process evidence → evidence rule → session attention state → popover/tray readout → one-click inspect or copy-resume`。

第一批只选择证据已经存在的状态：`working`、`finished_awaiting_review`、`unknown`，再根据 fixture 和真实 watcher 结果决定是否加入 `waiting_permission`、`waiting_input`、`stuck_candidate`、`orphaned`。每个状态必须带 source、observed-at、freshness、confidence/ground-truth level 和缺失原因；不以“有进程”直接推出“需要用户”。

当前实现使用 `working`、`needs_review`、`unknown` 三态：后端输出状态与原因，popover 显示前 3 个 needs-you 项并可选中会话；下一步只剩真实 watcher 延迟、误报和 CPU 验证。

**C. 把能力矩阵从文档表升级为发布事实**（已完成第一版）

先定义七个 signal family 与四个 capability slot 的映射，再实现 `observed / partial / unavailable / not_configured` 四态。未完成映射前不做七列 UI。完成定义后，再提供 `/api` 只读端点和 dashboard 面板，让用户看到“这个数字为什么有/没有”，并让 direct 与 sandbox build 的差异可以由同一矩阵表达。

当前 snapshot、`/api/capabilities` 与诊断页已经消费同一 registry projection；后续只需继续校验矩阵映射与 sandbox 差异，不再另起表格。

### 条件性后置

- `M04_S01` 成本和 `M04_S02` 配额：等 token evidence provenance、价格表版本/过期状态和 vendor coverage 先定稿；任何无 token 证据的 vendor 只能显示缺失。
- `M04_S03` 看门狗：等 attention 状态和文档化阈值存在后实现；先做线索，不做自动停止。正常长跑、采集缺口和 reset 必须有反例 fixture。
- `M04_S04` 机器影响归因：复用已有 process/session/project 映射和 system deck，先交付只读 inspector，再加入 worktree 冲突信号。
- `M05` 舒适度工作：欢迎流程、Preferences、popover 预热应在核心 attention slice 可用后做；`M05_S03` 先测 WebKit 内存/焦点/首绘，再决定原生 glance 退路。
- `M06` 分发、CLI/SSE、digest、sandbox：需要一个可安装且可反馈的预发布渠道。分发前移是否发生仍是用户决策，不应假装需求已经被验证。

### 需要用户决策或限时调研

优先处理 PLAN 的 Q6（LICENSE/商业化）、Q13（M02 出口是否做预发布渠道）、Q9（locale 市场取舍）和 Q2（是否允许任何显式同意的厂商官方查询）。Q1/Q4/Q11/Q14/Q15/Q17 属于可由限时 spike 或案头调研关闭的问题；不要把它们继续留成没有截止日期的“未来考虑”。

## 3. 可推进的 idea 记录

| Idea | 用户收益 | 最小切片 | 证据与验收 | 停止/降级条件 |
| --- | --- | --- | --- | --- |
| **Needs-you evidence card** | 一眼知道下一步该看谁 | 3 个状态 + source/freshness + inspect/copy-resume | fixture 在 watcher tick 内转态；真实页面每条状态可追溯；三语、unknown 不隐藏 | 事件噪声或 CPU 超预算时先保留 30s snapshot 状态，不上 push |
| **Evidence lineage inspector** | 看懂数字为何存在或缺失 | 点击 metric 显示 source、时间窗、覆盖边界、关联 signal | 从 UI datum 可回到 snapshot field、metric key、证据文件族；无绝对路径泄漏 | 若只能展示推断文本而不能给证据出处，删掉该 inspector 卡片 |
| **Coverage matrix panel** | 选择 vendor/功能时知道保真度 | 先只读 4-slot 矩阵，再扩七族四态 | registry、API、UI、文档同源；变异测试能抓伪支持 | 映射定义不完整时维持 docs-only，不生成漂亮但无依据的表 |
| **“风扇为什么转” inspector** | 从系统压力找到 agent/project | 复用已有 PID、CPU、memory、thermal、mapping | ≤2 次点击到 PID→session→project；资源与 movement 分开 | 归属证据缺失时显示 unassigned，不用 cwd leaf 猜项目 |
| **Low-footprint release channel** | 获得真实需求信号并持续更新 | signed/notarized pre-release + changelog + feedback link | 干净机器安装、更新、回滚和本地隐私审计 | 无用户决策或签名条件未具备时只做内部装机，不宣称已验证市场 |

## 4. 推荐推进方法

每个新需求先写一张小卡，固定包含：

1. **用户问题**：触发场景和用户下一步动作；避免把“想要一个页面”当需求。
2. **证据合同**：输入来源、语义族、观察时间窗、缺失/覆盖状态、禁止推断的内容。
3. **最小垂直切片**：从采集或已有 snapshot 到最终页面/动作，先只覆盖一条可验证路径。
4. **可量化验收**：fixture、真实语料、API、渲染层和装机体验分别验收；声称页面结果就必须在页面层验证。
5. **反例与降级**：损坏文件、缺失证据、过期数据、权限不足、vendor 格式变化和零值都要定义形状。
6. **性能与隐私门**：新增 watcher/poller 说明增量 CPU、内存、请求和磁盘成本；所有新接口默认 loopback，状态改变接口必须有 Host/Origin/后续认证边界。
7. **收口记录**：更新对应 sprint、`CURRENT.md` 和必要的 metric/design docs；代码、嵌入 bundle、安装版本和验证命令必须对应同一提交。

建议采用“一个活跃主 sprint + 一个必要 FIX”的纪律：先完成 attention vertical slice，再开并行功能；任何回归或超过 30% 的范围偏差都新建 FIX，不在旧 checklist 后追加解释。每个 milestone 出口做一次吸收巡检、能力矩阵漂移检查和需求信号复盘。

## 5. 下一步顺序

1. 用户裁决 Q6/Q13，决定是否把可安装预发布和 LICENSE 提前。
2. 收口 M01_S01，完成 M01_S02/M01_S03 的可重复门禁。
3. 做 `M03_S01` 的三状态 attention vertical slice，并在真实 watcher 上验证延迟、CPU 和缺失语义。
4. 依据真实 slice 结果更新 `M03_S02`/`M03_S03`，只实现被证据和用户动作证明有价值的动作。
5. 重写 M02_S01、M02_S03、M02_S04 的旧验收文字，把已落地代码、未完成设计和后续 conformance 分开。
6. 关闭一批限时问题后，再决定成本/配额、看门狗、分发和 sandbox 的顺序。

这份 review 不关闭任何需要用户拍板的 Q 项；它只把推进顺序、证据标准和下一次可交付切片固定下来。
