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
| System resources | `system_resources` | Whole-machine OS counters sampled independently from transcript scanning | Session activity, project attribution, or per-PID network guesses |
| Role matrix | `main_agent_sessions`, `subagent_sessions`, `unknown_role_sessions` and active role splits | Session role inference from thread source, parent thread, lane paths, and independent-run evidence | Process role guesses without mapped session evidence |
| Tool coverage | project/tool `session_count`, `active_burst_count`, `process_count` | Per-tool aggregation of known sessions, recent movement, and process pressure | Treating process pressure as recent movement |

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
- UI labels may abbreviate for density, but tooltips and accessible labels must
  preserve the semantic name.
- Trend charts, selected-point readouts, hover tooltips, and inspectors must use
  the same sampled datum. Do not draw a candle from one derived value and show a
  different raw point in the adjacent text.

## Implementation Ownership

- Backend semantic helpers live in the metric semantic layer and define how raw
  live sessions and snapshot sessions become metric facts.
- Frontend semantic helpers live in the UI metric semantic layer and define how
  snapshot fields are converted into row matrices and resource totals.
- Callers should use these helpers when computing project rows, tool rows,
  summary counts, or hover details. Direct arithmetic over raw fields is allowed
  only inside the semantic layer or in tests that explicitly assert the contract.
