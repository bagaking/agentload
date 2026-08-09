# Coding Agent Evidence Adapters

Status: approved implementation direction.

## User Requirement

Define one IOC mechanism for coding agents such as Codex, Claude, Gemini,
Trae/TraeX, OpenCode, Cursor, Hermes, OpenClaw, and Pi. When a coding-agent
adapter is injected, that adapter determines which local evidence roots and
files are eligible and which directories must be filtered. Optimize the code
structure while controlling system entropy.

## Product Boundary

- Use one code-owned agent registry assembled at the application composition
  root. Do not add a DI framework.
- Make adapters capability-oriented. Process identity, evidence discovery,
  transcript parsing, output-usage decoding, and display metadata are distinct
  capabilities; an agent may implement only the capabilities backed by local
  evidence.
- Keep filter and file-layout rules inside the owning adapter. User
  configuration may enable an adapter or override a root, but it must not define
  arbitrary include/exclude glob semantics.
- Unknown or changed evidence layouts produce an explicit evidence gap. Do not
  fall back to a broad recursive scan and do not synthesize sessions, projects,
  token usage, or throughput.
- Replace obsolete tool switches as their owning behavior moves into adapters.
  Do not retain compatibility dispatch, migration code, or parallel fallback
  paths.

## Initial Capability Truth

| Agent | Process identity | Transcript evidence | Output usage | Initial adapter scope |
| --- | --- | --- | --- | --- |
| Claude | verified | verified | verified | full existing parity |
| Codex / CodexL | verified | verified | verified | full existing parity |
| Trae / TraeX | verified | verified | verified | full existing parity |
| Gemini | verified | not yet verified | not yet verified | process-only |
| OpenCode | verified | not yet verified | not yet verified | process-only |
| Cursor | host/process evidence only | not yet verified | not yet verified | host/process-only |
| Hermes | not yet verified | not yet verified | not yet verified | registry identity only after evidence inspection |
| OpenClaw | not yet verified | not yet verified | not yet verified | registry identity only after evidence inspection |
| Pi | not yet verified | not yet verified | not yet verified | registry identity only after evidence inspection |

Registering an agent name is not evidence support. New capabilities require
real fixtures or an installed-source contract and focused parser/discovery
tests.

## Discovery Contract

- Priority transcript paths already mapped from visible processes are read
  directly and never require a tree walk.
- Claude adapters own the `projects` layout, including verified subagent
  transcripts and explicit pruning of non-transcript branches.
- Codex adapters own `sessions`, `archived_sessions`, and `.codexl` lane-event
  layouts. Date-partitioned directories are pruned against the requested
  cutoff before visiting files.
- Trae adapters own the dated `sessions` layout and must prune `*.artifacts`
  subtrees before traversal.
- Directory traversal uses standard-library structured APIs and returns exact
  file metadata and surfaced errors. External `fd`, `find`, or shell pipelines
  are performance probes, not production dependencies.
- The long-lived direction is an FSEvents-maintained evidence index and dirty
  set. Snapshot collection consumes that index instead of repeatedly walking
  complete agent histories.

## Acceptance Boundary

- Existing Claude, Codex, CodexL, and Trae process/session/project/role/token
  behavior remains semantically identical under focused parity tests.
- Foreground transcript discovery no longer enters Trae `*.artifacts` and does
  not visit date partitions older than the active cutoff.
- Process-only agents remain visible as process pressure without fabricated
  session or throughput evidence.
- The registry is the only tool-dispatch authority for migrated capabilities;
  old switches and fallback walkers are deleted.
- Benchmarks separately measure candidate discovery, transcript decoding, and
  live output-usage ingestion so improvements cannot be attributed to the wrong
  stage.
- `go test ./...`, race-sensitive adapter tests, locale validation, UI build,
  packaged-app smoke, and installed API smoke pass before closeout.

## Non-Goals

- Runtime-loaded plugins, reflection-based service containers, or a generic
  adapter configuration language.
- Claiming transcript or token support for an agent without verified local
  evidence.
- Replacing local evidence truth with cloud APIs or optional telemetry.
- Keeping the previous dispatch structure as a compatibility layer.
