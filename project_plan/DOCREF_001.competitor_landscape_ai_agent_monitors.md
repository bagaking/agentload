<!--
meta:
  目的: 直接竞品全景（本地 AI coding-agent 监控品类，5 个子品类、15 项调研对象），支撑 PLAN.md §3 SWOT / §4 品类格局与 M02（vendor 广度）、M03（attention）、M06（分发）的取舍依据。
  日期: 2026-07-26（调研与信息更新时间）
  来源: DIRECT 竞品调研；出处为各项目 GitHub 仓库 / 官网 / 第三方评测，原始链接见各条目与文末"来源与准确性"。
-->

# DOCREF_001 · 直接竞品全景：本地 AI agent 监控

## 1. 子品类一：日志解析型分析器

- **ccusage (ryoppippi)** — npm CLI，品类事实标准。解析 `~/.claude/projects` 本地 JSONL 出 daily/weekly/monthly/session 与 5 小时 block 报表，含逐模型成本；已扩到 Codex/Gemini CLI/Copilot CLI/OpenCode/Amp/Droid 等 8+ 工具，众多下游（CCSeva、viberank）构建其上。MIT、纯本地、npx 零安装。短板：成本是估算非账单真值；仅终端；回放历史日志、无实时进程/会话观测。<https://github.com/ryoppippi/ccusage>
- **Claude Code Usage Monitor (Maciek-roboblog)** — Python/Rich TUI，基于 ccusage 数据加 burn-rate 实时追踪、5h 滚动窗口、耗尽时间预测（Pro/Max5/Max20 + P90 自定义档位检测），刷新 0.1–20Hz，700+ 测试。其流行证明"我什么时候被掐断"是品类情绪核心。短板：仅 Claude；limit 靠推断，Anthropic 改套餐即漂移（issue 常态化抱怨）；占用一个终端 pane。<https://github.com/Maciek-roboblog/Claude-Code-Usage-Monitor>
- **sniffly (Chip Huyen)** — localhost web 面板，问的不是"花了多少"而是"Claude Code 哪里出错"：错误分类学（招牌洞察：20–30% 错误为 Content Not Found）、按项目下钻、可分享统计链接。短板：回顾式非实时；仅 Claude；需 Python；浏览器标签页形态不 ambient。<https://github.com/chiphuyen/sniffly>

## 2. 子品类二：菜单栏配额追踪器（拥挤区）

- **CCSeva** — 菜单栏实时 Claude 用量百分比 + 弹层多 Tab；标志性事件：从 Electron+React+ccusage 迁移为原生 Swift 自解析 JSONL（增量扫描+去重）——品类整体"转原生减脚印"趋势的样本。短板：ad-hoc 签名被 Gatekeeper 拦（无 Developer ID）；仅 Claude。<https://github.com/Iamshankhadeep/ccseva>
- **CodexBar (steipete) + caut** — 覆盖面最广的菜单栏配额器：Codex/Claude/Cursor/Gemini/Copilot/OpenRouter/Ollama 等，混合数据通道（CLI 日志、OAuth token、Keychain、浏览器 cookie）；带 CLI 伴生工具供脚本/agent 消费；Rust 跨平台移植 caut 覆盖 16+ provider。短板：纯配额视角、无会话/进程活动；cookie/Keychain 触信任疑虑。<https://github.com/steipete/CodexBar>
- **ClaudeBar (tddworks)** — 11 provider 配额聚合，色码 Session/Weekly/逐模型条；签名+公证+Sparkle+Homebrew；内置 Claude Code "skill" 让 agent 自行添加 provider。短板：依赖各 provider CLI 已装且已登录；macOS 15+；维护者少（~48 open issues）。<https://github.com/tddworks/ClaudeBar>
- **SessionWatcher** — 少数收费玩家（$6.99 单次 / Pro $59），Codex 5h 窗口+周上限+锁死前阈值通知；证明品类能收到钱。闭源，信任仅靠营销声明；靠激进 SEO 内容运营塑造品类认知。<https://sessionwatcher.com/>
- **长尾**（cc-usage-bar、ClaudeUsageBar、ai-token-monitor、SwiftBar 脚本）— 单指标单厂商小工具泛滥，证明"菜单栏放一个百分比"已饱和、不再构成差异化。<https://github.com/lionhylra/cc-usage-bar>

## 3. 子品类三：代理拦截型观测

- **ccflare / better-ccflare** — 以 `ANTHROPIC_BASE_URL` 代理截获每个请求：请求级延迟/错误分析、多账号轮换、SQLite 持久化、web 面板 + TUI。唯一"请求发生时即看见"的路线；代价是全流量过它（设置摩擦+单点故障），多账号轮换踩 ToS 边线；原版失修、fork 分裂社区。相关：seifghazi/claude-code-proxy 可视化进行中的会话。<https://github.com/snipeship/ccflare>

## 4. 子品类四：会话历史 / 管理 GUI

- **opcode（原 Claudia）** — 最高星 Claude Code GUI（~20k star，Tauri 2，AGPL）：会话浏览、checkpoint 时间线 diff/fork/restore、MCP 管理、用量面板。2026 年对比文章称已停止活跃维护——被 Anthropic 官方 Claude Code Desktop 挤压。**教训：范围过宽 + 与一方产品正面撞线 = 弃养**。<https://github.com/getAsterisk/opcode>
- **Agent Sessions (jazzyalex)** — agentload 最近的哲学邻居：本地优先、只读、MIT 原生 macOS，解析 10+ agent 的 JSONL/SQLite 存储做统一跨 agent 搜索、一键 resume 到终端、浮动 Quota Meter；"Session Runway"以 4 个视角（5h/weekly/tokens-hr/dollars）把配额消耗归因到单个会话（品类罕见）。短板：仅 macOS；配额计仅盖 Codex+Claude；UI 反复（Cockpit 模式被移除）；社区小。<https://github.com/jazzyalex/agent-sessions>
- **viberank** — ccusage 数据的公开排行榜（社交/攀比层，榜首晒 $458k）；证明用量数据的身份属性，但自报可刷、庆祝花钱非产出，无监控效用。<https://github.com/sculptdotfun/viberank>

## 5. 子品类五：编排器附带监控 + 官方遥测

- **claude-squad** — tmux + 每任务一 git worktree 的 TUI 多 agent 工作台（8.2k star，AGPL）：list+preview/diff、后台完成、auto-accept。监控是管理的副产品——只能看见自己启动的会话。<https://github.com/smtg-ai/claude-squad>
- **Conductor + Crystal→Nimbalyst** — GUI 并行 agent 编排（Conductor 闭源商业；Crystal MIT 已于 2026-02 弃养转 Nimbalyst）："一眼看清各 agent 在干嘛"是核心卖点；同样只见自己拉起的会话，且此子品类流失率高。<https://conductor.build/>
- **OTel/Grafana 栈** — Claude Code 原生 OTLP 导出（`CLAUDE_CODE_ENABLE_TELEMETRY=1`）→ collector → Prometheus/Grafana；唯一官方认可通道，信息最富（tool call、缓存效率、cost-per-commit、edit 接受率），Grafana 官方 Cloud 集成（2026-03）。面向团队/组织，重设置、非消费级 ambient 产品。<https://github.com/aaraujodata/claude-code-otel>

## 6. 关键洞察（直接决定 roadmap 取舍）

1. **Table stakes**（人人都有，做了不加分、没有出局）：只读解析本地 JSONL/会话存储；日/周/会话 token+成本报表；5h 窗口与周上限倒计时；macOS 菜单栏色码百分比；"local-only 无遥测"声明；免费 + MIT。**仅支持 Claude 已属落后**——所有增长中的工具都在数月内扩到 Codex/Gemini/Copilot。
2. **差异化空位**：实时进程级"agent 此刻在干嘛"观测（agentload 核心区几乎无人占据——对手要么事后回放日志、要么猜配额 API、要么必须自己当编排器）；配额消耗按会话归因（仅 Agent Sessions）；行为/错误分析（仅 sniffly）；请求级截获（仅代理，代价是截流）；**诚实的证据语义**——多数工具靠推断 limit，Anthropic 一改套餐即公开翻车，"never fabricate, missing stays missing"是值得在 UI 文案里大声说出的防御性信任地位。
3. **数据通道的脆弱性-丰富度谱系**：(1) 官方 OTel 导出——受认可、最富，但 opt-in 重运维；(2) JSONL/SQLite 解析——零配置但随格式变化断裂、且是回顾式；(3) 代理截获——实时完整但侵入、贴 ToS 线；(4) 进程观测——实时、厂商中立、抗格式变化，但粒度粗。**几乎没人组合多层**；进程证据 + 日志解析 + 可选 OTel 的融合可在覆盖度上超过所有人。
4. **品类被两头挤压**：Anthropic 一方功能（/usage、/stats、Console 分析、Claude Code Desktop、Grafana 官方集成）把基础用量展示商品化（已促成 opcode 弃养）；编排器（Conductor、claude-squad、Crystal→Nimbalyst）从上方吸收监控。**耐久的中间地带正是中立跨厂商观测者**：一方工具永远不会监控竞品 agent，编排器只见自己启动的会话。
5. **品类走向**：从"烧了多少 token"（2025）转向"我的 agent 在干嘛、哪个需要我"（2026）——fleet 监督、attention routing（needs-review 提示）、按会话问责。伴随趋势：Electron→原生 Swift（CCSeva、Agent Sessions、CodexBar）；签名/公证成为信任信号；CLI/JSON 伴生输出供 agent 消费监控数据；变现始终薄（$7–59 一次性）——**价值归于成为 ambient 默认者，而非订阅**。
6. **agentload 可独占的具体空位**：(a) 跨平台——几乎所有打磨过的对手都 macOS-only，Windows/Linux 空白；(b) i18n——无对手出 zh/ja 本地化，而 CJK 社区庞大；(c) App Store 分发——开源阵营无人过公证+审核，"普通人可安装"空置；(d) **一个产品同时提供 ambient 菜单栏（CodexBar 长项）+ 深度 web 面板（ccflare/sniffly 长项）**——现状是用户要同时跑 2–3 个工具。

## 来源与准确性

- 更新时间：2026-07-26。出处类型：一手 GitHub 仓库与官网（可信度高）、第三方评测/博客与对比文（中，注意 SessionWatcher 类"自评式对比内容"有利益倾向）、HN 讨论（中）。
- 活跃度/弃养判断（opcode 停维护、Crystal 转 Nimbalyst）来自 2026 年对比文与官方公告，采用前建议复核仓库 commit 记录。
- 主要链接：<https://github.com/ryoppippi/ccusage> · <https://ccusage.com/> · <https://github.com/Maciek-roboblog/Claude-Code-Usage-Monitor> · <https://github.com/Iamshankhadeep/ccseva> · <https://github.com/steipete/CodexBar> · <https://github.com/Dicklesworthstone/coding_agent_usage_tracker> · <https://github.com/tddworks/ClaudeBar> · <https://sessionwatcher.com/> · <https://github.com/snipeship/ccflare> · <https://github.com/tombii/better-ccflare> · <https://github.com/seifghazi/claude-code-proxy> · <https://github.com/chiphuyen/sniffly> · <https://github.com/getAsterisk/opcode> · <https://github.com/jazzyalex/agent-sessions> · <https://github.com/sculptdotfun/viberank> · <https://github.com/aaraujodata/claude-code-otel> · <https://grafana.com/docs/grafana-cloud/monitor-infrastructure/integrations/integration-reference/integration-claude-code/> · <https://github.com/smtg-ai/claude-squad> · <https://conductor.build/> · <https://github.com/stravu/crystal> · <https://code.claude.com/docs/en/analytics> · <https://github.com/lionhylra/cc-usage-bar> · <https://news.ycombinator.com/item?id=45081711>
