# Neutral Observation Principles

AgentLoad reports local evidence. It must not decide whether a
number is good, bad, too high, too low, efficient, inefficient, normal, or
abnormal unless the product has an explicit agreed standard for that label.

## Core Rule

Show the measured value, the evidence source, the time window, and the limits
of the measurement. Leave value judgment to the operator unless a documented
threshold exists.

Examples:

- Say `sessions with recent local-log movement: 5`.
- Do not say that count is critical, busy, or notable unless the product has a
  documented standard for that label.
- Say `100 concurrent sessions observed`.
- Do not say `100 concurrent sessions is too many`.
- Say `3 running processes are not matched to local session evidence`.
- Do not say those processes are wrong or broken.

## Why

Concurrency is contextual. A count that is easy for one operator or workflow can
be unrealistic for another. The observer cannot know that context from local
process and transcript evidence alone.

## UI Copy Rules

- Prefer nouns such as `count`, `bucket`, `lane`, `window`, `matched`,
  `unmatched`, `main agent`, `subagent`, and `unknown`.
- Avoid judgment labels such as `critical`, `bad`, `strained`, `healthy`,
  `unhealthy`, `too many`, or `too few` in visible copy.
- If the UI needs grouping, document the grouping standard first. Do not invent
  arbitrary visible thresholds during implementation.
- Do not promote one concurrency count into a standalone status, hero number,
  or badge unless the product has documented why that count is the primary
  reading. Realtime counts such as recent local-log movement, known sessions,
  and running processes should appear as peer measurements by default.
- Explanations should say what the number is and how it was observed, not what
  the user should feel about it.

## Role And Attention Inference

Main-agent vs subagent classification is an inference over evidence:

- `thread_source=user` or an independently resumable top-level session is a
  main-agent signal.
- `thread_source=subagent`, a parent thread id, or a codexL/asagent lane path is
  a subagent signal.
- Missing evidence stays `unknown`.

The UI may use this to separate human interaction entry points from derived
execution sessions. It must not treat either category as inherently better or
worse.

Project rows are grouping containers, not role labels. A project can contain a
mix of main-agent entries, subagent sessions, and unknown-role sessions. Product
surfaces should show project activity counts together with this per-project role
mix, and reserve per-session role labels for session rows or expandable
session-level detail.

## Project-First Tree

The online surface should start with projects because that matches the operator
question: which project and REPL/application is using local AI capacity right
now?

Within a project, group sessions by REPL/application/tool, then show:

- main conversations;
- subagents linked to a main conversation by explicit relationship evidence;
- unlinked subagents whose parent is absent or unknown in the snapshot;
- unknown-role sessions.

This tree is an observation aid, not a judgment. Do not label a project as good,
bad, overloaded, or underused. Do not attach a subagent to a main conversation
unless the observed metadata supports that link. If the relationship evidence
is missing, state that it is unlinked or not observed.

The active definition must be stated in the UI. In the current implementation,
`active` means the last local-log event age is less than or equal to
`snapshot.config.idle_gap_seconds`; the default is 90 seconds unless the
observer configuration changes it.
This is an event-observation rule, not a model-state detector. If an agent is
thinking but no transcript or local activity log writes a new event, the
observer should not infer recent movement from thinking alone. The session can
still appear through known-session and running-process evidence.

Compact metric rows should remain readable as observations. If the surface is
narrow, prefer a small table over a strip of per-number tags: the row header
should state the observation window (`Active` versus `All`) and the column
header should state the object type (`Main`, `Sub`, `Total`). Each numeric cell
still needs an accessible explanation such as a native tooltip or `aria-label`
that names the metric, the value, and the evidence meaning. Visual treatment
should not make every number look like a separate status badge. This keeps the
evidence window visible without requiring users to decode abbreviations such as
`AM` and `AS`.

Tool coverage is a contextual cue, not a separate analysis paragraph. In compact
project lists, show it as a small badge strip beside the row controls. The badge
should use the observed tool application's local icon when an allowlisted local
app resource exists, and fall back to a compact text mark only when the icon is
not available. Its tooltip or accessible label must state the full tool name and
the active/session meaning. If the strip has more tools than the row can hold,
keep it horizontally scrollable with edge fade instead of growing the row.

Compact rows should minimize heavy capsule styling. Role, movement, tool, and
process values are neutral labels and counts; use subtle text, small icons, and
thin separators before rounded badges. Reserve filled or outlined pills for
places where the user needs a strong selectable affordance.
In project lists specifically, tool marks should behave like inline readouts:
application icon, active/session count, and tooltip. They should not look like
independent status capsules unless they become an interactive filter or
selection control.

Session role marks can be icons to save space, but they remain evidence labels.
A role icon may copy the session id because the icon sits next to the abbreviated
id, but the tooltip must still name the role and the copy behavior. The row
should keep the last local-log activity time visible in the first reading pass,
not only inside an expanded detail row.

Host application context is also evidence. It should be derived from the local
process table and parent-process chain, then tied to an observed `.app` bundle
before showing an application icon or offering a click-to-open action. Do not
invent host app names from the coding tool. If the process ancestry or bundle
evidence is missing, show the coding tool and leave host app context missing.
Bundle evidence must come from the executable path of the coding-agent process
or one of its observed parent processes. A `.app` path that appears only as a
command argument is not host-application evidence and must be ignored.
Coding agent and host application are two different observations. A row should
not use the editor or terminal icon as a replacement for the agent identity:
show the coding agent mark from the observed agent tool, then show the host app
mark from process ancestry when available. Both marks need tooltip text because
icon-only marks are ambiguous.
For Codex CLI specifically, the coding-agent mark uses the embedded
OpenAI-style Codex CLI glyph. A local `Codex.app` icon belongs only to the host
application mark when process ancestry shows that app as the host.

Client-facing snapshots should summarize local evidence without exposing local
filesystem paths. Keep absolute transcript paths, executable paths, app bundle
paths, and cache/history files in the internal snapshot so host icons,
open-host actions, and diagnostics can still use them. The `/api/snapshot`
projection should keep stable identifiers, tool names, host app names, counts,
and sanitized command identity only.

Trend charts should expose exact sampled values without requiring the user to
open a detail card first. The selected point should show a compact date/time and
count label in the chart, while the expanded detail remains the place for
definitions and evidence limits. Use filled areas or other surface treatment to
reduce visual grid noise, but do not fill missing buckets with invented values.

Disclosure is part of the observation workflow. Expanding a project tree,
session evidence row, or explanatory note should preserve scroll and focus so
the operator can keep reading the same local context.

Auto-refresh is a data-update mechanism, not a navigation event. The refresh
timestamp can carry a compact click-to-cycle interval control, but changing the
interval should only affect client polling cadence. It should not reset tabs,
expanded project/session detail, scroll position, selected trend bucket, or
keyboard focus.

Refresh cadence is also an observation boundary. The fastest automatic refresh
is 30 seconds; the default automatic refresh is 5 minutes; users may choose
slower intervals or pause. Each refresh time slice has a stable
`refresh_slot_id` so duplicate manual, UI, or background triggers can coalesce
instead of repeating the same scan. A same-slot snapshot may update cached data
and timestamp text, but it should not force a full UI rerender when the user is
reading a panel.

Hidden surfaces should be quiet. When the native popover is hidden or a browser
document is not visible, client polling and popover resize messages should be
paused. On return, the UI can refresh once if the snapshot is stale, then resume
the selected cadence. This is a polling policy only; it does not change the
meaning of active sessions or sampled trend buckets.

## Missing Data

Missing values stay missing. Do not fill missing trend buckets with zero, sample
from a neighboring bucket, or show fixture/demo data as runtime truth.

For local transcript files, unchanged file metadata is a cache signal. If the
path, size, and modification time are unchanged, reuse the parsed trace instead
of rereading file contents. This cache policy must never fabricate a changed
value: a later metadata change or explicit uncached scan must still read real
local file content.
When a transcript grows append-only and the cached file ended with a newline,
the scanner may parse only the appended JSONL bytes and merge them into the
cached trace. If the file was rewritten, truncated, left with a partial final
line, or uses sidecar metadata that may have changed independently, do a full
parse instead.
