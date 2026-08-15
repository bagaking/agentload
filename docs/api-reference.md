# Agent Load API Reference

> Status: living document, last verified 2026-10-01.

This page documents every route registered in `server.go` (`trayApp.handler`).
The server binds to loopback only (`127.0.0.1:8642` by default, ephemeral-port
fallback when taken) and serves the embedded web UI plus a small local JSON
API. Response shapes are named after the Go types in `types.go`.

Global behavior:

- A top-level wrapper rejects any request whose path contains a `.` or `..`
  segment (including percent-encoded forms) with `404` (`hasDotPathSegment`).
- GET-style endpoints also accept `HEAD` (headers only). Wrong methods return
  `405 method not allowed`.
- State-changing endpoints are POST-only and pass through `rejectCrossOrigin`:
  if the request carries an `Origin` (or, failing that, `Referer`) header, its
  host must be equivalent to the request `Host` — loopback aliases
  (`localhost`, `::1`, `0.0.0.0`, `::`) normalize to `127.0.0.1` and ports
  must match. Requests **without** Origin/Referer (curl, the native shell)
  stay allowed. Mismatches get `403 cross-origin request rejected`.

## Route table

| Route | Method | Purpose | Response | Caching |
| --- | --- | --- | --- | --- |
| `/` | GET/HEAD | Popover page (embedded `ui/dist/index.html`) | HTML | `no-store` |
| `/dashboard` | GET/HEAD | Dashboard page (same embedded HTML) | HTML | `no-store` |
| `/assets/` | GET/HEAD | Embedded UI bundle assets | JS/CSS/SVG | `no-store` |
| `/api/snapshot` | GET/HEAD | Full sanitized observation snapshot | `Snapshot` | `no-cache` + ETag |
| `/api/capabilities` | GET/HEAD | Registry-owned vendor × signal-family evidence coverage | `{families, rows}` | `no-store` |
| `/api/system-resources` | GET/HEAD | Live whole-machine resource sample | `SystemResourceSnapshot` | `no-store` |
| `/api/live-token-rate` | GET/HEAD | Latest trailing output-token throughput sample | `LiveTokenRateSample` | `no-store` |
| `/api/diagnostic-export` | GET/HEAD | Downloadable sanitized evidence bundle | `DiagnosticExportSnapshot` | `no-store` |
| `/api/refresh` | POST | Request a refresh slot | ad-hoc JSON, `202` | — |
| `/api/quit` | POST | Quit the app | ad-hoc JSON, `200` | — |
| `/api/process-diagnostic/{pid}` | GET/HEAD | Sanitized per-PID diagnostic | `ProcessDiagnosticSnapshot` | `no-store` |
| `/api/tool-icon/{tool}` | GET/HEAD | Tool icon (embedded SVG or converted `.icns`) | image | `private, max-age=3600` |
| `/api/host-app-icon/{pid}` | GET/HEAD | Host app icon for an observed host-app PID | image | `private, max-age=3600` |
| `/api/open-host-app/{pid}` | POST | Bring an observed host app forward | ad-hoc JSON, `202` | — |

## Pages and assets

### `GET /` and `GET /dashboard`

Both serve the same embedded `ui/dist/index.html` (`handlePopoverPage`,
`handleDashboardPage`); the React bundle decides the view from
`window.location.pathname`. Exact-path match only; anything else under them is
`404`. Served with `Cache-Control: no-store, no-cache, must-revalidate,
max-age=0` plus `Pragma`/`Expires` so a rebuilt binary never fights a stale
browser cache.

### `GET /assets/{file}`

`handleUIAsset` serves single-level files under the embedded `ui/dist/assets/`
directory (no subdirectories). Content type is derived from the extension
(`contentTypeFor`); unknown extensions fall back to
`application/octet-stream`. Note that even hashed bundle assets are served
`no-store`.

## Snapshot API

### `GET /api/snapshot`

Returns the current sanitized `Snapshot`. The handler serves the cached
snapshot from the last refresh slot; only on a cold start (no cached snapshot
yet) does it build one synchronously under a context timeout of
`clamp(lookback/10, 45s, 5m)` — with default config that is 5 minutes, so a
cold first poll can block noticeably.

- **Headers (request)**: `If-None-Match` — compared against the snapshot ETag;
  comma-separated lists and `*` are honored (`etagListMatches`).
  `Accept-Encoding: gzip` selects the compressed representation.
- **Headers (response)**: `ETag` (the quoted `refresh_slot_id`),
  `X-Refresh-Slot-ID` (unquoted), `Cache-Control: no-cache`, and
  `Vary: Accept-Encoding`; gzip responses include `Content-Encoding: gzip`.
- **Conditional flow**: the refresh slot decides the ETag, so a matching
  `If-None-Match` short-circuits to `304` *before* the sanitize pass runs.
  Full responses are served from a per-slot cache of the sanitized, encoded
  JSON (`clientSnapshotJSON`). Its gzip representation is cached alongside it,
  so at most one sanitize and one compression pass run per refresh slot.
- **Empty state**: if the app has no observer and no cached snapshot the body
  is an empty `Snapshot` JSON (`{}`-equivalent with zero values).
- **Process coverage**: `process_stats.incomplete` marks a failed OS process
  query. When `last_known` is true, `live_processes` contains the last clean
  rows for visibility and `notes` states that they are stale evidence. Such a
  snapshot is served to the caller but is excluded from the cache and history.
- **Throughput trends**: each range exposes
  `throughput_trends.windows[].throughput_series[]`. Current series keys are
  `minute:60`, `minute:300`, and `minute:900`; their `kind` is `minute_rollup`.
  Optional migrated keys use `legacy:<seconds>` and kind
  `legacy_rolling_rate`. Every series includes `window_seconds`,
  `granularity_seconds`, `source_from`, `history_complete`, `points`, and an
  optional `summary`. Point fields are `output_tokens_per_second`,
  `output_token_throughput_state`, `output_token_throughput_window_seconds`,
  `output_token_active_sessions`, `output_token_projects`, and
  `throughput_sampled`. Project values use the same minute facts and denominator
  as the aggregate and sum to it. Missing points omit the numeric rate and
  project list; they are not reconstructed or coerced to zero.
- **Throughput summaries**: `summary` contains `max`, nearest-rank `p95`, `avg`,
  optional fresh `current`, optional `current_at`, `window_seconds`, and
  `sample_count`. Statistics include all valid numeric points, including zero,
  before the 240-point display reduction. Legacy summaries never include
  `current`, and legacy/current families are never combined.
- **Throughput storage health**: `history.throughput` reports retained minute
  facts, legacy facts, dropped/corrupt record counts, and optional
  `last_write_error`. Its local `store_path` is removed by the sanitize layer.
- **Capability coverage**: `capability_matrix` carries the same registry-owned
  vendor rows exposed by `/api/capabilities`, including seven signal-family
  cells and their evidence notes. It is descriptive evidence coverage, not a
  claim that unavailable or not-configured signals have been measured.
- **Sanitization** (`sanitizeSnapshotForClient`): see "Sanitize layer" below.

### `GET /api/capabilities`

Returns the registry-owned evidence coverage matrix. `families` is the stable
ordered list of seven signal-family keys; `rows` contains one entry per
registered vendor with the four underlying capability slots, evidence shapes,
notes, and `signal_families` cells. Cell states are `supported`, `partial`,
`unavailable`, or `not_configured`; observed cells are the only cells that
claim local evidence. The endpoint is read-only and uses the
same projection embedded in `Snapshot.capability_matrix` and rendered by the
diagnostics coverage panel, so a registry change cannot update only one of the
three surfaces.

`Cache-Control: no-store`.

### `GET /api/system-resources`

Returns a `SystemResourceSnapshot` from the background sampler (2s cadence);
falls back to a synchronous sample only if the sampler is not running. CPU%
and network rates are two-sample deltas; the first sample discloses "rates
need two samples" in `notes` instead of a fake zero. `thermal_state`
(`nominal|fair|serious|critical`) is reported separately from `supported` via
`thermal_state_supported`. `Cache-Control: no-store`.

### `GET /api/live-token-rate`

Returns the latest `LiveTokenRateSample` published by the independent 30-second
background transcript sampler. Reading the endpoint never advances transcript
file offsets or cumulative baselines. `output_tokens_per_second` is numeric only
for `live` and `zero`; it is `null` for `unavailable`, `no_data`, and `stale`.
`unavailable` responses include `unavailable_reason`: `not_configured`,
`file_capacity`, or `watch_incomplete`. High token volume and raw usage-update
frequency do not produce an unavailable state.
For numeric samples, `projects` contains positive per-project contributions from
the same event window (`project`, `output_tokens_per_second`, and
`active_sessions`); an empty array means measured zero. Missing or conflicting
attribution is reported as project `unassigned`, so the project partition sums
to the aggregate.
The denominator is the fixed trailing 300-second wall-time window. This is
aggregate output-token workload throughput, not model decode speed or
API-active-time TPS. `Cache-Control: no-store`.

### `GET /api/diagnostic-export`

Returns a `DiagnosticExportSnapshot` (`format_version: 1`) wrapping the
sanitized `Snapshot` plus `omitted_fields` and interpretation notes, with
`Content-Disposition: attachment; filename="agentload-diagnostics.json"` so
the dashboard's export button downloads a file. The export goes through the
same sanitize layer as `/api/snapshot` (`snapshotForClient`), never the raw
snapshot. `Cache-Control: no-store`.

## Control API (POST + Origin check)

### `POST /api/refresh`

Requests a refresh for a slot (`requestRefreshForInterval`). Slots dedupe:
repeated calls inside the same slot return the same `refresh_slot_id` without
queueing extra scans.

- **Query params**: `interval_ms` — optional; selects the slot granularity for
  this request. Zero, invalid, or negative values fall back to the configured
  refresh interval; positive values are clamped to a 30s floor
  (`normalizeRefreshInterval`).
- **Response** (`202 Accepted`):
  `{"ok": true, "refreshing": bool, "refresh_slot_id": string}`. Clients poll
  `/api/snapshot` until its `X-Refresh-Slot-ID` reaches the returned slot.

### `POST /api/quit`

Responds `200` with `{"ok": true}`, then quits the systray app ~150ms later so
the response can flush.

### `POST /api/open-host-app/{pid}`

Brings an observed host application forward. The `{pid}` must match a
`HostApp` present in the **current snapshot** (process rows or session host
apps) and pass `validObservedHostApp` (positive PID, non-empty name, and an
existing absolute `.app` bundle directory without `..`) — arbitrary paths or
PIDs cannot be opened. Runs `/usr/bin/open <bundle path>` under the request
context.

- **Response** (`202 Accepted`): `{"ok": true, "name": string, "pid": int}`.
- **Errors**: `404` when the PID is not an observed host app or has no bundle
  path; `502` with a stable generic error when launching fails. Subprocess
  output is not returned because it may contain local paths or other private
  diagnostics.

## Lookup API

### `GET /api/process-diagnostic/{pid}`

Finds the PID in the sanitized snapshot's `live_processes` and returns a
`ProcessDiagnosticSnapshot`: `pid`, redacted `command`, `session_ids`, and a
sanitized `host_app` (bundle path stripped). `session_paths` is always omitted.
`404` when the PID is not a currently observed AI process or no snapshot
exists yet. `Cache-Control: no-store`.

### `GET /api/tool-icon/{tool}`

Serves a tool icon. The name is normalized (`normalizeToolIconName`) to one of
`codex`, `trae`, `karp`, `claude`, `opencode`, `gemini`; unknown names are
`404`. Resolution order:

1. Embedded SVGs from `ui/tool-icons/` (`resolveEmbeddedToolIconFile`).
2. Well-known local app bundle icons (`toolIconFiles`); `.icns` files are
   converted to PNG with `/usr/bin/sips` and cached under
   `<UserCacheDir>/agentload/tool-icons/` keyed by a SHA-1 of the source path,
   re-converted when the source is newer (`cachedPNGForICNS`).

`Cache-Control: private, max-age=3600`; local files are served via
`http.ServeContent` so they get `Last-Modified`/range support.

### `GET /api/host-app-icon/{pid}`

Like the tool icon, but for a host app observed in the current snapshot (same
`observedHostAppFromRequest` + `validObservedHostApp` gate as
`/api/open-host-app`). Icon candidates come from the bundle's `Info.plist`
(`CFBundleIconFile`, `CFBundleIconFiles` via `/usr/libexec/PlistBuddy`), then
conventional names, then `Resources` globs; `.icns` converts through the same
sips cache. `404` when the PID is not observed or no usable icon exists.

## Sanitize layer

`sanitizeSnapshotForClient` is applied to every snapshot that leaves the
process (`/api/snapshot`, `/api/diagnostic-export`,
`/api/process-diagnostic/{pid}`). What it redacts:

- **Dropped outright**: `config.claude_roots` / `codex_roots` / `trae_roots`
  (emptied), `config.history_file`, `history.store_path`, live session `path`,
  live process `session_paths`, every `host_app.bundle_path`.
- **Command lines** (`sanitizeCommandForClient`): executable reduced to its
  base name; flags kept with values replaced (`--flag=<value>`, or the
  following positional replaced by `<value>`); a short list of identity
  subcommand words (`run`, `serve`, `resume`, …) kept; the first other
  positional argument replaced by `...` and the rest dropped.
- **Path-like text** (`sanitizeTextForClient` / `sanitizeTokenForClient`):
  absolute paths and `file://` URLs anywhere in free text (notes, transcript
  errors, risk-signal evidence, diagnostics, history write errors) are reduced
  to their base name; `key=value` and `key:value` tokens are sanitized on the
  value side; path-like project names are reduced to their directory base
  name.
- **Not sanitized**: session ids, tool names, freshness/confidence enums,
  numeric metrics, and the metric registry — these carry no local paths.

The export's `diagnostics.export.omitted_fields` documents the contract:
raw prompts, absolute local paths, full command arguments, environment
variables, transcript file paths, and app bundle paths never leave the
process.
## Trajectory content API

Trajectory uses JSON-RPC 2.0 at `POST /api/rpc`. The CLI, Popover and local HTTP
clients consume the same typed Go service. Existing snapshot APIs retain their
metric meaning and sanitization. Detailed content is disabled by default and
requires explicit local opt-in, the current instance bearer capability, and
configured transcript roots. Reading does not grant command replay or approval.

The native app passes a capability in the initial page fragment. The UI removes
it immediately and retains it in origin-scoped session storage. CLI clients read
the current owner-only `instance.json` in the history file's `.trajectory/`
directory. CLI HTTP refuses proxies, redirects and non-literal-loopback endpoints.
Credentials are absent from public APIs, metric exports and lifecycle URLs.
The server preserves the existing Host gate and rejects foreign browser origins;
HTML CSP restricts scripts and connections to the local origin.

### Access and methods

`GET /api/trajectory/access` returns `enabled`, `authorized`, `preference_gap`.
Both GET and POST require `X-AgentLoad-Local: 1`; POST additionally requires
`Authorization: Bearer <instance capability>` and body `{ "enabled": true/false }`.
This endpoint returns no content or credential. Revocation denies subsequent
reads/writes, closes content readers and clears in-memory content/watch cursors.
Private persistent recovery state and irreproducible facts are retained. Explicitly authored notes are retained
in a separate private store, inaccessible while disabled; delete them explicitly
through `traj.annotate` when access is enabled.

| Method | Named parameters | Result |
| --- | --- | --- |
| `traj.query` | `collection`, typed selectors, `limit: 1..50`, `cursor`; sessions/events optionally select `count: true` | Collection records, `coverage`, `revision`, optional `next`; sessions/events also expose `watch_cursor`; explicit count adds exact prepared `matched_total` |
| `traj.get` | Returned `id`, `view`, `around: 0..5` (default 3), `max_bytes: 1024..5120` | Bounded object/view, provenance, omissions and continuation |
| `traj.watch` | `selector` (sessions/events), separate `cursor`, `timeout_ms: 0..5000` | Metadata `changes`, stream `cursor`, revision, coverage, `reset_required`, `more` |
| `traj.annotate` | `operation: create/withdraw/delete`, creation fields or existing `id` | Written `record` or `deleted_id`, revision |

Methods accept named parameters only. Unknown fields, unsupported selectors and
invalid combinations produce invalid-params errors rather than being ignored.
Request bodies are limited to 64 KiB and batches to 16 calls; notifications
produce no RPC response. Application errors: `-32003` access denied, `-32002`
stale source/cursor, `-32004` not found. Standard JSON-RPC parse/request/method/
params errors retain their meanings. Encoded result budgets exclude RPC envelope
overhead. A batch shares one 6-second service deadline and rejects `count: true`;
explicit count must be sent as a single request. Watch waits are at most 5s.

### Collections and selectors

| Collection | Selection semantics | Payload |
| --- | --- | --- |
| `sessions` / `events` | `text`, `tool`, `skill`, `kind`, `role`, `agent`, `session_id`, `actor_id/kind`, `entity_kind/id`, `predicate`; optional actual-input `context_id` | Sessions or normalized events |
| `actors` | Event/source selectors, observed `actor_id/kind` | Source-backed sender/recipient observations, not a participant count |
| `relations` | Event/source selectors, `relation_kind` | Native relation nodes/edges and target-resolution status |
| `contexts` | Source/event selectors, `context_scope: archive/actual_input/workspace/query_window`, optional `context_id` | Context revisions with scope, membership, coverage and transformations |
| `entities` | Text/source/actor/event selectors, `skill`, `entity_kind/id`, `predicate` | Scoped entity descriptors and bounded example occurrences |
| `knowledge` | `text`, `tool`, `skill`, `agent`, `session_id`, `kind`, `state`, entity selectors | Source-bound observations/candidates/verification/counterexamples |
| `attention` | `agent`, `session_id`, `tool`, intervention `kind/state` | Independent observed progress, intervention evidence and coverage |

`text` is a case-insensitive intersection of query words. `tool` selects native
recorded tool names; `skill` and entity predicates select indexed occurrences.
Entity kinds are term/tool/skill/path/version. Predicates distinguish mention,
called, requested_read, read, requested_load and loaded. A mention is not a load.

Protocol role is separate from actor identity. Unknown identities are not
inferred from `role=user`. Relation kinds include parent/reply/delegation/handoff,
sent/delivered/input_inclusion, branch_parent/resume and tool_result, but only
native supported evidence generates an edge. Resolution can be missing,
ambiguous or coverage_incomplete. Time proximity and cwd create no causal edges.

Knowledge kinds are observation/candidate/verification/counterexample; states
are active/withdrawn. Evidence validity and applicability scope are independent
fields. Attention kinds are waiting_permission/waiting_input/stuck_candidate/
question_hint; its states include open_observed/closed_observed/unknown/candidate/
hint/unavailable. Its rule version and recovery conditions are returned with
results. Liveness is always unknown; question hints do not prove waiting.

A query returns at most 50 items within 64 KiB (relations/actors use 32 KiB).
Byte-limited pages advance `next` by the number actually returned. Pagination
cursors bind the selector, count mode, and observed source revision; source/index changes
require requery. Sessions report exact `matched_count`, while `matched_ids`
contains at most 50 references. These counts are not interchangeable. Sessions
also return a bounded `matched_preview` from the first matched event, tied to
`matched_ids[0]`; a title or the last action does not stand in for matching text.
Full-text pagination additionally binds the current search-projection revision.
`next` requires a verified subsequent match; unexamined candidates do not prove
another page exists.

The agreed sessions/events query contract prioritizes an accurate result page
by default, without requiring a whole-library count. Default sessions queries
scan ordered candidates until enough verified matches plus lookahead are found,
then fully verify matching events in returned sessions. Default events queries
likewise verify only through the page and lookahead. Per-session `matched_count`
and matching original-record identities remain exact. Default requests omit
`matched_total`, including lists without a text selector: absence means unknown,
not zero. Only explicit count performs complete per-source existence checks
across the authorized, prepared scope to produce the exact total.
For an exact total, explicitly set query selector `count: true` or use CLI
`query sessions --count` / `query events --count`. `matched_total` then counts
authorized, prepared matches before page limits (sessions or events, according
to the collection). It is not the whole-archive total while preparation is
pending. Count and ordinary query cursors cannot be interchanged.

Ordinary requests retain a 6-second service deadline and 7-second CLI timeout.
Single explicit count requests have a bounded, cancelable 60-second service
deadline and 61-second CLI timeout. JSON-RPC batches retain a shared 6-second
service deadline and reject `count: true`; submit counting separately.
The explicit count mode and its deadline/cancellation
behavior are being implemented; this documents the accepted contract, not a
completed acceptance result.

`coverage.index` reports `known_sources`, `decoded_sources`,
`searchable_sources`, `decoded_events`, and `searchable_events`, independent of
semantic decoder gaps. `searchable_sources` counts sources whose full observed
prefix is decoded and synchronized; other sources can already supply partial
matching prefixes. Enabled content is prepared by the app in background,
including when no query client is open.

While canonical or full-text preparation is incomplete, `index_pending` appears
in `coverage.gaps`. A zero-hit prepared prefix is not a complete no-match result.
Pending work is reported by index progress, not by `coverage.omitted`, which
continues to describe actual bounded exclusions.
Visible search may continue preparation; hidden surfaces do not poll.
Bulk indexing pauses below a 1 GiB free-disk reserve. Query error `-32009`
reports insufficient storage; committed checkpoints remain intact. The
background worker checks storage again every five seconds and resumes when
the reserve is restored. This is distinct from a source-change error.

### Evidence, views and continuation

IDs are opaque service identities. Event locators bind source/generation,
physical line, byte offset/length, block and digest. Source replacement,
truncation or changed raw records reject old locators rather than point to new
content. Protocol/native metadata remains indexed; oversized fields in a
transport preview are explicitly omitted without shortening an identity.

| Get view | ID | Meaning / continuation |
| --- | --- | --- |
| `slice` / `details` | Event or session | At most 11 nearby events, `before/after`, `truncated`, omissions; `raw: true` adds only bounded original records |
| `raw` | Event | Exact source bytes as base64 `raw_chunk`, `raw_offset`, `next_offset`, `total_bytes` |
| `relations` | Event or session | Bounded native relation neighborhood, unresolved/ambiguous/outside-window edges |
| `context` | Context ID (`ctx.`) | Manifest and bounded members; `member_offset`, optional `next_offset` |
| `attention` | Session ID | Private triage explanation with native evidence references; `around` must be 0 |
| Default object view | Entity (`ent.`) or knowledge (`k.`) ID | Descriptor/record and source references |

Get results stay within the requested 1–5 KiB budget, or explicitly fail when
the budget cannot include required provenance/scope. Inline raw strings preserve
physical UTF-8 bytes, whitespace and newline; they are never reserialized JSON.
For larger records, follow raw byte chunks (at most 2048 decoded bytes each),
then verify the digest. No archived body is permanently cached in Go memory.

Context scope distinguishes archive records, actual model input, recorded
workspace and retrieval window. Actual-input completeness requires explicit
native membership/completeness evidence; a turn or summary alone is insufficient.
Summary/compaction retains before/after and source, with unknown original members
when absent. A context filter on events/sessions selects only explicitly resolved
input members and exposes incomplete selection coverage.

### Watch continuity

Query pagination `next` and change-stream `watch_cursor` are distinct. Begin with
query, consume its snapshot, then pass `watch_cursor` to watch with the same
selector. Initial watch without a baseline requests reset. Requery on
`reset_required`; clients may deduplicate changes by observation sequence/ID.
Do not treat a JSON-RPC notification as a reliable subscription.

Watch reuses the existing filesystem notifications, with no subscription
poller. The in-memory history holds 256 metadata changes; batches hold at most
50 changes and 64 KiB. Signed cursors expire after 10 minutes. Lost windows,
expired cursors, restart, source generations, root withdrawal and observation
gaps are explicit resets. Cancel, close and access revocation wake waiting
consumers. CLI NDJSON emits one full watch batch per line, retaining reset and
coverage information.

### Knowledge writes and deterministic candidates

Create requires `kind`, `source_ids` (1–8 current event IDs), `text` (at most
2048 bytes), optional `applicability`. Verification/counterexample additionally
requires `target_id` naming an active candidate. Applicability accepts bounded
configuration/environment maps, version, evaluation_refs and artifact_refs.
Unprovided values remain absent; references are retained without being fetched
or executed. Source references are revalidated against current roots and bytes.

Withdraw takes an authored record `id`; delete removes an authored record
explicitly. Generated deterministic candidates are read-only and recomputed
from current authorized evidence. They carry rule ID/version, action/result
sequence, native outcome field, observed scope and any supported contrasting
outcomes. Causality and task completion remain unproven; missing verification
is not filled. Changed arguments need an explicit unique native relation.

There are at most 1024 authored records of at most 4 KiB each. Verification and
counterexample are separate records, not an automatic upgrade of a candidate.
Changed/withdrawn source evidence affects validity; retained notes do not keep
source bodies and do not make old conclusions current. Missing linked targets
remain visible.

### CLI examples

The same app binary supplies `traj access/query/get/watch/annotate`. It connects
to the running local instance; it never launches a second daemon. Use
`--instance-file` for a non-default private instance receipt. Query/get support
text or JSON; watch also supports NDJSON. Unreachable instances are errors.

```bash
agentload traj access on
agentload traj query sessions --text Proxy --format json
agentload traj query events --tool exec_command --format json
agentload traj query entities --skill proxy-debugger --predicate mention --format json
agentload traj query knowledge --kind candidate --state active --format json
agentload traj query attention --kind waiting_permission --format json
agentload traj query contexts --context-scope actual_input --format json
agentload traj get EVENT_ID --around 3 --raw --format json
agentload traj get EVENT_ID --view raw --raw-offset 0 --format json
agentload traj get CONTEXT_ID --member-offset 0 --format json
agentload traj get SESSION_ID --view attention --format json
agentload traj watch events --cursor WATCH_CURSOR --format ndjson
agentload traj annotate --file annotation.json --format json
agentload traj access off
```

ID/cursor placeholders are returned values. An annotation request file, for
example, is `{ "operation": "create", "kind": "observation", "source_ids":
["EVENT_ID"], "text": "Observed result, with stated limits." }`. Use `--file -`
for bounded stdin JSON. The CLI does not execute source commands or references.

### Offline index maintenance

`agentload traj compact [--history-file PATH]` runs after quitting the app. It
acquires the same history ownership lock and refuses a running instance. Its
NDJSON progress reports bounded metadata and source copy, verification, and
cutover with aggregate counters. Cancellation retains committed progress.

Persistent source identity uses the macOS volume UUID and inode. For an old
migration checkpoint whose mount device number changed and which predates an
input digest, `--expected-input-sha256 HEX` supplies the previously recorded full
original database digest. Maintenance verifies the entire original before
binding its persistent identity; a mismatch preserves the original and shadow.
The original input marker remains the recovery namespace. This option does not
enable content access or bypass source/range verification.

This operation preserves source sessions, all searchable text, event IDs,
generations, preparation checkpoints, and annotations. The accepted replacement
uses one private SQLite store for source metadata, sparse physical ranges,
bounded candidate filters, and complete facts that cannot be reproduced from
current authorized sources. Reproducible event DTOs are read on demand. Candidate
filters never establish a match; canonical evidence and exact predicates do.
Unknown recovery metadata and complete original checkpoint frames remain opaque
recovery evidence. No external compression executable is required.

Migration writes one recoverable small shadow. Each bounded write reserves its
actual data and rollback capacity, keeping at least 1 GiB available. The original
is read-only until independent fact/metadata checks, target cardinality and a
physical cutover seal pass. Only then is the replacement atomically published and
the old structure retired. A source that disappears or changes during migration
retains its original canonical facts in quarantine; it is not made queryable by
stored paths. The replacement is undergoing acceptance; the installed production
format and whole-library size must be verified before claiming migration complete.

### Incremental index and vendor coverage

Codex, Claude, Trae and Grok share the registered local discovery/watch owners;
see [capability matrix](coding-agent-evidence-adapters.md). Format-specific gaps
remain explicit. Index commits only complete physical lines, persists checkpoints
transactionally and reparses versioned projections while preserving unchanged
physical source identities. Historical events stay on disk; request windows
remain bounded. Each record is limited to 1 MiB/64 blocks and each source update
to 32 MiB. Each request prepares canonical data for at most one second and
search readiness in bounded batches; progress resumes at committed checkpoints.
All configured archived sources are queryable, independent of the resource
metrics lookback. Historical source states are not capped to a recent 512 files.

The source-backed store retains canonical source identity and physical range
evidence. Checksummed candidate filters accelerate literal text and selectors;
selected ranges reconstruct complete events through the common parser. Point reads
validate the current authorized catalog and only prepare the requested source;
opening one hit does not backfill unrelated history.

A partial discovery does not establish source deletion or advance its physical
generation. Absent sources remain inaccessible during the gap; a complete
catalog confirms removal. Removing roots removes queryable source projections
and retains missing-source markers. Disabling content stops source reads and
writes and clears in-memory content and cursors. Useful private facts, recovery
state and annotations remain on disk and inaccessible until enabled again.
The authored annotation store contains authored
text and locators, not source bodies. Raw reads verify current physical digests;
query index revisions describe observed checkpoints, not a full rehash of every
historical byte on each append. Unsupported/corrupt/partial source observations
remain coverage gaps.
