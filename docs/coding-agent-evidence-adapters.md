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
| Grok | verified | verified | verified | full parity; usage is per-turn, not cumulative |
| Gemini | verified | verified (JSON/JSONL session files) | verified (per-message output) | transcript + live usage |
| OpenCode | verified | verified (SQLite or legacy message JSON) | unsupported (database-backed) | all session traces; transcript-only |
| Cursor | generic host-app evidence only | unsupported (no per-line timestamps) | unsupported (recorded counters are all zero) | identity-only adapter; do not classify the app host as an agent |
| Hermes | verified | verified (read-only `state.db`) | unsupported (database-backed) | all session rows from SQLite |
| OpenClaw | verified | verified (agent session JSONL) | verified (per-message output) | transcript + live usage |
| Pi | verified | verified (sessions and session-artifacts JSONL) | verified (per-message output) | transcript + live usage |

Registering an agent name is not evidence support. New capabilities require
real fixtures or an installed-source contract and focused parser/discovery
tests.

## Verified Limited Adapters

- Hermes process identity is backed by the installed upstream
  [NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent)
  entry-point and service contract: `hermes`, `hermes-agent`, `hermes-acp`,
  and `python -m hermes_cli.main`. Exact executable or Python module tokens
  match; incidental command arguments and the generic `run_agent` module do not.
- Hermes support reads session rows from `~/.hermes/state.db` in read-only mode.
  It preserves parent-session role metadata and stored token totals, emitting
  one `SessionTrace` per database session.
- Cursor is preserved as generic `.app` ancestry on an already verified coding
  agent process. The Cursor host process itself does not create an agent session
  or throughput source.
- Cursor transcript and usage support stay off for a measured reason, not an
  unexamined one. Its CLI transcripts
  (`~/.cursor/projects/<path-encoded>/agent-transcripts/<uuid>/<uuid>.jsonl`)
  carry exactly two keys per line, `role` and `message`: across all 33 files on
  the survey machine, zero contained a timestamp or token field. `nonEmptyTrace`
  drops any trace without event times, so there is nothing to parse. On the IDE
  side the schema has the fields but not the values — per-message `tokenCount`
  is zero across all 13990 stored messages and `usageData` is empty for all 92
  conversations. The only populated number, `promptTokenBreakdown.totalUsedTokens`,
  measures current context occupancy rather than consumption; rendering it as
  throughput would be fabrication.
- Gemini CLI support reads the `~/.gemini/tmp/**/chats/session-*` JSON/JSONL
  files. It derives project paths from message `cwd` and subtracts cached input
  before publishing input/output/reasoning totals. Its per-message output
  decoder feeds the existing live sampler.
- Grok is the one vendor whose on-disk record exceeds the existing parity bar:
  `~/.grok/sessions/<percent-encoded-cwd>/<session-id>/updates.jsonl` timestamps
  every line and records per-turn `inputTokens`/`outputTokens`/`cachedReadTokens`/
  `reasoningTokens` plus `costUsdTicks`. Two traps are locked by tests: the
  counts are per-turn (the output series falls between turns, so reading them as
  cumulative inflates the rate), and every counter is repeated under
  `usage.modelUsage.<model>`, which must not be summed twice.
- OpenClaw support reads `~/.openclaw/agents/*/sessions/*.jsonl`, with indexed
  and fallback layouts handled by the same bounded evidence walk. `openclaw`,
  `open-claw`, and the historical `penclaw` executable spellings normalize to
  the `openclaw` adapter.
- Pi support reads both `~/.pi/agent/sessions` and nested
  `~/.pi/agent/session-artifacts` JSONL. The generic `pi` executable is now an
  explicit adapter identity because its session layout is verified by parser
  fixtures; it is not inferred from unrelated command arguments.

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
- Gemini, OpenCode, Hermes, OpenClaw, and Pi own their configured local roots;
  database-backed sources are opened read-only and JSONL sources reuse the
  Observer's file cache and cutoff coverage.
- Directory traversal uses standard-library structured APIs and returns exact
  file metadata and surfaced errors. External `fd`, `find`, or shell pipelines
  are performance probes, not production dependencies.
- The Observer owns one FSEvents-maintained evidence index and injects it into
  snapshot collection and live throughput sampling. Adapter discovery performs
  the bounded cold reconciliation; warm reads consume indexed candidates with
  zero directory visits.
- Configured root spelling remains product-visible. Physical root and file keys
  resolve symlinks only inside index/watch ownership, so duplicate aliases do
  not launch duplicate discovery walks and nested watch roots have one owner.
- Filesystem events are classified by the owning adapter and update the index
  directly. Only events racing with a reconciliation enter the 4,096-distinct-
  file mutation buffer. Overflow and dropped events fail closed, expose an
  evidence gap for the recovery sample, and request one adapter-pruned
  reconciliation. Transcript files are not kept permanently open.

## Output Usage Contract

- The composition root injects the same registry into snapshot observation and
  live throughput sampling. A configured root does not imply usage support; the
  owning adapter must expose an output-usage decoder.
- Claude decodes verified message usage and keys growing-message deltas by
  session plus message id or UUID. Codex/CodexL and Trae/TraeX decode their
  verified cumulative and incremental output fields. Missing timestamps use the
  poll observation time; missing output fields produce no usage observation.
- Live decoding uses finite typed JSON envelopes. It does not lowercase whole
  records, decode generic maps, recurse through arbitrary keys, or call the full
  transcript token parser.
- Claude message dedupe is bounded during insertion with a 2,048-entry LRU;
  repeated updates move one existing entry and do not grow the ordering
  structure.
- Gemini, OpenClaw, and Pi expose per-message output decoders keyed by the
  upstream message id. OpenCode and Hermes remain transcript-only for live
  throughput because their current evidence is SQLite-backed.
- The live sampler requests the maximum six-hour foreground index coverage on
  startup, then locally selects files modified in its 15-minute live window.
  The watcher starts before the first snapshot, while live polling starts only
  after process-derived roots have joined the initial reconciliation. This
  avoids competing startup walks while keeping live parsing bounded to recent
  files.
- The append benchmark defines one operation as 32,768 realistic updates split
  across Claude, Codex, and Trae files. Running it with a 1,000-operation
  benchtime proves 32,768,000 updates through file IO, typed decoding, dedupe,
  and bucket aggregation.

## Acceptance Boundary

- Existing Claude, Codex, CodexL, and Trae process/session/project/role/token
  behavior remains semantically identical under focused parity tests.
- Process-only adapters require exact positive and negative command fixtures;
  discovery adapters additionally require positive, rejected-path, and pruning
  fixtures before their capability can be enabled.
- Filename-derived session hints are source rules owned by the matching process
  adapter. Session aggregation consumes the hint without switching on agent
  names.
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
