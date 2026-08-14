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

## 2. 执行状态（更新于 2026-09-20）

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

**2026-09-16 完成**（mini-sprint `M02_S05.001.FIX`，计划外缺陷修复）：

- **活跃 session 永久丢归属已修**：live agent 的 transcript 一旦静默超过 2 小时前台窗口，就永远不再被扫描，渲染成 `missing_transcript`。根因是既有的 priority 旁路只由 lsof 供料，而 lsof 对本机 agent 几乎无效（grok 持 `events.jsonl` 非 `updates.jsonl`；claude append-then-close 不常驻 fd）。修复是把 argv 里**早就解析出来**的 session id 反解成磁盘路径，喂进同一条旁路，不新增机制。实机：claude 10/19 → **17/18**，grok 2/6 → **6/6**。
- **四家 vendor 全部落地**（claude/codex/trae/grok），兑现用户「四家全做」的明确选择。codex/trae 借 uuidv7 内嵌时间戳把日期目录从通配收窄为定值（本机 5275 个 rollout 零例外），**913ms → 20.3ms**。
- **`deferred_files` 口径修正两次**：先前恒报 1（老文件在收集阶段即被丢弃，从未进入计数）；中途一度报 **11043**，因为把「超出 7 天历史地平线」的文件也算成了覆盖缺口——**把范围之外报成「我漏了」同样是虚构**。最终口径只计「索引内但被前台 cutoff 挡下」的文件，实测 2879，与独立统计 2826 吻合。
- **两处诚实性欠账已还**：(a) 我此前告诉用户「后台历史扫描会补齐」是**错的**，生产路径上根本不存在该扫描（D-014）；(b) 我曾以「trae 会产生错误归属」为由擅自把范围收窄为两家，复核后该理由不成立（D-015）。
- **性能**：扫描本身热态 0.52s（前 0.55s），无回退；HTTP 刷新端到端 0.79s（前 0.55s），**小幅回退已记录**未优化。
- **Web dashboard 左下角刷新已补回**：刷新时间戳 + 节拍菜单抽成共享的 `RefreshDock`，popover 与 dashboard 共用一份实现，dashboard 侧同时把原先「盲目循环」的节拍按钮升级为与 popover 一致的选择菜单。

**2026-09-16 完成**（mini-sprint `M02_S05.002.REDESIGN`，用户要求「并发页面应该重新好好设计下」）：

- **图表右端的 0 是虚构的，已修**：每个历史窗口的最后一个格点恰好落在 `now`，而 live session 的 span 止于它最后一条 transcript 事件、永远严格早于 `now`，于是扫描必然返回 0——不是「没有东西在跑」，而是「这里测不出来」。实机对比：旧二进制五个窗口末点全是 `session_concurrency: 0`（机器上实际 44 个已知 session），新二进制该点字段整体缺省，走既有的 honest-missing 通路渲染成留白。
- **设计面板四选一的共识方案是错的，靠实测推翻**：三份独立提案都收敛到「窗口边界不要发 `-1` 关闭事件」，实测 `old [19 19 0] → fixed [19 19 0]` 零变化，因为「真正跨越 `now` 的 span：0/19」。它们的测试只在 `End >= now` 的合成数据上过。改采第四份的「末点不采样」，无条件成立。记为 D-016。
- **三按钮图例换成 live hero**（用户选择）：大字读 `summary.active_sessions`，旁边是已知 session / 可见 PID / 今日峰值，以及常驻的 per-tool PID 拆分（原先藏在图例点击后）。hero 与图表是**两个不同的证据家族**——一个测于快照瞬间、一个扫自 span——这正是图表右端可以诚实留白而 hero 仍能回答「现在多忙」的原因。
- **两处图表修正**：并发是阶跃函数，曲线插值出的中间值从未被测量（违反 metric-semantics 契约），改 `LineType.WithSteps`；可见 PID 量级远大于两条 session 序列（实测 7D pids 28-106 vs burst 0-10），共用一条价格轴时信息量大的序列被压进底部十分之一，给它 `priceScaleId: "left"` 独立轴。两条轴都不标数值——不同证据家族的高度本就不可比。
- **删掉了 UI 里掩盖该 bug 的 hack**：readout 原先 `reverse().find(d => d.value > 0)` 跳过末点的假 0，导致图例与图表互相矛盾（metric-semantics 另行禁止）；后端不再虚构后，这段扫描只会藏住真正空闲的桶。
- **净删代码**：13 文件 +307 / -484。`TrendRuntimeDrilldown` 一族（110 行）与其 126 行 CSS 随图例一并作废，6 个 i18n key × 3 语言清掉。
- **实机验证**：新二进制跑在 8699（刻意避开用户在用的 8642），五个窗口末点均只剩 `at`，前一个桶正常携带真实计数；hero 读的四个字段全部有值（active 3 / known 44 / pids 45 / 今日峰值 18）。

**2026-09-18 完成**（mini-sprint `M02_S05.003.FIX`，用户「现在的吞吐页面, worktree 好像还没往对应的项目上聚合?」）：

- **worktree 被拆成独立项目行已修**：根因是**同一 session 中途 `cd` 进临时目录**。实读真实 trae transcript，同一文件里两个 cwd——先 `.../farm/.local/workspaces/flowlens/flowlens-v2-analysis-cost`，后 `/tmp/flowlens-audit-bea26d8c.RjqnPR`（agent `mktemp -d` 出的审计沙盒）。两者都以 `transcript_cwd`（rank 4）上报，同 rank 后者按出现顺序胜出，项目名遂变成临时目录名。
- **不对称是缺陷本体**：`setTraceProjectPath` 里 worktree/branch 只在 `resolveRepoBoundary` 成功时才写，项目名却无条件退到 `filepath.Base`——所以那一行的 worktree 名是对的、只有项目键被污染。修复即补上这个不对称，一处判断，零额外 stat（复用同一次 `resolveRepoBoundary` 结果）。
- **上一轮三次修复失败的教训**：都改在 `pathProjectName`（5 个调用方共享的纯函数）并用 `os.Stat` 当判据，分别被 8 个 / 2 个既有测试和自己的回归测试推翻。`os.Stat` 无法区分「已删除的临时 checkout」与「本就不该 stat 的路径」（相对 cwd、grok 从存储目录名解码的 cwd、`.benchmark` 工作区）。**边界选错了**：缺陷在「两条同 rank 证据竞争」那层，只有 `setTraceProjectPath` 同时看得见「这条路径解析出仓库了吗」与「trace 已有更强项目名吗」。
- **活体验证抓到一次自造回归**：第一版只判「无仓库」，把 `~/.agentmux/scratch/topic--launcher--<uuid>`（真实存在的非 git 目录）也挡了，4 行 UUID 尾巴垃圾名回归。加 `isGenericTemporaryPath`（**既有**函数）限定在临时根下后消失，并补进回归测试第三面。
- **决定性证据**：用生产解析器跑触发本报告的那个真实 transcript，A/B 只差一处——`flowlens-audit-bea26d8c.RjqnPR` → `flowlens`，worktree/branch 保持不变。
- 五道门全绿；UI 无需改动（`ThroughputRiver.tsx` 早已按 `worktrees[].name` 渲染标签）。

**2026-09-19 完成**（mini-sprint `M02_S05.003.FIX` 续，用户「我打包个最新版本, 安装启动一下」后复查发现残留）：

- **装机复查发现第一轮只修了三分之一**。完整分类：(A) 临时沙盒 cwd 顶掉仓库名——已修；(B) codex 的 cwd 以 `file://` URL 形态出现——本轮修复；(C) 已删除且已 git 注销的 worktree——判定诚实不可解。
- **根因 B：`file://` scheme 让仓库边界解析全程 miss**。codex 把工具 payload 里的 cwd 写成 `file:///Users/...`，而 cwd 提取是整行字节扫描（取行内第一个 `"cwd"`），URL 形态因此进入归属链路。`walkRepoBoundary` 对该字符串 `os.Stat(…/.git)` 必然失败，worktree rollup 不发生，`filepath.Base` 拿到 worktree 目录名。**只差 7 个字符的 scheme**：同一路径去掉 `file://` 立刻正确解析为 `flowlens` + worktree + branch。本机 codex 语料 **1422 个文件**含此形态，非个例。
- 修复是 `setTraceProjectPath` 入口一行 `path = localPathFromFileURL(path)`。放这里因为它是**所有 cwd 归属的唯一漏斗**（codex/opencode/grok/extra transcripts 全经此），且必须在 `resolveRepoBoundary` 之前，worktree/branch 才一并恢复。用 `url.Parse` 而非 `TrimPrefix`：要处理 `file://localhost/` 与 percent-encode，并拒绝远端 UNC host 和非 file scheme。
- **活体验收**：新旧二进制同期对比，那个「既作为 worktree 挂在 flowlens 下、又独立成顶层行」的重复行消失，会话并入 `flowlens`（该 worktree 3→5 个会话），无新增回归行。
- **子类 C 判定为诚实不可解并有据**：三个名字的目录、`<main>/.git/worktrees/<name>/`、`.git/logs/HEAD` 三处证据**全部为空**（注销即删除，不留痕）。按「绝不虚构」应保持独立成行——反推归属所需证据已被销毁，猜一个就是虚构。这不是没修完，是**证据边界**。上一轮三次失败正是在强修这一类。
- **本轮方法**：4 路并行探针 + 4 路对抗验证（8 agent）。**四个验证全部推翻了各自的探针**，但价值在于把「一个根因」拆成 A/B/C 三类，并纠正了我看错的字段（`mapping_method` 是会话↔transcript 配对方式，与 `project_attribution_source` 无关——我先前误当成项目名来源）。最终定案由我自己用生产函数实测三行路径完成。
- **踩到一个门禁污染**：某 subagent 遗留 `zz_wide_test.go`（遍历全部 5000+ codex transcript），使 `go test` 从 8s 变 600s 超时，我一度误判为「修复引入 hang」。删除后恢复 8.8s。
- 打包安装 `2026.09.19.173452`（dmg 8.5M / zip 7.9M，ad-hoc 签名），已装 `/Applications` 并实机出数。

**2026-09-19 完成**（mini-sprint `M02_S05.004.VENDOR`，补录另一会话的计划外落地）：

- **vendor 注册表 4 家 → 9 家**：新增 gemini / opencode / hermes / openclaw / pi 的 transcript adapter（`agent_extra_transcripts.go`，858 行）。**这批代码不是本会话编写的**，是另一会话（2026-09-19 01:00–01:53，已结束）留在工作树里的；用户指示「如果是别人修改的，你也提交」。`hermes`/`openclaw`/`pi` 补录前出现在**零个** plan 文档里——属计划外 vendor。
- **机制是一份参数化实现**（`extraTranscriptDiscovery{kind}` 等三个 kind-dispatch 类型）而非五份拷贝，插进与 claude/codex/trae/grok 相同的注册表与接口。新增共享路径：多会话 `ParseSessions() []*SessionTrace`（一个 DB 含多会话）。
- **关键区分——代码档位 ≠ 本机语料**（详表见 mini-sprint §3）。本机实测：**hermes 真实有数（10287 个 trace、1932 个含 output token）**；gemini 仅 1 个文件且解析为 `trace=nil`；openclaw / pi 各 **0** 个数据文件；opencode 根目录不存在。**四家出不了数是正确行为**——`DecodeUsage` 在 `OutputTokens <= 0` 时 `ok=false`、`nonEmptyTrace` 无事件时间即返回 nil，两道闸门闸死。活体快照交叉验证：五家在 `tool_coverage` 档位是真实观测 0，而 `token_usage` / `output_token_throughput` 两条经济档**一个字节都没出**。
- **我阻断提交的判断是错的，已记账（D-019）**。我以「违反绝不虚构」为由拒绝提交，四条理由实测塌了三条：(1) 说 gemini token 字段是发明的——实为 **Gemini API 真实字段名** `promptTokenCount`/`candidatesTokenCount`/`thoughtsTokenCount`，我只读了夹具走的那条分支；(2) 说会显示虚构 0——把「进程档的真 0」当成了「token 档的假 0」；(3) 说 SQLite 必须先拷到 /tmp——`AGENTS.md` 零命中，那是**我给审计子代理下的取证卫生指令**，被我当成产品规则来执法。**「绝不虚构」约束的是有没有把未知渲染成数字，不是有没有数字。**
- **守卫测试反转属实但非放宽**：`TestVendorsWithoutEvidenceDeclareNoTranscriptOrUsageCapability` 名字未变、断言反了（注释已同步）。三条真守卫全在：cursor 仍四槽全 nil、opencode/hermes 仍断言**不得**有 usage decoder、grok 正反两面仍锁。**但测试名已与内容不符**，登记为命名债。
- **已知天花板（不修，记明）**：`isAgentDatabase` 让 DB 每次扫描都重解析（绕过 mtime 缓存，理由正当——mtime 不反映 WAL 提交），本机 hermes 全解析 **6.84s**；今天不付这个钱（`state.db` 距今 9 天，在 7 天地平线外、发现阶段即 deferred），但**活跃 hermes 用户会每次扫描都付**。另有文档能力矩阵手工维护（仓库内无生成器，归入 M02_S01）。
- **测试债如实登记**：7 个新测试的夹具**全部合成**，且 gemini 夹具形状与本机唯一真实文件不一致（夹具 `{timestamp,role,cwd,tokens}` vs 真实 `{kind,$set,content,id}`）。本机无语料，**这笔债无法靠本机取证消除**，只能标注「adapter 就绪 / 本机无数据」，待真实语料出现后重验。
- **审计规模**：8 agent（4 路理解 + 4 路对抗验证），四个 bundle 全部 `refuted: false` / `blockers: []`。SQLite 直开与文档矩阵两条被独立判为 concern 而非 blocker。
- **计划偏差**：M02_S03（opencode）7 项 checklist 全未勾选而代码已落地；M02_S04（gemini）前提被**第二次**推翻（本批接的 `~/.gemini/tmp/**/chats/*.jsonl` 与 M02_S05 查证的 Antigravity protobuf 是**不同证据源、不互相否证**）。两个 sprint 的验收标准均待按已落地现状重写——**不得反向为用例适配代码**。

**2026-09-20 完成**（mini-sprint `M02_S05.005.PERF`，用户「我们是不是没有特地去对 history 做过压缩之类的操作？」）：

- **三个 JSONL 存储改为「2 天热明文 + 按月 gzip 冷归档」**：本机 **109.5MB → 12.26MB（-88.8%），零行丢失**。热文件保持**原名、原格式、原逐行 fsync 追加路径**，压缩只发生在启动压实时的冷段——`appendHistorySampleFile` 与 `lifecycleLog.record` 对每一行 `Sync()`，崩溃最多丢 1 行，这个保证一个字节都没动。
- **先修正两条我自己的误判**：(a) history/throughput **并非无界增长**，它们有 30 天保留，109MB 接近稳态；真正无界的只有 `lifecycle.jsonl`（唯一既无读者也无保留的存储）。(b) **压实只在启动时发生**，进程内长跑期间三个文件都单调增长——本机 2.4 次重启/天使这个节奏可接受，但方案不得依赖运行期压实。
- **月分区让 keep-forever 与有界读取同时成立**：保留窗口 30 天，读者最多开 2 个分区即覆盖全窗口，更早的分区永不再读也永不删除，且**由文件名判定**，无需打开。单一归档文件做不到——它随年份线性变大并在每次启动被整读。
- **否掉了三项指标全胜的方案**（D-020 附带）：往归档追加 gzip 成员体积只差 1%、快 76ms、`Multistream(true)` 可透明读回——但**一个被截断的半成员会让其后所有成员都读不出来**（实测 100 行只读回 28）。改用整月原子重写：慢一点，但复用仓库已有的 temp→`Chmod`→`Sync`→`Close`→`rename` 模式、天然幂等、没有截断态。
- **「原子」不等于「不丢」（D-020）**：跨两个文件的一次逻辑提交，靠三件事压住——先落归档后截热文件（崩在中间是**重复**而非丢失，由既有 `At` 去重吸收）；两次 rename 之间 `syncDir` fsync 目录（`os.Rename` 原子但**不持久**，断电可丢归档 rename 而保留截断 rename）；读分区出错即中止整次压实且热文件保持原样。
- **lifecycle 额外省 20.8MB**：`snapshot_recorded` 行上的两个进程名册在 history 里有信息量更大的一份，删。**`snapshot_aborted` 上的必须保留**——aborted 快照不进 history，那是唯一证据。**对抗评审「逐字节相同」的依据被我实测证伪**（精确匹配 0 命中、±5s 邻近 4620 行内容相同 0 行，实为严格子集少 4 个字段），结论方向对但依据错——照着错依据做会连 aborted 的一起删掉（D-021）。
- **补齐三处既有欠账**：`appendThroughputHistoryRecords` **今天就没有逐行 fsync**，已补 `file.Sync()`；`rewriteThroughputHistoryFile` 补 `Chmod(0o644)`（磁盘上 `throughput.jsonl` 一直是 `0600`，另两个是 `0644`）与 Close/Rename 失败分支的临时文件清理。
- **测试抓出一个真 bug**：`compactHistorySampleFile` 收到的是**归档+热文件合并后**的集合，已归档的行每次压实会被**再归档一遍**，分区无界增长——而 **gzip 藏起了字节，从磁盘大小完全看不出来**。修复是 `writeArchivePartition` 读回既有内容去重。**「预期会红的测试没红」当时是覆盖缺口的信号**：既有夹具的冷集恒为空，归档路径零覆盖（D-021 附带）。
- **验收**：五门全绿；真实数据迁移后**行数守恒经 Python 独立复算逐条吻合**（history -508、throughput -741 均精确等于保留窗口过期数，lifecycle +2 为迁移期间新事件）；65425 行归档 `invalid_json=0`、落在热窗口内 0 行；**重启二次压实 history/throughput 归档字节完全相同**（幂等），lifecycle 增的 6 行经核验是恰好跨过 2 天边界的 heartbeat；9 个文件全部 `0644`。**语义完整性**：`trends.windows` 五个区间全部正常，最长跨至 **2026-08-21**（整 30 天，只可能来自归档）；空闲 CPU **0.3%**。

**2026-09-20 完成**（mini-sprint `M02_S05.006.RSI`，计划文件批次 2）：

- **删掉 agent DB 的缓存旁路**：`transcripts.go` 原本对所有 agent DB **每次扫描强制全量重解析**。git archaeology 给出了准确解释——旁路（`5b81005`，19:27:03）比 WAL 感知的 `agentEvidenceStat`（`43251ae`，19:27:24）**早 21 秒**，它是真修复落地前的占位符，没人回来删。实测 **410ms/次 → 首次 400ms 后 ~9ms（45×）**。
- **验证 WAL 检测时的一个假阳性，值得记**：第一版用两次独立 `sqlite3` 调用，plain stat 与 WAL-aware stat「都检测到了变化」，看似两者等价。**这个结论是错的**——关闭连接会 checkpoint 并截断 WAL，纯 WAL 写入这个场景根本没被构造出来。改用**持久连接**（stdin 管道 + SYNC 哨兵）才测到真实情形：主 db 停在 4096 字节不动，db+wal 从 16488 涨到 24728。
- **删除 4 个 test-only wrapper**：`parseHermesStateDB` / `parseOpenCodeDBTrace` 返回 `traces[0]`，多会话 DB 在测试里永远只被看见第一条，**生产回归会被绿测试掩盖**。测试改为直调注册表解析路径并断言两个会话都回来；**变异测试确认断言有效**（注入 `traces[:1]` 如期失败）。
- **Antigravity 接为 gemini 第二证据根，仅 timeline 档**：实测 44783 条记录、**0 个 token/usage 字段**，所以对 token 指标零贡献。`transcript_full.jsonl` 与 `transcript.jsonl` **逐字节相同**，文件名用精确匹配而非前缀——两个都收会让每个会话计两次。验证 102 文件 → 22087 事件 → **0 条 trace 声称 token**。
- **RSI 面的计划偏差（本 sprint 唯一一处，已记入 D-022）**：计划的头条是「强制重解析计数器」，但**同批次第 1 项已经把那个浪费删了**。再上这个计数器它会**结构性恒为零**——一个永远显示 0 的仪表会被读成「已测量且为零」。该项**放弃**，不是延后。
- **替代品是一个「已测未用」的发现**：`transcriptEvidenceIndexStats` 每次 reconcile 都在算 `Elapsed`/`VisitedEntries`/`PrunedDirectories`/`AgedOutFiles`，一路抵达 `transcripts.go`，**然后全仓库零消费者**。数据一直在算，只是被丢掉。
- **两个诚实性陷阱（D-023）**：(a) 非 reconcile 的 pass 会把三个走查计数**显式清零**，直接呈现会把「本次没测」渲染成「耗时 0ms」；`AgedOutFiles` 例外，它描述索引内容而非走查，两次 pass 都是 9060，不需要守卫。(b) **装机后才发现的更要命的一条**：只报「本次 pass」等于几乎永远不报——索引约每进程只 reconcile 一次，`walk_measured` 稳态下**每次都是 false**。这在诚实性上无懈可击，在实用性上**和恒为零的计数器是同一类废物**。修法：索引保留 `lastWalk` + `MeasuredAt`，`lastStats` 的清零语义原样不动（有测试锁着）；对外 `WalkMeasured`＝有过真实测量，`WalkFresh`＝本次自己走的。
- **`deferred` 与 `aged_out` 必须分成两个 signal**：本机 3474 deferred 对 9100 aged-out。前者在范围内本轮未扫（是缺口），后者在 7 天地平线外（不是缺口）。`evidence_index.go` 的注释早写明了这个区分，但**从未到达界面**——合成一个数会把范围外的量算进覆盖缺口，读起来像故障。
- **验收**：五门全绿；**两处变异测试**均如期变红（去掉 `!WalkMeasured` 守卫实测报出 `Value:0ms`；删掉 `lastWalk` 赋值冷 pass 即失去测量）。装机（2026.09.20.153726）实测：冷启动 `elapsed_ms=319 visited=28555 pruned=936 aged=9100`，随后三次 pass **`walk_fresh=False` 但 `walk_measured=True` 且数值稳定保持**；诊断面 `evidence_out_of_horizon | 9100 files`、`evidence_walk_cost 319ms ok`。空闲 CPU **0.0%**。
- **一次误判，记明**：装机后 5 分钟 `parsed_files=0` + `transcript scan wait cancelled` + 累计 CPU 2 分钟，我一度判定自己引入了 hang 并开始怀疑新加的 Antigravity 目录谓词。实测否掉：gemini 根走查 72ms/23 文件，完整快照 34s 正常完成。真因是**冷启动首扫本来就要几十秒，而我用短超时反复轮询——每次轮询都带自己的 context，超时即取消，于是永远看不到结果**。安静等一次就拿到 78/78。**验证长操作时，超时必须长于操作本身，否则你测的是自己的超时。**

**2026-09-20 完成**（`M02_S01` 部分落地：能力矩阵 code→doc 单端同源）：

- **触发事件不是排期，是一次真实漂移**：Antigravity 接进 gemini adapter 后，`docs/coding-agent-evidence-adapters.md` 的手写能力表**毫无反应**——全仓库没有任何东西比对过代码与文档。这正是 M02_S01 存在的理由，所以先补这一格。
- **注册表成为唯一真相源**：`codingAgentAdapter` 增 `Evidence`（实际读取的磁盘形状）与 `Note`（某槽为何缺席／带什么陷阱），十个 adapter 全部声明。`capability_matrix.go` 直接读四个能力槽渲染表格——**nil 槽一律 `unsupported`，绝不软化、绝不从兄弟槽推断对等**。
- **文档表格改为标记包裹的生成区**，`spliceCapabilityMatrix` 只替换标记之间的内容，**标记缺失时报错而非猜测**（防止把手写散文整体覆盖掉）。
- **三个测试**：doc 与注册表一致性门、nil 槽必须渲染 unsupported、凡解析 transcript 的 adapter 必须声明证据形状（且 gemini 必须声明两个根——专盯「往既有 adapter 加第二个证据根」这个**已经发生过**的失败形状）。
- **变异测试两类都确认有效**：删掉 antigravity 声明 → 两个测试同时变红；给 hermes 塞一个它并不具备的 Usage 槽 → doc 判定陈旧并打印出错误行 `| hermes | … | supported |`。
- **明确未做，不得误读为完成**：7 个信号族**没有对应代码结构**（代码只有 4 个能力槽），生成的是「4 槽 × 10 vendor」而非「7 族 × N vendor」；4 态 cell 未实现，当前只有二元 supported/unsupported（能力槽本身二元，造 partial 会是虚构）；`/api` endpoint 与 dashboard 面板未做。**KR1 只满足「docs 一端 + 代码源」，KR2/KR3 未触及。**
- **这笔账的性质**：4 槽 → 7 族是需要先定义信号族与证据映射的**设计工作**，不是接线工作；在那之前生成 7 列表只会产出无依据的 cell，违反本 sprint 自己的 KR3。
- **打包安装 `2026.09.20.174628`**（dmg 8.5M / zip 7.9M，ad-hoc 签名），已装 `/Applications` 并实机出数：`parsed_files=84`、`scan_cost{walk_measured=true elapsed_ms=512 visited=28223 pruned=942 aged=9202}`、诊断面 `evidence_out_of_horizon 9202 files` 与 `evidence_walk_cost 512ms ok`，空闲 CPU **0.0%**。本次改动是 code→doc 的构建期门，运行时行为按预期与上一版一致。

**当前活跃**：

- **M01 地基**（本轮架构与熵审查修复已落地，发布门已复跑）。
- **当前 sprint：`M01_S01.hardening_release_gate.md`**——落地在途 hardening、把脏工作树收敛为干净提交、四门验证、录基线指标、打 tag。
- M01 后续排队：M01_S02（Go 拆包 + VendorAdapter）、M01_S03（main.tsx 拆分 + popover 快路径）。

**交接**：2026-09-20 会话的收尾盘点见 [`HANDOFF_2026-09-20.md`](HANDOFF_2026-09-20.md)（未完成项按"接手方最可能先碰"排序 + 踩过的坑 + 安全红线）。接手方读完并更新本节后即可删除该文件。

**近期待办（非 sprint 内）**：

- PLAN.md §8 未决问题中标〔用户决策〕的项（Q2/Q6/Q9/Q13）向用户提出。**17 个未决问题目前一个都没关闭**，表格「处理」列写的是计划去向不是结论。
- 建立**吸收巡检仪式**：每个 milestone 出口，审计 Claude Code / Codex / gemini 第一方新出了什么，重新校验受影响 sprint（源于 critic，见 OPINIONS_001 D-006）。

## 3. 文档目录

| 文件 | 用途 |
|---|---|
| `PLAN.md` | 主计划：北极星、用户原话、5W1H、SWOT、品类格局、硬约束、Roadmap、否决项、未决问题 |
| `CURRENT.md` | 本文件：标准与原则、执行状态、文档目录、质检记录 |
| `OPINIONS_001.strategy_decisions.md` | 战略决策记录（来源与语境版本齐备） |
| `HANDOFF_2026-09-20.md` | **一次性**交接单：未完成项排序、踩过的坑、安全红线、常用命令。接手方消化后删除 |
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
| `M02_S03.opencode_adapter_full_evidence.md` | vendor wave 1a：opencode 全证据 adapter（**代码已由 M02_S05.004 落地，验收标准待按现状重写**） |
| `M02_S04.gemini_adapter_conformance_kit.md` | vendor wave 1b：gemini-cli adapter 与 conformance kit（**前提已被实测推翻两次，待按 M02_S05 §7 与 M02_S05.004 §9 改写**） |
| `M02_S05.grok_adapter_full_evidence.md` | vendor 实机落地：grok 满证据；cursor/gemini 诚实分级 |
| `M02_S05.001.FIX.live_session_transcript_resolution.md` | 活跃 session 的 transcript 反解（argv session id → 路径）与 `deferred_files` 口径修正 |
| `M02_S05.002.REDESIGN.activity_concurrency_view.md` | 并发视图重设计：末点虚构 0 的修复、live hero 取代三按钮图例、阶跃线与拆分价格轴 |
| `M02_S05.003.FIX.worktree_project_aggregation.md` | worktree 项目聚合：中途 cd 进临时目录导致同 rank cwd 竞争、项目名被沙盒目录名顶掉；`file://` cwd 让仓库边界解析全程 miss |
| `M02_S05.004.VENDOR.extra_transcript_adapters.md` | 补录计划外落地的 gemini/opencode/hermes/openclaw/pi adapter；**代码档位与本机语料分开记账** |
| `M02_S05.005.PERF.history_archive_compression.md` | 历史压缩：2 天热明文 + 按月 gzip 冷归档（109.5MB→12.26MB，零行丢失）；lifecycle 保留缺口与三处持久性/权限欠账 |
| `M02_S05.006.RSI.scan_cost_and_absorbed_items.md` | 扫描开销诊断面（把索引已测未用的走查数据接上）+ 三个吸收项：删 agent DB 缓存旁路、删 4 个 test-only wrapper、Antigravity 以 timeline 档接入 |
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

| 2026-09-16 | mini-sprint `M02_S05.002.REDESIGN`：并发视图重设计（用户「并发页面应该重新好好设计下」） | `go vet ./...`、`go test ./...`（`ok agentload 31.6s`）、`npm --prefix ui run build`、`node scripts/validate_locales.js`（468 keys × 3 locales）、`./build_macos_app.sh` 全绿。**活体对比**：新二进制（8699）五个 range 的末点字段整体缺省，前一 bucket 仍报真实计数（1D 31）；旧二进制（8642）同期五个末点全为 `session_concurrency: 0` 而机器实有 44 个已知 session。hero 四个字段全部有值（active 3 / known 44 / pids 45 / 今日峰值 18） | 净删 13 文件 +307/-484。教训见 OPINIONS **D-016**：四份提案里三份独立收敛到同一个 Go 修复，实测对真实数据形状**零效果**（`old [19 19 0] → fixed [19 19 0]`，真正跨越 `now` 的 span 0/19）——它们共享同一个错误前提（以为 live span 的 `End >= now`），提案间的一致只衡量前提的共享度，不衡量正确性。有意收窄了 `TestBuildTranscriptTrendWindowsUsesEvidenceAtConfiguredSourceStart` 的一条断言（`len(Points)` → `len(Points)-1`），见 mini-sprint §6 |

| 2026-09-18 | mini-sprint `M02_S05.003.FIX`：worktree 项目聚合（用户「现在的吞吐页面, worktree 好像还没往对应的项目上聚合?」） | `go vet ./...`、`go test ./...`（`ok agentload 7.707s`）、`npm --prefix ui run build`、`node scripts/validate_locales.js`、`./build_macos_app.sh` 全绿。**决定性验证**：用生产解析器跑触发本报告的真实 trae transcript，A/B 只差 `transcripts.go` 一处——修复前 `PROJECT="flowlens-audit-bea26d8c.RjqnPR"`、修复后 `PROJECT="flowlens"`，worktree/branch 两边均为 `flowlens-v2-analysis-cost` / `perf/v2-analysis-cost`。新增回归测试三面锁定（顶不掉更强证据 / 唯一证据时仍报出 / 非临时根的无仓库 cwd 正常归属），已验证修复前必红 | 根因是同一 session 中途 `cd` 进 `mktemp -d` 沙盒，两个 cwd 同 rank、后者按出现顺序胜出。**首版自造回归已在活体对比中抓到**：只判「无仓库」会连带挡掉 `~/.agentmux/scratch/topic--launcher--<uuid>`，4 行 UUID 尾巴垃圾名回归；加既有 `isGenericTemporaryPath` 限定临时根后消失。教训见 OPINIONS **D-017**（共享纯函数不是修复边界）与 §质检步骤库归属类三条 |

| 2026-09-19 | mini-sprint `M02_S05.003.FIX` 续：codex `file://` cwd 破坏 worktree rollup（装机复查发现残留） | `go vet ./...`、`go test ./...`（`ok agentload 8.829s`）、`npm --prefix ui run build`、`node scripts/validate_locales.js`、`scripts/package_macos_app.sh` 全绿，产出 `2026.09.19.173452`。**活体对比**：新旧二进制同期快照，`flowlens-v2-budget-roster-release-exact-260918` 那个「既是 flowlens 的 worktree 子项、又是顶层项目行」的重复行消失，会话并入 `flowlens`（该 worktree 3→5），无新增回归行 | 只差 7 字符的 scheme：同一路径去掉 `file://` 即正确解析；本机 codex 语料 1422 文件含此形态。子类 C（已删除且已注销的 worktree）经三处证据核验（目录 / `.git/worktrees/<name>` / `.git/logs/HEAD` 全空）判定**诚实不可解**，按「绝不虚构」保持独立成行。教训见 OPINIONS **D-018**。**门禁污染**：subagent 遗留 `zz_wide_test.go` 遍历 5000+ transcript，使 go test 8s→600s 超时，一度被我误判为修复引入 hang |

| 2026-09-19 | mini-sprint `M02_S05.004.VENDOR`：补录另一会话落地的 gemini/opencode/hermes/openclaw/pi adapter（vendor 4→9） | `go vet ./...`、`go test ./...`、`npm --prefix ui run build`、`node scripts/validate_locales.js` 全绿；`ui/dist` 哈希与工作树产物一致。**8 路审计**（4 理解 + 4 对抗验证）四个 bundle 全部 `refuted: false` / `blockers: []`。**生产解析器实跑本机真实文件**：hermes `state.db` → **10287 个 trace、1932 个含 output token**（真实可用）；gemini 唯一真实文件 → `trace=nil`；openclaw/pi 各 0 个数据文件 | **代码档位 ≠ 本机语料，分两行记账**（mini-sprint §3）。四家出不了数是正确行为——`DecodeUsage` 在 `OutputTokens<=0` 时 `ok=false`、`nonEmptyTrace` 无事件时间即 nil；活体快照证实五家只在 `tool_coverage` 档位报真实观测 0，`token_usage`/`output_token_throughput` 两条经济档零输出。**我阻断提交的判断是错的**，四条理由塌三条，见 OPINIONS **D-019**。已知天花板：hermes 全解析 6.84s 且 DB 有意绕过 mtime 缓存（本机因 9 天未动而 deferred，活跃用户会每次付）；文档矩阵无生成器（归 M02_S01）；7 个新测试夹具全合成且 gemini 夹具形状与真实文件不符（测试债，本机无语料无法消除）。打包安装 `2026.09.19.192831`（dmg 8.5M / zip 7.9M，ad-hoc 签名），已装 `/Applications`；实机验证 `current_by_tool` 九家齐备，五家新 vendor 在 coverage 档位报真实 0、经济档零输出 |

| 2026-09-20 | mini-sprint `M02_S05.005.PERF`：三个 JSONL 存储改为 2 天热明文 + 按月 gzip 冷归档（用户「我们是不是没有特地去对 history 做过压缩之类的操作？」） | `go vet ./...`、`go test ./...`（`ok agentload 7.846s`，新增 8 个测试）、`npm --prefix ui run build`、`node scripts/validate_locales.js`、`./build_macos_app.sh` 全绿。**真实数据迁移**（备份 `/tmp/agentload_backup_pre_install_20260920_103529`，73534 行 / 109.5MB）：109.5MB → **12.26MB（-88.8%）**，行数守恒经 Python 独立复算**逐条吻合**——history 热 514 + 归档 4199 = 4713，差 508 精确等于保留窗口过期数；throughput 差 741 同样精确吻合；lifecycle +2 为迁移期间新写入事件。归档 65425 行 `invalid_json=0`、落在热窗口内 **0** 行。**重启二次压实**：history/throughput 归档**字节完全相同**（幂等成立），lifecycle 增 6 行经核验是 09-18 10:35–10:40 恰好跨过 2 天边界的 heartbeat。9 个文件全部 `0644`（迁移前 `throughput.jsonl` 为 `0600`）。**语义完整性**：`trends.windows` 五区间全部正常，最长跨至 **2026-08-21**（整 30 天，只可能来自归档）；`/api/refresh` 后 `parsed_files=80`，空闲 CPU **0.3%** | 写路径一个字节未动（逐行 `Sync()` 的崩溃丢 1 行保证保持不变）。**否掉了三项指标全胜的 gzip 追加成员方案**——半成员污染其后所有成员（实测 100 行只读回 28），改整月原子重写（D-020 附带）。**测试抓出真 bug**：已归档行每次压实被再归档，分区无界增长且 **gzip 藏起字节从磁盘看不出来**；「预期会红的测试没红」是覆盖缺口的信号（D-021 附带）。**对抗评审「逐字节相同」依据被实测证伪**（实为严格子集），结论方向对但照错依据做会连 `snapshot_aborted` 的唯一证据一起删（D-021）。教训见 OPINIONS **D-020**（原子 ≠ 不丢）与 **D-021**（冗余判定必须自己比对字节） |
| 2026-09-20 | mini-sprint `M02_S05.006.RSI`：删 agent DB 缓存旁路 + 删 4 个 test-only wrapper + Antigravity 接为 timeline 档 + 扫描开销诊断面（计划文件批次 2） | 五门全绿（`go vet ./...`、`go test ./...` `ok agentload 11.174s`、`npm --prefix ui run build`、`node scripts/validate_locales.js` 474 keys × 3 locales、`./scripts/package_macos_app.sh`）。**两处变异测试均如期变红**：去掉 `scanCostValue` 的 `!WalkMeasured` 守卫实测报出 `Value:0ms`；删掉 `index.lastWalk = index.lastStats` 冷 pass 即失去测量。**装机实测**（2026.09.20.153726）：冷启动 `walk_measured=true elapsed_ms=319 visited=28555 pruned=936 aged=9100`，随后三次 pass **`walk_fresh=False` 但 `walk_measured=True` 且数值稳定**；诊断面 `evidence_out_of_horizon \| 9100 files`、`evidence_walk_cost 319ms ok`。缓存旁路删除后 agent DB 解析 **410ms/次 → 首次 400ms 后 ~9ms（45×）**。Antigravity 102 文件 → 22087 事件 → **0 条 trace 声称 token**。空闲 CPU **0.0%** | **计划偏差一处（D-022）**：计划头条「强制重解析计数器」被同批次第 1 项消灭，再上会**结构性恒为零**，故放弃而非延后。**装机后才暴露的第二个陷阱（D-023）**：只报「本次 pass」的走查开销在稳态下每次都是 false（索引约每进程只 reconcile 一次），诚实但无用——改为保留 `lastWalk` + `MeasuredAt`，`WalkMeasured`/`WalkFresh` 分开表达。**一次误判**：冷启动 `parsed_files=0` 被我当成自己引入的 hang 并开始怀疑 Antigravity 谓词，实测否掉（gemini 根 72ms/23 文件，完整快照 34s 正常）——真因是**短超时反复轮询，每次轮询自带 context，超时即取消**。教训见 OPINIONS **D-022**（恒为零的指标不叫诚实）与 **D-023**（只报本次等于几乎不报）|
| 2026-09-20 | `M02_S01` 部分落地：能力矩阵 code→doc 单端同源（触发事件是同日 `M02_S05.006.RSI` 造成的一次真实文档漂移） | 五门全绿（`go vet ./...`、`go test ./...` `ok agentload 8.767s`、`npm --prefix ui run build`、`node scripts/validate_locales.js`、`./scripts/package_macos_app.sh` 产出 `2026.09.20.174628`）。装机实测 `parsed_files=84`、`scan_cost elapsed_ms=512 visited=28223 pruned=942 aged=9202`、诊断面两条信号在位、空闲 CPU 0.0%。**变异测试两类均如期变红**：删掉 gemini 的 antigravity 证据声明 → `TestCapabilityMatrixDocMatchesTheRegistry` 与 `TestCapabilityMatrixDeclaresEveryEvidenceRootTheParserReads` 同时失败；给 hermes 注入它并不具备的 Usage 槽 → doc 判定陈旧并打印 `\| hermes \| … \| supported \|`。生成表比旧手写表**多出** `Evidence discovery` 列、每个 adapter 的磁盘路径形状、以及旧表里根本不存在的 **antigravity 根** | **漂移是实测到的而非假设的**：Antigravity 接入后手写表毫无反应，因为全仓库没有任何东西比对代码与文档。**nil 槽一律渲染 `unsupported`**，绝不软化也绝不从兄弟槽推断。`spliceCapabilityMatrix` 在标记缺失时**报错而非猜测**，避免把手写散文整体覆盖。**明确未做**：7 信号族无对应代码结构（只有 4 能力槽）、4 态 cell 未实现（槽本身二元，造 partial 即虚构）、`/api` 与 UI 面板未做——KR1 只满足 docs 一端，KR2/KR3 未触及 |

**质检步骤库（随 sprint 验收累积）**：

- 目前基础步骤 = §1.2 常设门六项。M01_S01 完成后追加：基线性能指标复核（空闲 CPU %、popover 打开 ms、snapshot p95 ms、二进制 MB，测量方法须记录在案）。后续每个 sprint 验收通过时，把其量化验收中可复用的检查项追加到本节并注明来源文件。
- **趋势/并发类改动追加（来源 `M02_S05.002.REDESIGN`）**：任何触及 trend 序列的改动，验收必须包含一次**新旧二进制同期快照对比**——`go:embed ui/dist` 意味着在跑的进程永远拿不到新产物，只看单元测试会漏掉序列化层。对比时逐点核对「字段缺省 vs 值为 0」，二者在 JSON 里长得不一样但在图上都容易被读成「没有负载」。
- **归属类改动追加（来源 `M02_S05.003.FIX`）**：
  1. **活体快照对比前先确认触发样本仍在活跃窗口内**。本次第二轮 A/B 两边行集完全相同，不是修复失效而是触发 session 的 `last_event_at` 已滑出窗口——**「两边一样」既可能是修好了也可能是测空了**。样本已过期时，改用生产解析器直接跑那个真实 transcript 做 A/B。
  2. **归属逻辑收紧后必须做一次全量项目行 diff**，不只看目标那一行。本次第一版把 `~/.agentmux/scratch/topic--launcher--<uuid>` 一并挡掉，代价是 4 行 UUID 尾巴垃圾名回归——只盯 `flowlens` 一行看不出来。
  3. 断言「某路径应被拒绝」的测试，**输入必须取自真实布局**（沿用 D-008）；同时补一条「唯一证据时仍须报出」的反向断言，防止把「不许顶掉更强证据」写成「一律无效」而丢信息。
- **新 vendor / 证据档位类改动追加（来源 `M02_S05.004.VENDOR`）**：
  1. **区分「代码档位」与「本机语料」，分两行记**。adapter 声明 Usage 槽 = 「能解析这种格式」；本机有没有数由 nil 链路表达为缺省。验收表必须两列并置（声明档位 / 本机实测），否则读者会把「本机没装这个工具」误读成「adapter 坏了」。
  2. **说「违反绝不虚构」之前走完三步**（D-019）：(a) 找到该数字**实际序列化**的位置（omitempty？指针？`ok=false` 早退？）；(b) 从**活体快照**确认它现在出不出数、出在哪个档位——`tool_coverage` 的 0 是真实观测，`token_usage` 的 0 才是虚构；(c) 确认所引规则**在仓库里**（`AGENTS.md` / docs），不是在此前对话或我给子代理的指令里。三步任一没走就阻断，代价是把正确实现判成违规。
  3. **新 adapter 必须用生产解析器跑一遍本机真实文件**，而不是只看夹具绿。本轮 hermes 跑出 10287 个 trace（真实可用），gemini 跑出 `trace=nil`（本机语料形状与夹具不符）——**两个结论都只能这样得到**，夹具全绿时两者看起来一样。
  4. **DB 类证据源要量一次全解析耗时并判断是否落在扫描热路径上**。`isAgentDatabase` 有意绕过 mtime 缓存（mtime 不反映 WAL 提交），代价是每次扫描重解析；本机 hermes 6.84s 但因 9 天未动而 deferred，**活跃用户会每次都付**。记明天花板与升级路径，不要因为「本机不痛」就不记。
  5. **测试名被反转时必须同步改名**。本轮 `TestVendorsWithoutEvidenceDeclareNoTranscriptOrUsageCapability` 断言反了而名字未改，留下「读名字得到相反预期」的陷阱；注释改了不算够。
- **存储/压实类改动追加（来源 `M02_S05.005.PERF`）**：
  1. **动真实数据前先备份，迁移后做行数守恒的独立复算**。不是「看起来少了一些」，而是**用另一种语言/工具把该丢的行数单独算一遍**，两个数字必须精确相等。本轮 history -508、throughput -741 均由 Python 独立复算逐条吻合；只要差一行就说明有丢失或重复。注意 `at` 带 `+08:00` 偏移，字符串比较会错 8 小时；且 Go 写出的 5 位小数秒会让 Python 的 `fromisoformat` 报错——**复算脚本本身先要能解析全部行（unparsable 必须为 0）**，否则算出来的是脚本的 bug 不是数据的账。
  2. **压实类改动必须验幂等：连跑两次，归档字节应当完全相同**。本轮 history/throughput 二次压实后归档字节一致；lifecycle 增的 6 行经核验是恰好跨过热窗口边界的 heartbeat（正确行为）。**跨边界的增量与重复归档在行数上长得一样，必须看时间戳落在哪个带**。
  3. **压缩会藏起证据，缺陷要在解压后的行上验**。本轮真 bug（已归档行每次压实被再归档）在磁盘大小上**完全看不出来**——gzip 把重复内容压掉了。归档类断言一律对 `gunzip -c` 后的行数与内容做，不对文件大小做。
  4. **「预期会红的测试没红」先当覆盖缺口查，不要当好消息**。本轮既有夹具把过期行放在保留窗口外、保留行放在热窗口内，**冷集恒为空、归档路径零覆盖**，所以接上归档后全绿。新路径落地时若既有测试毫无反应，先确认它们是否**根本没走到新路径**。
- **诊断/指标上线类改动追加（来源 `M02_S05.006.RSI`）**：
  1. **上线一个指标前，先问它在稳态下取什么值**。诚实不等于有用：一个「结构性恒为零」或「结构性恒为空」的指标会被读成「已测量且结果为零」，比不上线更糟。本轮两次踩到同一形状——计划的重解析计数器（被同批修复消灭）与第一版走查开销（索引约每进程只 reconcile 一次，稳态恒为未测量）。**判据是「装机后连看三次稳态快照，它出数吗」**，不是「单元测试里它能出数吗」。
  2. **`Has*` / `*Measured` 守卫必须做变异测试**。把守卫改成 `if false` 或删掉赋值，确认测试如期变红。本轮两处守卫都是这样确认的；不做这一步，一个永远为真的守卫和一个正确的守卫在绿测试下完全一样。
  3. **区分「描述本次动作」与「描述当前状态」的字段，前者要守卫后者不要**。本轮 `Elapsed`/`VisitedEntries`/`PrunedDirectories` 描述走查（非 reconcile 时被显式清零，必须守卫），`AgedOutFiles` 描述索引内容（两次 pass 都是 9060，不需要守卫）。混在一起会要么虚构零、要么把真数据藏掉。
  4. **本身语义不同的两个量不要合成一个数**。`deferred`（在范围内、本轮未扫，是缺口）与 `aged_out`（在地平线外，不是缺口）本机是 3474 对 9100，合并会把范围外的量算进覆盖缺口，读起来像故障。
  5. **验证长操作时，超时必须长于操作本身**。本轮冷启动首扫要几十秒，我用短超时反复轮询——每次轮询自带 context，超时即取消，于是永远看不到结果，进而误判为自己引入了 hang。**你测的是自己的超时，不是被测对象。**
- **能力/证据声明类改动追加（来源 `M02_S01` 部分落地）**：
  1. **改了 adapter 能力槽或证据根，验收必须含 `go test . -run TestCapabilityMatrixDocMatchesTheRegistry`**。这条现在是发布门：注册表是唯一真相源，文档里的表从它生成。有意变更时用 `UPDATE_CAPABILITY_MATRIX=1` 重新生成再复核 diff，**绝不手改生成区**。
  2. **往既有 adapter 加第二个证据根时，`Evidence` 必须同步声明**。这正是本轮触发漂移的形状（Antigravity 进了 gemini 解析器却没进任何表）。`TestCapabilityMatrixDeclaresEveryEvidenceRootTheParserReads` 专盯这个。
  3. **新能力槽上线前先做两类变异**：删掉一条证据声明（应点红 doc 陈旧 + 证据根缺失两个测试）、给一个不具备该能力的 adapter 注入槽（应打印出错误行）。**没红过的门等于没有门。**
  4. **`unsupported` 是结论不是待办**。复核生成表时逐行确认每个 `unsupported` 都有实测理由（记在 `Note` 或 docs 散文里），不能让「还没做」和「实测没有」在表上长得一样。
