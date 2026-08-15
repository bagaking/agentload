# Agent Load Metric Semantics

## Agent Load 自身资源

诊断必须监控 Agent Load 自己，并支持按功能和文件下钻。自身读数独立于
Agent 进程汇总和整机资源，不能加入 Agent 的 CPU、内存或吞吐总数。

- CPU：当前 Go 进程累计 user + system CPU 时间在两个实际采样间的差值，
  除以真实墙钟间隔；100% 表示一个核心，允许超过 100%。首次采样和读取
  失败为未知，不能拿 `ps` 的平均值代替区间值。
- 内存：操作系统当前进程 resident bytes（RSS），不是 Go heap、缓存预算
  或整机内存；不包含独立 WebKit 子进程。功能共享进程内存、CPU，未单独
  测量时必须留空，不按文件大小或执行耗时分摊。
- 存储：只统计运行时拥有的本地文件，分别保留逻辑长度和 `st_blocks * 512`
  已分配字节。主读数使用后者；APFS 共享块的独占物理空间未测量。不将源
  session、应用安装包、开发构建缓存、研究材料或系统 swap 计入运行数据。
- 功能下钻：轨迹目录、吞吐索引与吞吐历史、资源与生命周期历史、其他运行
  文件；每个文件只归属一项。只读取文件元数据，不打开活跃数据库，不跟随
  符号链接。扫描不完整时总数留空，并保留已测量的部分及缺口。
- 自身监控模块：作为独立监控项，展示最近采样的实际墙钟耗时；自身采样
  不写文件，新增持久化字节为零。此耗时不是 CPU 时间，不冒充模块 CPU
  或 RSS。采样结果只保存在内存中。
- 进程读数最多每两秒采集一次；存储最多每分钟扫描一次。诊断读取返回
  缓存快照，携带各自采样时间；界面关闭即停止轮询，不增加后台全库分析。

This page is the contract for Agent Load's metric matrix. Backend aggregation
and frontend rendering should route through semantic helpers instead of mixing
raw snapshot fields inline.

Data truth is the first product standard. Visual design, compact density, and
interaction convenience must serve the semantic source of truth; they must not
make a number look active, current, complete, or matched unless the metric
semantic layer says so.

## Semantic Matrix

| Semantic | Snapshot field family | Evidence source | Must not include |
| --- | --- | --- | --- |
| Recent movement | `active_burst_*`, `active_sessions`, project/tool `active_burst_count` | A local transcript/activity-log event whose age is within `config.idle_gap_seconds` | Visible processes that did not write a recent local event |
| Known sessions | `session_concurrency`, `session_count`, `main_agent_sessions`, `subagent_sessions`, `unknown_role_sessions` | Observed session evidence from transcripts, command hints, or fallback session ids | Process-only rows with no mapped session evidence |
| Process pressure | `pid_concurrency`, `process_count`, `mapped_processes`, `unmapped_processes`, `multi_mapped_processes` | Visible local AI process rows and their session mappings | Transcript-only sessions without visible processes |
| Process resources | `cpu_percent`, `memory_bytes`, disk I/O counters/rates, process elapsed values | Visible process table and local per-PID process counters, summed only for matched process ids when shown at project/session scope | Token usage, transcript movement, or session count |
| System resources | `system_resources` | Whole-machine OS counters sampled independently from transcript scanning, plus public macOS thermal-pressure state when available | Session activity, project attribution, exact hardware temperature/fan readings, or per-PID network guesses |
| Role matrix | `main_agent_sessions`, `subagent_sessions`, `unknown_role_sessions` and active role splits | Session role inference from thread source, parent thread, lane paths, and independent-run evidence | Process role guesses without mapped session evidence |
| Tool coverage | project/tool `session_count`, `active_burst_count`, `process_count` | Per-tool aggregation of known sessions, recent movement, and process pressure | Treating process pressure as recent movement |
| Token usage | `token_usage`, `token_usage_source`, `token_usage_confidence` | Parsed local transcript usage fields when present, including cumulative token-count events when the local trace exposes them | Inferring usage from process duration, CPU, memory, or elapsed time |
| Output token throughput | `/api/live-token-rate` `output_tokens_per_second`, `projects`, `state`, `window_seconds`; minute facts and derived series in `throughput_trends` | Positive output-token events and safe cumulative-output counter deltas from local transcripts, stored in non-overlapping closed-minute facts and attributed through the observer's project mapping | Input/cache/reasoning tokens, process activity, model decode speed, API-active-time throughput, replayed deltas across collection gaps, or averaging previously derived rates |
| Runtime telemetry | `runtime_telemetry` | Optional local adapter state for future OpenTelemetry or JSONL events | Replacing local process/session evidence or treating unconfigured telemetry as failure |
| Diagnostic export | `diagnostics.export` and `/api/diagnostic-export` | Sanitized local snapshot with omitted private fields documented | Raw prompts, absolute paths, full command arguments, environment variables, transcript paths |

## Rules

- `active_burst_count` means recent local-log movement, not "currently has a
  process".
- A mapped visible process can make `process_count`, CPU, and memory non-zero
  while `active_burst_count` stays zero.
- One visible process can map to multiple historical sessions. Therefore
  process evidence must not be folded into recent movement counts.
- Project and tool rows should present recent movement, all known sessions,
  process pressure, CPU, and memory as separate rails.
- Project attribution must normalize nested generated execution workspaces back
  to the owning project root before aggregation. A generic leaf such as
  `workspace` must not become its own project when the path carries a clear
  project-owned benchmark or evaluation ancestor.
- Role splits are session-level facts. A process contributes to role totals only
  after it maps to session evidence that has a role.
- Disk I/O is diagnostic process evidence. It must not imply recent movement
  unless the mapped session also has recent local-log activity. Network
  throughput should remain unavailable unless the observer has a reliable
  per-PID source for it.
- Whole-machine CPU, memory, and network counters are system pressure, not agent
  workload. They may refresh more often than snapshots, but they must stay in
  their own system resource field and UI surface.
- macOS thermal pressure is a public system state, not a Celsius temperature.
  Exact hardware temperature and fan RPM remain unavailable unless a reliable,
  public source is added; they must never be estimated from CPU usage or shown
  as zero.
- Whole-machine network counters should foreground inbound and outbound
  throughput. Interface packet errors and input drops may be shown as a local
  packet issue rate, but the UI must not present that number as an end-to-end
  internet packet-loss measurement.
- Output token throughput is a recent workload-volume rate, not a benchmark of
  model generation speed. Its denominator is the fixed trailing wall-time
  window. When a cumulative output counter is observed sparsely, its positive
  delta is distributed uniformly across the interval between the two observed
  counter timestamps, folded into one-second session buckets, and clipped to the
  trailing window. The UI must disclose the rolling window and must not label
  this value as decode TPS.
- High output throughput must not make the metric unavailable. Raw usage update
  frequency is not a capacity dimension: valid appends are parsed as a stream
  and immediately folded into fixed one-second, per-session buckets. Collection
  capacity depends on the 300-second window and contributing sessions, not the
  number of token updates inside that window.
- A newly discovered token source establishes a baseline without replaying
  history into current TPS. Archive replay independently restores retained history
  from native usage timestamps and counts. Counter resets, file replacement or truncation, and collection gaps
  longer than the rate window also rebaseline without producing a current event.
- Project output-throughput rows are partitions of the same sampled events used
  by the aggregate. Conflicting or absent project attribution stays under
  `unassigned`; project rates plus `unassigned` must sum to the aggregate rate.
- `unavailable`, `no_data`, and `stale` output-throughput samples carry no
  numeric rate. A numeric zero is valid only when fresh output-token evidence
  exists and no positive output falls inside the trailing window.
- Live `unavailable` samples expose `unavailable_reason`. Supported reasons are
  unconfigured transcript sources, and incomplete file-event coverage while no
  positive output has been measured. Token volume, raw usage-event count, and
  the recent-file sampling cap are not unavailable reasons.
- Degraded coverage suppresses the rate only while the measured total is zero. A
  zero floor is trivially true and reads as "nothing is happening" when the truth
  is "we may have missed all of it", so that case fails closed. Once positive
  output is measured, missing evidence can only mean there was more throughput,
  never less, so the reading survives as a floor with `coverage: "partial"` and
  a `coverage_reason`.
- Sampling more recent transcripts than the file cap allows does not make the
  rate unknown: every token in the sample was really observed, so the sample
  stays numeric and declares `coverage: "partial"` with `tracked_file_count` and
  `eligible_file_count`. Such a rate is a floor and must be presented as one
  (a `≥` marker and the file counts), never as a complete measurement. The cap
  evicts the coldest tracked file rather than refusing the newest candidate, so
  the retained subset is the hottest one available. Honest-unknown means never
  claiming completeness you do not have — it does not mean withholding a real
  measurement, least of all when transcript volume is at its highest.
- A floor stays a floor once persisted. A minute measured under degraded
  coverage carries `coverage: "partial"` into the throughput history, and a
  rolled-up window inherits it from any minute behind it: the sum of floors can
  only be missing tokens, never carrying extra. Storing such a minute as a plain
  exact count would launder away the qualifier the live sample is careful to
  carry.
- A transcript path only names a project when the path is actually inside a
  repository. Transcript stores are not checkouts — `~/.codex/sessions/2026/09/14/`
  has a leaf that looks like an ordinary directory name but identifies nothing,
  and attributing to it would merge unrelated sessions into a fabricated project.
  An unprovable project is `unassigned`, which is honest, rather than a confident
  wrong bucket, which is not.
- The persistent throughput fact is one closed, non-overlapping minute: minute
  end, evidence state and reason, exact output-token count, exact sparse project
  token partitions, and hashed contributing-session identities. A pointer-valued
  token count keeps measured zero distinct from missing coverage. Snapshot
  refresh cadence never controls minute-fact density.
- Startup archive recovery and live append collection share adapter discovery,
  native usage decoders, counter/message transitions, and token partition rules.
  Replay persists per-source checkpoints and message maxima; it does not read
  cleaned trajectory text to infer counts. Detailed trajectory normalization is
  a separate projection of the same authorized source inventory.
- Replay restores closed minute facts in the retained 30-day window at their
  native times, with `origin: "session_replay"` and `coverage: "partial"`.
  First cumulative counters establish baselines; resets, missing timestamps,
  future timestamps outside accepted skew, and half-written records do not
  manufacture throughput. It never injects old output into the current rate.
- Recovered positive output is a lower bound: an archived file does not prove
  that every producer was recorded. Missing intervals and a zero lower bound
  remain gaps. CPU, RSS, and other absent measurements cannot be reconstructed
  from usage logs. A directory audit repairs discovery omissions, not absent
  evidence inside an already lost record.
- The source replay ledger commits usage state, message deduplication, minute
  contributions and its complete-record offset together. Repeated scans and
  restarts replace the same derived minute keys rather than add the full source
  totals again. Replay may replace its own older projection or a non-numeric
  minute; numeric online facts are retained, including their partial qualifier,
  because old aggregate facts cannot prove per-token overlap. They are never
  summed with reconstructed totals. A later online fact can replace a replay
  fact for the same minute. Updated history is projected on snapshot refresh.
- The semantic layer derives trailing `1m`, `5m`, and `15m` rates by summing the
  required consecutive minute facts once and dividing by the selected wall-time
  window. It also unions hashed identities for exact contributing-session counts.
  Any absent or non-numeric minute makes the derived window unavailable; the
  denominator never shrinks and gaps never become zero.
- Project rates are derived from the same minute facts and denominator as the
  aggregate. Their sum, including `unassigned`, equals the aggregate. History
  without an exact project partition is missing evidence; do not reconstruct it
  from the current project mix or draw a synthetic catch-all layer.
- `1D` through `30D` select only the horizontal observation range. `1m / 5m /
  15m` independently select the rolling denominator. Dense series retain at
  most 240 time-distributed source points after period statistics are computed;
  missing coverage is retained as a gap marker and the river must break there.
- The throughput trend shows `MAX`, nearest-rank `P95`, `AVG`, and
  `CUR(<selected-window>)`. The first three use every valid numeric derived point
  in the selected range before display reduction. Numeric zero is valid;
  missing, stale, no-data, and partition-missing points are excluded. `CUR` is
  present only when the latest point is currently fresh and never reuses an old
  non-zero value.
- Historical rolling-rate samples are semantically incompatible with minute
  facts. Migrate them automatically in bounded, idempotent batches into
  independently keyed `legacy:<seconds>` series. Never approximate them into
  minute facts, mix them into current summaries, or expose a legacy value as
  `CUR`. Clear the old main-history field only after every legacy batch and the
  atomic main-history rewrite succeed.
- UI labels may abbreviate for density, but tooltips and accessible labels must
  preserve the semantic name.
- Trend charts, selected-point readouts, hover tooltips, and inspectors must use
  the same sampled datum. Do not manufacture OHLC fields from adjacent points or
  show a derived plot value beside a different raw readout.
- The session/process count chart may place recent-movement sessions, known
  sessions, and visible PIDs on one time plane because all three are counts.
  `active_burst_concurrency` is the number of sessions whose recent-activity
  spans overlap the sampled time; it is not a record count or raw event count.
  Each series keeps its own evidence family and actual sample timestamp.
  Missing transcript or runtime coverage stays empty, and the UI must not
  resample, synthesize aligned values, or carry one family forward to make the
  lines align.
- Runtime trend drilldowns may split a selected bucket into persisted process
  fields such as total visible PIDs, Coding Agent process distribution, host
  process distribution, mapped processes, unmapped processes, and matched share.
  Per-tool or per-process-type historical drilldowns must use distributions
  recorded in that trend sample; do not infer them from the current snapshot for
  past buckets.
- Prediction/anomaly signals and safe export live in the diagnostics surface.
  They can summarize system pressure, mapping gaps, low-confidence sessions, and
  missing collection capability, but they must report unavailable evidence as
  unavailable rather than silently coercing it to zero.
- Runtime telemetry is an optional adapter family. Until configured, it should
  appear as `not_configured` capability, not as a warning against the local
  observer. When configured in the future, telemetry may strengthen attribution
  and timing but must not override the session/process semantic matrix without
  an explicit merge rule.

## Implementation Ownership

- Backend semantic helpers live in the metric semantic layer and define how raw
  live sessions and snapshot sessions become metric facts.
- Frontend semantic helpers live in the UI metric semantic layer and define how
  snapshot fields are converted into row matrices and resource totals.
- Metric registry fields in the snapshot are the user-facing index of the
  semantic matrix. UI components may localize labels, but missing-state and
  source-family meaning must stay aligned with this document.
- Callers should use these helpers when computing project rows, tool rows,
  summary counts, or hover details. Direct arithmetic over raw fields is allowed
  only inside the semantic layer or in tests that explicitly assert the contract.
