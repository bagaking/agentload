<!--
meta:
  目的: 战略决策记录——项目方向层面的决策、取舍与被接受的质疑，含来源（用户洞见 / 迭代结论）与产生时的语境版本，保证决策可追溯。
  日期: 2026-07-26
  来源: 用户 2026-07-26 原话（逐字见 PLAN.md §1）；judge 三案合议；critic 复核；当日仓库审计。
-->

# OPINIONS_001 — 战略决策记录

> 记录格式：每条含**来源**（用户提出的洞见 / 迭代中积累的经验）与**语境版本**（产生时的 sprint 阶段并引用）。话题：项目战略与方向。架构/实现层决策后续另开 OPINIONS_002+。

## D-001 北极星由用户设定：品类内最全面、最舒服、作用最大

- **来源**：用户洞见（2026-07-26 原话，逐字与解读见 PLAN.md §1.1，含「做舒服→最舒服」笔误判定）。
- **语境版本**：pre-M01（2026-07-26，计划制定阶段，尚无 sprint）。
- **内容**：三个最高级被操作化为可度量定义与五大支柱（PLAN.md §0），并由「绝不虚构」不变式统一。任何后续功能议案先问「推进哪个最高级、是否破坏不变式」。

## D-002 先加固后功能（hardening-before-features）

- **来源**：迭代结论。用户同日审计指令（PLAN.md §1.2）触发全量审计，审计发现（平铺 root Go package、~3,000 行 main.tsx、脏工作树、文档漂移）与 judge 合议一致：在现有地基上建 attention 引擎或新 adapter 会放大所有估算。
- **语境版本**：pre-M01（2026-07-26）。落地为 `M01_S01.hardening_release_gate.md`。
- **内容**：在途 hardening 收敛为打 tag、带性能基线的发布门；M01 期间**功能合入冻结**；基线指标（空闲 CPU、popover 打开时长、snapshot p95、二进制体积）录档作为未来 CI perf budget 参照。

## D-003 结构重构为一切后续工作的地基

- **来源**：迭代结论（三份独立战略提案共同收敛于同一骨架，judge 视共识为正确性证据）。
- **语境版本**：pre-M01（2026-07-26）。落地为 `M01_S02.go_package_split_vendor_adapter.md`、`M01_S03.main_tsx_split_popover_fast_path.md`。
- **内容**：Go 按边界拆包（observer/transcripts/history/semantics/server/shell/config）；定义 VendorAdapter 接口 + 每 adapter 能力声明（后者是 M02 Coverage Matrix 的代码级数据源）；main.tsx 拆模块并让 popover 关键路径剥离 trend/diagnostics 包。验证手段：golden snapshot 保证重构前后 `/api/snapshot` 字节可比；语义层 guard 由 CI 强制。

## D-004 排序规则：不变式先行、attention 前移

- **来源**：迭代结论（judge 合并规则）。
- **语境版本**：pre-M01（2026-07-26）。体现于 PLAN.md §6 的 M02→M03 排序。
- **内容**：(a) 三案均以 transformative/high 提名的项在依赖就绪后最早排期（attention 引擎、vendor wave 1、能耗预算）；(b) **不变式类（Coverage Matrix、perf gate）先于其所门禁的工作出货**——事后补装等于重审一切；(c) Delight 案把 attention 引擎推后于舒适打磨被否，attention 是品类 2026 重心与后续一切（分诊/通知/看门狗/digest/glyph/自动化）的平台原语，故前移至 M03，舒适分摊到各里程碑。

## D-005 关键否决（top rejected ideas）

- **来源**：迭代结论（judge 裁定；完整清单与措辞见 PLAN.md §7，此处只记最能定义产品身份的四条及其原理）。
- **语境版本**：pre-M01（2026-07-26）。
- **内容**：
  1. **外推配额预测**被否——品类已验证的信任杀手（ccusage #298/#483、CCUM #48/#52）；只做「观测 burn + 厂商本地写入的 reset + 用户自报限额」的诚实变体。原理：信任是唯一护城河，一次虚构即破产。
  2. **自动 kill**被否——断路器永远是人；观测者身份不可越界。
  3. **远程 relay**被否——loopback 纯净不可协商；用 exec-on-event + 本地 SSE 让用户自建，信任决策归用户。
  4. **vendor wave 2 里程碑化**被否——广度当 checkbox 是品类致命模式（91x token 虚报、~48 open issues 维护塌方）；conformance kit 证明成本后按需逐落。
  5. 另记**常设反目标**：不编排、不启动、不 diff review、不管理 MCP（opcode 第一方绞杀教训）——供 review 时拦截 scope creep。

## D-006 critic 挑战被接受为未决问题（而非直接改计划）

- **来源**：迭代结论（critic 复核；逐条内容与处理方式见 PLAN.md §8，SSOT，此处不重抄）。
- **语境版本**：pre-M01（2026-07-26）。
- **内容**：17 项挑战全部接受入档为 PLAN.md §8 未决问题表，按三类处置——〔用户决策〕Q2（OAuth usage 端点是否允许单次显式同意的外发）、Q6（LICENSE 与商业化）、Q9（locale 集合）、Q13（预发布渠道是否前移至 M02 出口）；〔spike/调研〕Q1（sandbox 案头调研前移 M02）、Q4（容器/远程执行位置）、Q11（FSEvents 事件通道先 spike 后锁架构）；〔计划修订〕Q3（M04_S02 改为跨厂商统一配额视图）、Q5（HTTP 攻击面为 stop/exec 端点硬前置）、Q7（负载门与磁盘预算）、Q8（TCC 权限映射）、Q10（商标审查）、Q12（格式漂移金丝雀与专项 fixture）、Q14–Q17（对应 sprint 验收改写）。
- **理由**：这些挑战多数指向「计划的假设未经验证」而非「计划错误」；在 M01 冻结期内以问题表管理、在触及对应 sprint 前解决，优于当下推翻排期。每项关闭时须回 PLAN.md §8 标注去向并更新 CURRENT.md。
- **另记（吸收巡检）**：critic 指出品类功能半衰期以月计（/usage 已吸收单厂商配额显示）。决定建立**吸收巡检仪式**：每个 milestone 出口审计三家第一方新船了什么、重校验受影响 sprint——已列入 CURRENT.md §2 待办。
