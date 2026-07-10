<!--
meta:
  目的: 多 agent 日常用户的 jobs-to-be-done 与痛点调研（12 项，按 频率×严重度 排序），是 M03（attention）、M04（成本/配额/看门狗/归因）、M06_S03（digest）优先级排序的需求侧依据。
  日期: 2026-07-26（调研与信息更新时间）
  来源: NEEDS 调研；出处为 Reddit 配额讨论串（经二级报道转述佐证）、HN 并行 agent 讨论、ccusage / Claude-Code-Usage-Monitor / anthropics/claude-code GitHub issues、工作流博客，原始链接见各条目与文末。
-->

# DOCREF_004 · 用户需求与痛点（按 频率×严重度 排序）

## 排序总览

1. 配额焦虑 + 监控器不可信（合并为品类第一痛）→ M04_S02
2. Attention triage："哪个会话现在需要我" → M03 全里程碑
3. 失控 agent 检测（低频×灾难级）→ M04_S03
4. Review 瓶颈（普遍但被编排器部分服务）→ M06_S03
5. 机器负载/发热归因 → M04_S04
6. 跨 agent 统一视图 → M02_S03/S04
7. 会话身份与上下文 → 既有会话映射能力 + M03_S03
8. 成本归因（项目/会话/subagent）→ M04_S01
9. 离机/多设备监控 → M06_S02（本地事件面）
10. 协作冲突可见性 → M04_S04
（元需求）监控器自身必须轻、不扰、隐私干净 → M02_S02

## 各项痛点/工作详述

1. **配额焦虑：不透明的 5h 窗口与周上限阻断真实工作**。品类最响的痛：r/ClaudeAI"20x max usage gone in 19 minutes"24h 内 330+ 评论；"Limits Were Silently Reduced"360+；周上限让重度并行用户"1–2 天打满、剩下整周锁死"；2026 高峰时段收紧使 ~7% 用户撞上原本不会撞的限制；配额横跨 Claude Code/claude.ai/Cowork 一处漏光全部干涸。Job："告诉我烧多快、何时撞墙、何时重置"——ccusage（~9k star）与 CCUM 存在的全部理由。现有 burn-rate ETA 只是叠在无文档记账上的估算。<https://techcrunch.com/2025/07/28/anthropic-unveils-new-rate-limits-to-curb-claude-code-power-users/>
2. **可信的限额信号：与官方账本相左的监控器被骂"misleading"**。ccusage #298"shows normal usage, but limit is reached...Why it is misleading?"、#483 Live Blocks 不准（27 评论）、#288/#705 token 计数错误；CCUM #48/#52 重置时间"Fundamentally Wrong"、#1 靠众筹 limit 数据；Codex 侧更糟：#950 subagent 91 倍 token 膨胀、#1434 记账严重问题、#897 fork 会话重复计数。**根本上无人服务：厂商不公开账本，所有外推工具最终"说谎"。中立证据立场（只显观测到的、明确标注未知）是唯一可辩护姿态——正是 agentload 的语义**。<https://github.com/ccusage/ccusage/issues/483>
3. **Attention triage："哪个会话现在需要我"**。3–6 会话散在终端 tab/tmux pane，人错过 agent 完成/提问/卡权限的时刻，agent 闲置数分钟到数小时。AgentsRoom："agents 不是问题，跟踪它们才是。"点解决方案成行成市：Chive（$29 菜单栏红绿灯）、Claude Code Notifier（"告诉你到底哪个需要你"，终端聚焦时抑制）、CCNudge、tmux bell 转发、macos-notify-mcp（点击通知聚焦对应 pane）。用户要的状态分类：working / waiting-for-approval / asking-a-question / done / stuck；permission prompt 是最费时的中断。**只有碎片化点工具服务；无人把 attention 状态 + 用量燃烧 + 机器负载合成一个 glanceable 面**。<https://software-dc.com/blog/4-claude-code-tmux-how-i-got-notifications-working>
4. **失控 agent 断路器：循环与过夜 token 篝火**。低频、灾难级：anthropics/claude-code #26171 无界 thinking 循环烧 token 直到人按 Esc；#75314 十个后台任务"Running"34+ 小时、~1.08M token、无法取消、周限额报废；某 gist 记录递归 subagent 5 分钟内吃掉 4M token（整个 Max 20x 5h 窗口）、50+ 层递归；病毒式故事：cron + `--dangerously-skip-permissions` 一夜 $6,000。用户结论："detection plus immediate exit is the real circuit breaker——config 救不了你，速度才行"；"agent 卡住时不 crash——它循环，每圈都花钱"。**完全无人服务：没有本地看门狗标记异常燃烧斜率、卡死 Running、异常 spawn。纯观测式线索（无需捏造）正好落在 agentload 能力内**。<https://github.com/anthropics/claude-code/issues/75314>
5. **一个监控器盖多个 agent CLI**。用户刻意在厂商间轮换摊薄配额（"spreading the load 就很少撞任何单一平台的顶"）；编排器把异构 fleet 常态化（Vibe Kanban 支持 13 agent）；ccusage issue 史证明拉力：#626 求 codex、#757/#845/#865 求 opencode、#855 求 gpt-5.3-codex。**部分被服务但质量差：ccusage 的 codex 分支 91 倍膨胀（#950）——跨 agent 的"质量对等"才是未被服务的部分；agentload 已观测 claude/codex/trae 进程，领先多数 Claude-only 工具**。<https://github.com/ccusage/ccusage/issues/626>
6. **"我的笔记本在熔化"：agent 进程堆积、风扇狂转、电池速降**。anthropics/claude-code #11122 多 CLI 进程跨会话堆积、"用户觉得电脑慢却不知原因"、热节流；#22253 全新 M5 MBP 四个 Claude 进程各 100% CPU 却在闲置；#22968 长会话内存泄漏；#19393 闲置 100%+ CPU"风扇常转"。需求证明：ClaudeCodeMonitor、c9watch 专为此存在。**Job 是归因："机器烫是不是 agent 干的、是哪个？"Activity Monitor 无法把 PID 映到会话/项目；孤儿/僵尸进程在系统劣化前不可见。几乎无人服务——agentload 的资源检查器 + 热压状态 + 证据式会话映射是观测到的唯一组合打法**。<https://github.com/anthropics/claude-code/issues/11122>
7. **Review/验证是新瓶颈，不是生成**。HN 一致："I can't write spec files quick enough...and I can't QA their output"；"Fancy orchestration is mostly a waste, validation is the bottleneck"；博客共识："超过一定数量 agent，你的 review 就是瓶颈——真正的极限是你的注意力"；实践者把有效并行数压在 ~3–10（Addy Osmani），约 1/3–1/4 的 prompt 需要人推一把。**diff review 本身已被编排器服务；监控器的未服务切片是"优先级"：哪个会话的产出已就绪且风险最高**。<https://news.ycombinator.com/item?id=46902368>
8. **会话身份与上下文：哪个终端是哪个、别逼我重建上下文**。claunch 作者：每次上下文重建 10–15 分钟、一天切几次项目损失可观；hboon 的 tmux 指南靠 pane 内容自动改名，因为"状态栏显示五个同名 agent"。人们手搓命名 hack = 映射这个 job 是真的。**只有手工 hack 服务；无中立跨终端观测者自动做——agentload 的证据式会话映射正是此 job**。<https://dev.to/kaz123/how-i-solved-claude-codes-context-loss-problem-with-a-lightweight-session-manager-265d>
9. **成本归因：项目/会话/subagent 明细**。ccusage #281 按项目细分、#929/#560 工作目录内按会话细分、#313 子任务 token 追踪（23 评论）——subagent 花费在主会话数字里不可见；fork 会话重复计数（#897）。预算问题（"哪个项目在吃配额"）+ API 用户对账。**subagent 归因是最难子项，也是最空的**。<https://github.com/ccusage/ccusage/issues/313>
10. **离机监控：手机、第二台机器、多设备合计**。agent 跑几十分钟到数小时，人会离桌。证据：claude-code-mcp-controller（手机控）、Claude Code Channels（Telegram/Discord 汇报）、ntfy.sh 转发、HN 求过夜排队；多机记账割裂：ccusage #222 多设备准确追踪、#287 提议 opt-in 同步 daemon（并明确要 iCloud 文件夹而非云服务）。**local-first 形态无人服务；现有方案都把数据路由过第三方 relay，与隐私敏感用户冲突**。<https://github.com/ryoppippi/ccusage/issues/222>
11. **协作开销：冲突、重复劳动、编排税**。相关代码上并行 agent 产生合并冲突与"相似但略异"的重复实现；metacircuits 实测并行会话开销 ~1 小时/天；Anthropic 自用 lock-file 模式防两 agent 认领同一工作。反流声音（HN）："我不需要 agents 互聊，我需要一个 agent 把活干对。"**编排器拥有修复权；监控器的未服务角度是让开销可见——两个会话被观测到落在同一 repo/worktree 即是可告警条件**。<https://metacircuits.substack.com/p/managing-parallel-coding-agents-without>
12. **（元需求）监控器自身必须轻、不扰、隐私干净**。锋利证据：ccusage #459 自家 statusline 集成造成"无限进程 spawn 循环 100% CPU"；#262/#676 live 模式因 CPU 成本被提议弃用；CCUM #17 求更小 UI、#55 亮色模式不可读、#126 求 greppable 输出；分发摩擦也算（#7 求正经打包、#31 求 brew）。**监控资源大户的人绝不容忍看门人自己变成大户；全天挂在余光里（菜单栏/statusline）所以 glanceable + 零配置是硬要求。原生低脚印菜单栏观测者是差异化形态**。<https://github.com/ccusage/ccusage/issues/459>

## 横向洞察

- **真正未被服务的**（机会清单）：(a) 诚实限额信号——显示观测证据 + 明确标注未知，独此一家（= agentload 语义）；(b) 失控/异常检测本地看门狗；(c) attention 状态 + 燃烧 + 系统负载合一的 glanceable 面（今天要同时开 Chive + ccusage + Activity Monitor）；(d) agent 进程→会话/项目→CPU/热压归因（"风扇为什么转、哪个 agent、哪个项目"）；(e) 不过第三方 relay 的 local-first 多设备聚合与离机推送。
- **已被服务、避免正面竞争**：diff review 与 worktree 编排（Conductor、Vibe Kanban、Claude Squad）；原始 token/成本表（ccusage）；单厂商 burn-rate ETA（CCUM）。agentload 的胜位是这些工具缺的观测层，不是再造编排器或成本表。
- **用户隐含要求的 attention 状态分类**：working / waiting-for-permission / asking-a-question / finished-awaiting-review / stuck-or-looping / orphaned-process。permission prompt 是最费时中断；"stuck vs working"之辨的缺席正是灾难案例（第 4 条）的成因。→ M03_S01 状态机直接采纳。
- **信任是贯穿全部来源的元主题**：用户不信厂商记账（静默降限）、不信监控器（live blocks 不准）、不信 agent（1/3 prompt 需干预）。**品牌为"从不捏造、只显证据、承认看不见"的产品对齐品类最强未满足情绪需求**。
- **跨 agent 对等是质量竞赛不是打勾竞赛**：ccusage 加了 codex 却 91 倍膨胀，比没有更糟。agentload 以同等保真观测 claude/codex/trae——哪怕保真意味着"仅进程级、诚实标注"——胜过深而错的计量。
- **看门人必须近零成本**：领头 TUI 监控自己 spawn 无限进程 100% CPU（#459）、live 模式因 CPU 被提议弃用（#676）。原生菜单栏近零脚印既是功能也是营销声明。

## 来源与准确性

- 更新时间：2026-07-26。出处类型：GitHub issues 一手（高）；HN/博客（中）；**Reddit 讨论串标题（"20x max usage gone in 19 minutes"330+ 评论、"Limits Were Silently Reduced"360+）经二级报道转述佐证而非直接抓取（中低，引用时注意）**；需求证明型工具（Chive、Claude Code Notifier、CCNudge、ClaudeCodeMonitor、c9watch、macos-notify-mcp 等）以其存在本身作证据。
- 主要链接：<https://news.ycombinator.com/item?id=46902368> · <https://news.ycombinator.com/item?id=46990733> · ccusage issues #23/#137/#146/#222/#281/#287/#298/#313/#459/#483/#626/#676/#897/#929/#950/#1434 · Claude-Code-Usage-Monitor issues #1/#44/#48/#52/#55/#75/#121/#150 · anthropics/claude-code issues #11122/#19393/#22253/#22968/#26171/#75314 · <https://techcrunch.com/2025/07/28/anthropic-unveils-new-rate-limits-to-curb-claude-code-power-users/> · <https://metacircuits.substack.com/p/managing-parallel-coding-agents-without> · <https://dev.to/kaz123/how-i-solved-claude-codes-context-loss-problem-with-a-lightweight-session-manager-265d> · <https://software-dc.com/blog/4-claude-code-tmux-how-i-got-notifications-working> · <https://hboon.com/using-tmux-with-claude-code/> · <https://addyosmani.com/blog/code-agent-orchestra/> · <https://agentsroom.dev>
