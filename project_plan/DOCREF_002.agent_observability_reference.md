<!--
meta:
  目的: 相邻领域（agent observability SaaS / 遥测标准 / 多会话管理器）调研摘要——哪些信号与 UX 模式值得本地化移植、哪些在无插桩前提下诚实做不到；支撑 M03（attention 状态机）、M04（异常线索）、M05_S04（hooks/OTLP）、M06_S02（事件 API 命名）。
  日期: 2026-07-26（调研与信息更新时间）
  来源: ADJACENT 调研；出处为各产品官方文档 / GitHub 仓库 / OTel 规范 / 对比评测，原始链接见各条目与文末。
-->

# DOCREF_002 · 相邻领域参照：agent 可观测性与多会话管理

## 1. SaaS 可观测性平台（捕获什么信号、怎么可视化）

- **AgentOps** — 会话中心制：每次运行是可回放会话，瀑布时间线含 LLM 调用/tool 调用/错误/逐步延迟与 token 成本（400+ 模型）；session replay 时间旅行调试；prompt-injection 审计轨迹。需 SDK 插桩，cloud-first。<https://github.com/agentops-ai/agentops>
- **Langfuse** — 开源采用度最高；其数据模型是品类事实词汇表：trace → span/generation/event，session 聚 trace、user 聚 session；OTel 摄入减少锁定；成本/延迟/质量看板与告警。插桩开销实测 ~15%。<https://langfuse.com/docs/observability/overview>
- **LangSmith** — 告警故事最强：延迟/成本/质量回归告警、账单异常检测；2026 统一成本视图（token + tool 调用 + 外部 API 开销一屏）。LangChain 优先；云产品。<https://www.langchain.com/langsmith>
- **Helicone** — 零改码捕获的证明：AI-gateway 代理在 LLM API 前记录一切（成本、延迟、TTFT、session），无 SDK——架构上最接近 agentload 的免插桩立场。代价：代理加 5–30ms、可靠性耦合；被收购后疑似进入维护模式（依赖前须复核）；**代理只见 API 流量，见不到本地 agent 行为（文件/进程/审批）**。<https://www.helicone.ai/blog/the-complete-guide-to-LLM-observability-platforms>
- **Braintrust** — 评测优先：trace 是带类型 span 的 DAG（LLM/tool/score/task/**review**——为人工判断设专属 span 类型值得注意）；CI 式 eval 门。重 SDK（~1 周改造），被动观测者无法触及其评测深度。<https://www.braintrust.dev/articles/helicone-vs-braintrust>

## 2. 遥测标准与 Claude Code 原生信号（本地可白拿的升级）

- **OTel GenAI semantic conventions** — 新兴共享词汇：固定 span 名（`invoke_agent`/`chat`/`execute_tool`）、`gen_ai.*` 属性（agent.id/name、provider.name、token 计数）、operation-duration 指标、MCP tool-call 约定。**仍是 pre-1.0**，2026-06 仓库拆分后仍在漂移——须 pin 版本并用映射层隔离约定字符串。规范原文的纪律要求："instrumentation must not report usage metrics when it cannot efficiently obtain them"——**对 agentload never-fabricate 语义的标准级背书，值得在文档中引用**。<https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/>
- **Claude Code 原生 OTel 遥测（opt-in，beta）** — 用户设 env 后自愿发出 OTLP：`claude_code.token.usage`（按 input/output/cacheRead/cacheCreation、model、query_source）、`claude_code.cost.usage`（含 agent.name、skill.name）、行数/commit 计数，及 API 请求 / tool 执行 / **permission 决策**结构化事件。零 SDK 工作量即得最高保真信号——本地起一个 loopback OTLP receiver 即可。注意：OAuth 场景会带 `user.email` 属性，须本地脱敏；beta 可能变化；仅盖 Claude Code。<https://code.claude.com/docs/en/monitoring-usage>
- **Claude Code hooks 作为事件总线** — 生命周期事件触发 shell 命令带 JSON payload：PreToolUse/PostToolUse/PermissionRequest/Notification（matcher：`permission_prompt`、`idle_prompt`、auth_success）/Stop/SubagentStop。`permission_prompt` 标记 agent 阻塞等审批的精确时刻（attention routing 关键信号）；`idle_prompt` 区分"停着等输入"与"在干活"——**纯进程观测者无法消解的歧义**；Stop/SubagentStop 给干净的 turn 完成时间戳。约束：要写入用户 settings.json——必须是明示同意的 opt-in，绝非静默注入；schema 为 Claude Code 专属。<https://code.claude.com/docs/en/hooks>

## 3. 多会话管理器（借 UX，不借捕获架构）

- **Conductor**（Melty Labs，$22M A 轮）— 每 worktree 一 workspace 的 fleet 视图，attention 状态是首要组织原则；给出 **3–5 个并行 agent 是人类监督上限**的判断——fleet 视图应按 ~5 会话优化密度并优雅溢出。编排器本质：只见自己拉起的会话。<https://www.conductor.build/>
- **Crystal/Nimbalyst + Claude Squad** — 殊途同归的会话模型：worktree 里的会话 + 四态生命周期（initializing / running / **waiting-for-input** / completed）+ 按等待/运行排的 kanban + diff 优先 review。"waiting for input"作为一等状态是每个 fleet UI 独立重复发明的模式。Crystal 已弃养（2026-02）——此子品类流失率高。<https://github.com/stravu/crystal>
- **VibeTunnel（+ 官方 claude remote-control）** — 把终端代理进浏览器远程看 agent 进度；起源即 agentload 的用户需求原话："we all wanted to check on our AI agents and see how far they'd gotten"。localhost 默认 + Tailscale 自有网络作隐私友好远程路径——**local-first 远程访问的蓝图**；asciinema 录制作可回放证据。短板：需 `vt claude` 包装命令，漏掉未包装的会话；只是终端镜像，无分析。<https://github.com/amantus-ai/vibetunnel>
- **VS Code Agents window + LangGraph Agent Inbox** — 注意力路由双模式：统一会话列表（本地 CLI + 云 + Claude 一屏，"needs input/permission"徽标，完成后多文件 diff）vs Gmail 式收件箱（每个被阻塞的 agent 动作是一条可 approve/edit/reject 的 triage 项）。**Inbox 框架在 HIL 场景优于 dashboard**：schema 强约束、可审计、即刻熟悉。两者都是控制器而非中立观测者。<https://code.visualstudio.com/docs/agents/agents-window>

## 4. 关键洞察

1. **所有相邻产品独立收敛于同一模式：会话 attention 状态机**（working / waiting-for-input / waiting-for-permission / idle / done）渲染为 triage 面（徽标、kanban 或 inbox）。这完全可以本地实现——hooks Notification matcher + JSONL transcript tail 给出真值转移——是 agentload popover/dashboard 的最高杠杆升级：**fleet 按"needs you"排序而非按新旧**。
2. **两条零 SDK、基于同意的 Claude Code 信号升级路径**：(1) loopback OTLP receiver + 用户开 `CLAUDE_CODE_ENABLE_TELEMETRY`，得逐请求 token/cost、tool 执行、permission 决策；(2) 明示同意安装轻量 hooks 把事件追加到本地文件。两者数据全在本机，能为 Claude Code 切片补齐对 SDK 平台的保真度差距大半。→ M05_S04。
3. **无插桩也能本地做成的**：AgentOps 式会话瀑布/回放（从 JSONL transcript 重建；ccusage 证明成本核算可行、sniffly 证明错误/摩擦分析可行）；LangSmith 式本地告警（burn-rate 突刺、错误连击、超阈值卡死会话）；VibeTunnel 式只读远程查看（走用户自己的 tailnet）。
4. **无插桩做不到或不诚实的（明确不竞争）**：不落本地日志的 agent 的完整 prompt/completion 捕获；LLM-as-judge 质量评测与 eval 门（Langfuse/Braintrust 地盘）；TTFT 与请求内延迟（需代理，与中立观测者立场冲突）；跨服务分布式 trace。
5. **命名策略**：metric registry 在有证据处采用 OTel GenAI semconv 词汇（gen_ai.agent.name、execute_tool、operation-duration、error.type），但 pin 版本 + 映射层隔离（pre-1.0 且 2026-06 迁仓中）。低成本换未来互操作（可导出到任意 OTel 后端）。→ M06_S02。
6. **定位结论**：编排器只见自己启动的会话；SaaS 只见插桩过的应用。**"无论从哪启动都看得见"的中立本地观测者是真空位**；该向它们借的是 UX（attention kanban、会话瀑布、统一会话列表、needs-input 徽标、完成即 diff），不是捕获架构。
7. **人类上限**：Conductor 的 3–5 并行 agent 监督上限 → fleet 视图按 ~5 会话优化信息密度、优雅溢出，把"下一个该看谁"当第一问题——inbox 式 review 提示，不是指标墙。

## 来源与准确性

- 更新时间：2026-07-26。出处类型：官方文档与规范（高：OTel semconv、code.claude.com docs）、GitHub 仓库（高）、厂商博客与第三方对比文（中，注意 Helicone/Braintrust 对比文出自厂商自身，有立场）。
- 风险提示：OTel GenAI 约定 pre-1.0 且迁移中，字段名可能再变；Claude Code 遥测为 beta；Helicone 维护状态需复核。
- 主要链接：<https://github.com/agentops-ai/agentops> · <https://langfuse.com/docs/observability/overview> · <https://www.langchain.com/langsmith> · <https://www.helicone.ai/blog/the-complete-guide-to-LLM-observability-platforms> · <https://www.braintrust.dev/articles/helicone-vs-braintrust> · <https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/> · <https://opentelemetry.io/blog/2026/genai-observability/> · <https://code.claude.com/docs/en/monitoring-usage> · <https://code.claude.com/docs/en/hooks> · <https://www.conductor.build/> · <https://github.com/stravu/crystal> · <https://github.com/smtg-ai/claude-squad> · <https://github.com/amantus-ai/vibetunnel> · <https://code.visualstudio.com/docs/agents/agents-window> · <https://github.com/langchain-ai/agent-inbox> · <https://www.digitalapplied.com/blog/agent-observability-platforms-langsmith-langfuse-arize-2026>
