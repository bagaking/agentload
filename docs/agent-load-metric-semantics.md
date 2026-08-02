# Agent Load Metric Semantics

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
| Output token throughput | `/api/live-token-rate` `output_tokens_per_second`, `state`, `window_seconds` | Positive output-token events and safe cumulative-output counter deltas from local transcripts, projected onto a trailing 180-second wall-time window | Input/cache/reasoning tokens, process activity, model decode speed, API-active-time throughput, or replayed deltas across collection gaps |
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
  counter timestamps before clipping to the trailing window. The UI must
  disclose the rolling window and must not label this value as decode TPS.
- A newly discovered token source establishes a baseline without replaying
  history. Counter resets, file replacement or truncation, oversized append
  gaps, and collection gaps longer than the rate window also rebaseline without
  producing a current event.
- `unavailable`, `no_data`, and `stale` output-throughput samples carry no
  numeric rate. A numeric zero is valid only when fresh output-token evidence
  exists and no positive output falls inside the trailing window.
- UI labels may abbreviate for density, but tooltips and accessible labels must
  preserve the semantic name.
- Trend charts, selected-point readouts, hover tooltips, and inspectors must use
  the same sampled datum. Do not draw a candle from one derived value and show a
  different raw point in the adjacent text.
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
