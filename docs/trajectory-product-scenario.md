# Trajectory 知识工作区

## 目标体验

用户要求"界面设计不够优雅, 应该更加的简约, 直击要害"，并希望先在 Popover
做出最终场景，锁定长期目标。首屏是会话搜索与命中原句，点击直接打开前后文；
需要时再打开 Context、知识记录和局部关系图。

用户从一个名词、工具或 Skill 找到以前的执行经历，读懂当时的尝试和结果，
了解经验的适用条件，并回到支持或限制它的原始证据。轨迹自然积累为证据库，
可复用经验仍需提炼与验证。没有命中、没有来源和没有足够覆盖分别表达。

## 一条完整路径

以再次排查 Proxy 为例：

1. 打开“知识”，输入 `Proxy`、`tool:exec_command` 或 `skill:proxy-debugger`。
   结果来自本地服务；同一 Session 的多个命中聚为一条会话，显示实际匹配数。
   命中片段是结果主体，关键词高亮；会话标题与 Agent 为辅助信息。
2. 选择命中，默认在“过程”打开该记录及前后文，不先展示与命中无关的最后动作。
   “详情”和“关系”保留进一步查看入口，工具回执成功不显示成任务成功。
3. 在“过程”阅读请求、调用、回执和摘要，展开行号、offset、digest、event ID
   或原始字节。片段超过预算时提供前后续读，不假装完整历史。
4. 读 Context 时区分 archive、actual_input、workspace 和 query_window。
   历史里存在约束不等于某次模型实际收到它；输入成员没有证据时显示 unknown。
   压缩里程碑可回到前后观察分段。
5. 按需打开“关系”。图围绕当前真实记录，边保留原生类型、来源及解析状态。
   选择节点回到同一个 canonical event；越窗、缺失及歧义边可见。
6. 查询 `in:knowledge kind:candidate` 或 `in:knowledge kind:counterexample`，
   查看限定经验、来源、提取规则和支持/反证。`state:withdrawn` 是记录生命周期，
   candidate 是知识种类；它们不是同一个字段。
7. 返回经历列表保留查询；不存在的词显示无匹配与覆盖范围，不保留旧结果冒充命中。

示例条件说明操作方式，不保证机器上存在相应记录。生产界面不混入硬编码经历。

## 交互契约

- 入口：底部“知识”位于“系统”之后、“诊断”之前。默认 operational 视图是吞吐。
  直接地址为 `/?view=knowledge&lang=zh`，详细内容仍需当前实例凭据与显式启用。
- 内容访问关闭时显示明确启用入口；没有凭据时引导从本地应用打开。
  本地标注关闭期间不可读取，已有笔记的保留规则见 [隐私](privacy-local-observation.md)。
- 搜索编译成同一 Go 服务选择器；帮助展示支持的条件，未知字段与不支持组合报错。
- 首屏只放问题、行动和相关记录，不堆实体分类、全图数量与术语说明。
- 普通词搜索不要求掌握选择器语法；高级条件收起，检索覆盖历史归档。
  默认快速返回准确命中页，不为全库计数等待；每会话匹配数与命中原文身份保持精确。
  尚在建立索引时显示完成索引会话数 / 已知会话数，并显示当前已返回记录数。
  默认总数留空，包括无文字列表；不显示 0，也不把当前页数量称为总数。
  分页下一页只能来自已验证的后续命中。
  零命中不能冒充完整无匹配；回溯由后台推进，不要求搜索页一直打开。
- 项目归属只来自已观测证据；只有 command hint、没有解析日志或缺少 cwd 时，
  保留未归属状态，不根据名称、时间邻近或其他会话补造项目。
- 阅读详情时暂停列表的自动查询刷新；返回列表保留查询并重新读取结果，
  请求失败不能留下无法恢复的空白加载状态。历史回溯继续由后台服务推进。
- 选择、查询与来源保持一致；请求取消、旧响应和分页不会覆盖新的选择。
- 键盘支持搜索、Enter 选择、Escape 清除、标签切换、证据和返回。
- 图按需加载，稳定有界，不持续运行力导向布局；状态有文字表达，不能只靠颜色。
- en/zh/ja、深浅主题和 430px 窄 Popover 可用，无横向溢出。
- 原文当作文本显示，不执行 HTML、命令或外部链接抓取。
- 资源、movement、Token 和公开诊断的语义保持独立。

## 给 Agent 的使用方式

CLI 与 Popover 通过 loopback JSON-RPC 消费同一服务。最小流程是 query 找 ID、
get 读有界证据、watch 跟踪新观察；明确沉淀经验时 annotate 写本地记录。

```bash
agentload traj access on
agentload traj query sessions --text Proxy --format json
agentload traj query events --tool exec_command --format json
agentload traj query sessions --text Proxy --count --format json
agentload traj query events --tool exec_command --count --format json
agentload traj query events --skill proxy-debugger --predicate mention --format json
agentload traj get EVENT_ID --around 3 --raw --format json
agentload traj query contexts --context-scope actual_input --format json
agentload traj query knowledge --kind candidate --format json
agentload traj query attention --kind waiting_permission --format json
agentload traj watch events --cursor WATCH_CURSOR --format ndjson
```

ID 和游标来自服务，`WATCH_CURSOR` 使用 query 返回的 watch_cursor；它不同于
分页 next。Scope 和 predicate 有各自语义，原文中的提及不证明实际加载。
所有具体 flag、预算和错误见 [API Reference](api-reference.md)。

需要精确总数时显式使用 `--count`，对应 RPC 查询选择器 `count: true`。
`matched_total` 只计算授权且已准备范围内的精确命中；未追赶完成的 coverage
仍然是不全。默认请求（包括无文字列表）省略该字段，不能按 0 处理。计数模式绑定分页
cursor，切换模式应重新 query。普通请求服务端/CLI 上限为 6/7 秒，显式计数为
单条请求 60/61 秒且可取消；JSON-RPC batch 仍共享服务端总计 6 秒期限并拒绝
`count: true`，计数须单独发送。默认 sessions/events 都只分页核验至足够命中
和已验证 lookahead；每会话匹配数仍精确。计数模式与超时/取消机制正在实现，
本页不宣称已经验收。

Agent 可据此检索旧尝试、定位失败、准备交接、消费来源变化或向外部改进器提供
诊断证据。当前配置、权限、产物与适用条件需要重新核验；读取旧日志不授权执行。

## 知识记录与限制

知识记录 kind 分为 observation、candidate、verification、counterexample；
state 分为 active、withdrawn。原生来源状态、记录状态和验证适用范围分开。
验证/反证是独立记录；一次成功不让候选自动成为通用结论。

自动候选保留确定性规则版本与绑定的行动/回执，明确因果和任务完成未证明。
显式笔记可补充当时未记录的判断及 configuration、environment、version、
evaluation_refs、artifact_refs；未提供的值留白，不用当前文件补历史事实。
来源被修改或撤回时，派生候选消失或读取失效，笔记显示来源缺口。

局部关系图表达已记录的关系，不提供全量推理式 ontology。完整模型输入、
历史 Git Diff/产物快照、任务级 Review Digest、实验回放和全量 Dashboard 图谱
属于单独的未来范围。

用户已接受额外 Trajectory 索引百 MiB 级的目标。目标布局改为轻量来源目录、
断点与解析状态、稀疏定位、有界候选索引及不可再生例外；完整轨迹从源文件按需
重建。原有持久化全部 normalized DTO、正文及每事件 FTS 是待迁移布局。
百 MiB 级尚未达成，真实全库容量与普通查询 6 秒速度仍需验证。

用户阅读到的事件 ID/generation、Context、实体同一 occurrence、配对、关系、
coverage 和显式空值语义保持不变；按需重建不能只展示原始一行冒充完整轨迹。
候选索引只减少读取，返回命中仍精确核验；短词、Unicode 和 NUL 不得漏报。
默认准确分页、显式 count 才计总数的查询契约不变。

源 sessions、历史、usage 与用户标注不计入新增索引预算，绝不清理来达标。
迁移按用户授权的“蚂蚁搬家”方式进行：一份小影子库，按来源/小批复制与独立
核验，持久记录进度；失败可恢复，全部来源与逻辑身份/恢复状态核验后原子切换，
再释放旧布局。分批处理不承诺每批物理缩小旧文件；不能提前删旧库，不保留长期
双后端或 fallback。峰值包含旧库、小影子、临时数据及 VM 余量，沿用至少 1 GiB
可用空间保护。空间不足时保留数据与断点、明确暂停；查询可用不证明追赶完成，
体积须附覆盖范围，不能把部分索引称为整库终态。

## 验收与跟踪

当前 Feature 为 `trajectory-knowledge`（`f-22duuagpj`），实施与验证状态由
`.bagakit/feature-tracker/` 的 JSON 管理。本页不复制任务状态。

验收路径包含真实 query → 经历 → 详情/过程/Context/关系/知识 → 原始来源 → 返回；
工具与 Skill 条件、限定经验和反证、来源失效、权限关闭、键盘、三语言、
深浅主题及窄窗口均须经过真实服务夹具验证。生产构建通过 locale 与 Go gates。
按用户要求，去掉需要人手工配合的测试门禁，不以用户手工开合 Popover 或计时
作为交付前提。当前环境不能自动测试原生开合/首绘，诚实记录为未验证，不能
记作通过；≤150ms 产品目标仍保留。空闲 CPU 达标也需要真实应用测量。
网页通过不替代原生性能证据。可自动执行的数据真实性、查询和存储保护验收
继续保留；查询可用、安装成功或空间保护正常暂停，都不证明追赶与增长验收完成。
