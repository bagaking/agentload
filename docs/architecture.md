# Agent Load Architecture

> Status: living document, last verified 2026-07-26.

This page is the contributor-facing map of the Go backend and the native shell.
It describes how local evidence is acquired, aggregated into a snapshot,
persisted, and delivered to the three consumers (web UI, native popover, tray).
Everything runs in one process on the loopback interface; there is no network
egress and no privileged helper. File references use file names, not line
numbers.

## Layered overview

```
            ACQUISITION                      AGGREGATION                 DELIVERY
 ps/lsof ──> process.go ─┐
             process_io* │
 ~/.claude               ├──> observer.go ──> Snapshot ──> tray.go ──> server.go ──┬─> web UI (ui/src)
 ~/.codex   transcripts.go┘   diagnostics.go   (types.go)  history.go   (sanitize) ├─> WKWebView popover
 ~/.trae/cli                  metric_semantics.go              │                   └─> tray title/menu
 OS counters ──> system_resources*.go ──> /api/system-resources│
                 system_thermal*.go                        history.jsonl
 token JSONL ──> live_token_rate.go ──┬────────────> /api/live-token-rate
                                      └─> snapshot-time history sample
```

## 1. Acquisition

### Process scan (`agent_registry.go`, `agent_process.go`, `process.go`, `process_io.go`, `process_io_darwin.go`)

- `discoverLiveProcesses` runs `ps -axo uid=,pid=,ppid=,pcpu=,rss=,etime=,command=`
  under the snapshot context and keeps rows owned by the current UID. The
  injected coding-agent registry owns executable aliases, interpreter script
  evidence, and process display identity for Claude, Codex/CodexL, Trae/TraeX,
  Gemini, and OpenCode. Updater/Sparkle processes are excluded before adapter
  matching; `node`/`bun`/`deno` commands expose only their executable script
  token, so incidental argument text cannot identify an agent.
- `inferHostApp` walks the PPID chain (bounded, cycle-safe) looking for an
  ancestor whose command points into an existing `.app` bundle; that becomes
  the process's `HostApp` (name, PID, bundle path).
- `sessionFilesForPIDs` runs one `lsof -nP -Fn -p <pid,...>` batch and asks the
  registry to classify each open path. Claude, Codex/CodexL, and Trae accept
  only their verified transcript layouts and reject known non-evidence paths;
  Gemini and OpenCode remain process-only. A partial `lsof` result is disclosed
  as a note instead of being discarded.
- `extractSessionHints` pulls session/thread ids out of the command line
  (`--session-id`, `CODEX_THREAD_ID=`, etc.) as weaker mapping evidence.
- `sampleProcessIO` (per PID) reads cumulative disk read/write byte counters
  (`proc_pid_rusage` with `RUSAGE_INFO_V2` on darwin) and derives per-second
  rates from the previous scan batch. `rotateProcessIOBatchLocked` uses the
  shared batch timestamp to evict counters of PIDs that exited between scans.

### Transcript scan (`agent_registry.go`, `agent_discovery.go`, `transcripts.go`)

`Observer.transcriptData` is the single entry point and stacks four layers:

- **Adapter registry**: `newObserver` injects one code-owned registry. Registered
  adapters own their roots and optional discovery capability; an agent without
  that capability cannot enter transcript collection. Claude discovery keeps
  verified project and workflow JSONL while pruning `memory` and `tool-results`.
  Codex and Trae discovery accepts the verified `YYYY/MM/DD` session layout and
  prunes expired date partitions before file visitation; Trae also prunes
  `*.artifacts`. Unknown dated layouts surface an evidence gap instead of
  falling back to a full recursive scan. Priority transcript files mapped from
  visible processes are stat'ed directly and do not depend on a tree walk.
- **TTL cache**: results are cached for `Config.TranscriptCacheTTL` (default
  60s) under a key derived from roots + priority files + idle gap + min
  interval + lookback. Any cached hit is deep-cloned before return.
- **Singleflight**: concurrent snapshot builds join one in-flight scan via the
  `inflight` map. A waiter whose context is cancelled returns whatever cached
  data exists and appends a `transcript scan wait cancelled` error; the scan
  itself keeps running for other waiters. A scan that finishes under a
  cancelled context is marked incomplete and is *not* written to the TTL
  cache; healthy waiters elect a new owner and retry instead of consuming its
  partial result.
- **Foreground/deferred split**: adapter discovery only collects files modified
  after the foreground cutoff (`idleGap * 80`, clamped to 2h–6h). Non-priority files
  older than that, or whose JSONL tail timestamp predates the cutoff, are
  counted as `deferred_files` and skipped. Files kept in the foreground window
  are tail-parsed (head prefix + metadata-looking middle lines + tail bytes)
  and counted as `tail_parsed_files`. Files opened by a live process are
  *priority* and always parsed fully. `historical_scan_deferred`,
  `foreground_scan_lookback_seconds`, and
  `configured_history_lookback_seconds` disclose the split in
  `transcript_stats`.
- **Incremental per-file parse**: `fileCache` remembers modtime/size/trailing
  newline and the parsed `SessionTrace` per path. Unchanged files are served
  from that cache; files that only grew (and ended with a newline, and are not
  codexl lanes) are append-parsed from the cached offset onto a clone of the
  cached trace; everything else re-parses. Cache entries whose file
  disappeared are pruned after a successful scan. A parse error can coexist
  with a degraded trace (codex lane sidecar failures): both are kept so the
  session stays visible while the error is disclosed in
  `transcript_stats.errors`.

Parsing runs in a worker pool of `min(NumCPU, 4)` goroutines. Context
cancellation marks undispatched or context-failed jobs and appends a
`transcript scan aborted early (N files not parsed)` error; such files keep no
cache entry so they are retried. The shared `WalkDir` traversal surfaces walk
failures per subtree (a permission error stays distinguishable from "no
sessions") while each adapter remains responsible for its descent rules.

Per-tool line parsers extract event timestamps, session ids, project/cwd
evidence, role metadata (`thread_source`, `parent_thread_id`, lane paths), and
token usage (both incremental and cumulative usage shapes). The scan output
also carries `SessionSpans` and `BurstSpans` (bursts segmented by
`Config.IdleGap`, spans padded to `Config.MinInterval` in `metrics.go`), which
feed historic peaks and transcript trend windows.

### Live output-token sampler (`live_token_rate.go`)

- A separate 30-second background owner discovers recently modified Claude,
  Codex, and Trae JSONL files, then advances append cursors. Appends are scanned
  line by line through the last complete JSONL record, so collector memory does
  not scale with bytes written between polls. API clients read an immutable
  published time-bucket snapshot and never own collection baselines.
- Discovery caches directory topology by directory mtime and re-reads entries
  only when names are added or removed. Vendor-owned non-transcript branches
  such as Trae `*.artifacts` trees are pruned before recursion, so historical
  session artifacts do not consume the directory budget or periodic scan time.
  Roots discovered from visible processes reuse the observer's exact priority
  transcript paths for baselines and are watcher-only while native coverage is
  available; adding a custom root therefore does not recursively index its
  historical session or lane tree.
  The observer also publishes a private session-path-to-project map to the
  sampler. API project rates partition the already sampled events through that
  map; they do not trigger another transcript read or maintain separate token
  counters. Published snapshots retain attribution only for sessions referenced
  by the active bucket buffer, so API sampling cost does not grow with historical
  session count.
  On macOS, recursive FSEvents file notifications surface newly created and
  resumed old JSONL files without recurring full-tree walks; tracked appends are
  then read from their private cursors. Dropped events fail closed and force one
  pruned index rebuild. Platforms without watcher coverage retain the bounded
  periodic rescan fallback.
- New files start from a bounded tail baseline. File identity changes, boundary
  fingerprint changes, truncation, counter rollback, and observation gaps over
  180 seconds rebaseline without replaying history. Large valid appends do not
  rebaseline: they stream from the current cursor with memory bounded by one
  JSONL line.
- Only explicit output-token fields contribute. Cumulative output counters are
  differenced per file; repeated Claude messages are differenced per
  session/message identity. Input, cache, and reasoning tokens stay outside this
  metric.
- Positive observations are folded immediately into one-second, per-session
  buckets. Sparse cumulative deltas are first distributed over their observed
  interval and then folded into the same buckets. The semantic layer clips those
  buckets to a trailing 180-second wall-time window; the result is rolling
  workload throughput, not model decode speed. Memory and API sampling work are
  bounded by window duration and contributing sessions rather than raw usage
  update frequency, so high output throughput cannot itself trip an event-count
  capacity state.
- Complete snapshot refreshes copy the sampler's current aggregate value, state,
  rolling window, and contributing-session count into the persisted history
  sample. This gives the Trend surface durable throughput points without making
  snapshot readers advance sampler cursors or baselines.
- The high-frequency capacity check is reproducible with
  `go test -run '^$' -bench '^BenchmarkLiveTokenRateBucketsThirtyTwoMillionUpdates$' -benchtime=32768000x -benchmem .`.
  It exercises 1,000 times the retired 32,768-event threshold and must retain a
  single same-second/session bucket without per-update allocations.

### System resources sampler (`system_resources.go`, `system_resources_darwin.go`, `system_thermal*.go`)

- `readSystemResourceCounters` reads whole-machine counters on darwin: CPU
  ticks (`host_statistics`), memory (`hw.memsize` + `host_statistics64`), load
  average/uptime (`getloadavg`, `kern.boottime`), root filesystem capacity
  (`statfs`), and per-interface network counters (`getifaddrs`, loopback and
  down interfaces excluded). Non-darwin builds report `supported: false`.
- `readSystemThermalState` maps `NSProcessInfo.thermalState` to
  `nominal/fair/serious/critical`; unavailability is disclosed separately
  (`thermal_state_supported`) — it is a pressure state, not a temperature.
- CPU% and network rates are deltas between two samples. A **background
  sampler** (`startSystemResourceSampler`, started by `trayApp.run`, stopped
  on exit) owns the delta baseline at a fixed 2s cadence, so rates do not
  depend on whichever client polled last. `sampleSystemResources` serves the
  latest background sample and only samples synchronously when the background
  sampler is not running (e.g. in tests). Each stop/start advances a generation,
  so a late result from an older goroutine cannot overwrite the current run.
  The first sample discloses
  "rates need two samples" in `notes` instead of reporting a fake zero.

## 2. Aggregation — `Observer.Snapshot` (`observer.go`)

One snapshot build, in order:

1. **Process discovery** (above), then `rootsFromLiveProcesses` derives extra
   config roots and priority transcript files from open file handles and
   command lines; `mergeKnownRoots` accumulates them across runs so sessions
   from non-default homes stay visible.
2. **Transcript data** via the cached scan described above.
3. **PID-to-session mapping** (`buildLiveSessionsAt`,
   `normalizeProcessSessionMappings`) with strict evidence precedence per
   process:
   1. *Parsed transcript session id* from an open transcript file (strongest;
      `mapping_method: transcript_path`, confidence high).
   2. *Filename-derived fallback id*, but only when it matches an already
      parsed session key of the same process (prevents manufacturing sibling
      sessions).
   3. *Single command hint* (`mapping_method: command_hint`, confidence
      medium). Weaker evidence that loses to parsed ids is disclosed in
      snapshot notes.
   4. *Filename-derived fallback* alone (`fallback_session_id`, confidence
      low).
   Recent transcript-backed sessions with no mapped process (last event within
   `recentSessionWindow` = `idleGap * 10`, clamped 15m–1h) are added as
   transcript-only sessions (`transcript_activity` provenance). Unmapped
   processes only contribute to PID concurrency, and that is disclosed in
   notes.
4. **Freshness model** (`observeLiveSession`): sessions with transcript timing
   are `active` (age ≤ `idle_gap`), `idle` (age ≤ `staleSessionThreshold` =
   max(3×idle gap, 5m)), or `stale`; sessions without timing are `unknown` and
   have their mapping confidence lowered. Only `active` counts as recent
   movement (`active_burst`); visible processes alone never do.
5. **needs_review rule** (`sessionNeedsReviewObservation` in
   `metric_semantics.go`): a `main`-role session with no recent movement whose
   freshness is `idle` or `stale` gets `needs_review: true`. It is an observed
   cue for human attention, not a judgment that the session is stuck.
6. **Projections**: `projectLiveSessions` (role observation, project
   attribution, durations, token usage), `projectLiveProcessesWithSessions`
   (per-PID mapped-session evidence, match methods, role splits), and
   `attachProcessResourcesToSessions`, which attributes full process
   CPU/memory to every attached session and discloses the overlap via
   `shared_process_count` so aggregates can avoid double counting.
   `buildRuntimeProcessSummary` / `buildHostAppProcessSummary` roll processes
   up per tool and per host app.
7. **Project focus** (`buildProjectFocus`): per-project aggregation with an
   attention basis that switches from `process_count` to `session_count` when
   any PID maps to multiple projects; attribution precedence is
   `transcript_project` > `transcript_cwd` > `transcript_path` >
   `config_root_parent` > `unassigned`, each with a fixed confidence level and
   human-readable reasons. `buildCandidateWorkitems` groups sessions by
   project + tool + freshness bucket, deduping process counts by PID evidence.
8. **Coordination risk** (`buildCoordinationRisk`): observed-posture signals
   only (top project share, stale sessions, unmatched processes, recent
   session churn, project spread, observed peak ratio, duplicate-overlap
   suspicion, low-confidence mapping, candidate-workitem coverage). Severity
   is always `observed`; the language stays evidence-based.
9. **Diagnostics** (`diagnostics.go`): anomaly signals (coordination risk plus
   system thresholds: CPU ≥ 85% warn, memory ≥ 85% warn, packet issue > 1%
   info), evidence gaps (unmapped PIDs, low-confidence sessions, deferred
   scans, parse errors, sampler caveats, missing token usage), baselines
   (mapping coverage ok ≥ 80 / watch ≥ 60, recent-movement share,
   low-confidence count, token-measured sessions), and capability states.
   Unavailable metrics stay unavailable; they are never converted to zero
   (`metric_registry.go` documents the missing-state contract per metric).

## 3. Persistence — history JSONL (`history.go`, `tray.go`)

- After each refresh, `trayApp.rememberSnapshot` converts the snapshot into a
  `HistorySample` (current metrics, summary, coordination-risk subset, project
  rows, runtime/host-app summaries, and the current aggregate plus per-project
  output-throughput datum) and appends one JSONL line to
  `Config.HistoryFile` (default
  `~/Library/Application Support/AgentLoad/history.jsonl`). The append runs
  under the process-local `historyFileMu` and the cross-process
  `internal/historyfile` lock *before* taking the snapshot lock, so
  `/api/snapshot` readers never wait on disk I/O. Append failures surface as
  `history.last_write_error` plus a snapshot note.
- Retention is 30 days (`historyRetentionWindow`), enforced in memory on every
  append and on load. Corrupt lines are counted, not fatal.
- **Compaction** happens at load (`loadLocalHistoryState`): when the file holds
  materially more lines than the retained window (excess > 500 lines or > 25%
  of retained), `rewriteHistorySampleFile` rewrites it atomically via a temp
  file + sync + rename. Load/compaction and append hold the same stable lock
  file, so a second app instance cannot append into the file being replaced.
- Retained samples feed `buildRealtimeTrendWindows` for process lanes,
  `buildThroughputTrendWindows` for timestamped 180-second output-rate samples,
  and `buildProjectHeatmapWindows`. All three are merged into the snapshot with
  `history` metadata. Throughput ranges preserve sparse samples and cap dense
  ranges at 240 time-distributed exact observations instead of inheriting the
  wider process buckets. Each stored throughput point retains the sampler's
  project partition; missing partitions are not migrated or reconstructed.
  Transcript lanes in `trends` come directly from span data, so the three trend
  sources stay independent.

## 4. Delivery (`server.go`, `tray.go`)

- **Refresh slots**: time is bucketed into slots of the refresh interval
  (floor 30s), with slot ids like `300s:2026-07-26T10:05:00Z`. The refresh
  loop in `tray.go` claims one slot at a time (`pendingSlot` / `activeSlot` /
  `lastSlot` dedupe), so repeated `/api/refresh` calls inside one slot are
  coalesced instead of stacking scans. Snapshot builds run under a context
  timeout of `clamp(lookback/10, 90s, 5m)` in the background loop and
  `clamp(lookback/10, 45s, 5m)` for on-demand builds.
- **ETag model**: `/api/snapshot` uses the quoted refresh slot id as the ETag
  (also exposed as `X-Refresh-Slot-ID`). Conditional requests short-circuit to
  `304` *before* the sanitize pass runs.
- **Sanitize layer**: `sanitizeSnapshotForClient` strips local roots, the
  history store path, transcript paths, and app bundle paths; reduces absolute
  paths and `file://` URLs to their base names; and redacts command lines to
  executable + flags (`--flag=<value>`) + known identity words + `...`.
  `clientSnapshotJSON` caches the sanitized, encoded snapshot per refresh
  slot, and `internal/httpencoding` provides a separately cached gzip
  representation negotiated through `Accept-Encoding`. Repeated polls within
  one slot therefore skip both sanitization and compression
  (`snapshotSanitizePasses` is the test hook for the former). See
  `api-reference.md` for per-endpoint details.
- All state-changing endpoints are POST-only and reject cross-origin browser
  calls (`sameOriginRequest`); requests without an Origin/Referer header
  (curl, the native shell) stay allowed. A top-level wrapper rejects any
  `.`/`..` path segment. The listener binds `127.0.0.1:8642` and falls back to
  an ephemeral port when the address is taken (`listenWithFallback`).

## 5. Consumers

- **Web UI** (`ui/src/main.tsx`): a single embedded React bundle serves both
  `/` (popover view) and `/dashboard`; the view is chosen by pathname. Snapshot
  transport, refresh scheduling, ETag state, visibility gating, and viewport
  restoration are owned by `ui/src/snapshot/useSnapshotController.ts`. Each
  auto-refresh cycle POSTs `/api/refresh` (passing the selected
  `interval_ms`), then polls `/api/snapshot` with `If-None-Match` until the
  returned slot appears, backing off 0.5s→8s (~15.5s budget). The cycle
  interval is user-selected (default 5m; 30s/60s/2m/5m/manual); auto refresh
  is paused entirely while the surface is hidden, and reader interaction
  (recent clicks/scrolls, expanded rows, the trend/diagnostics popover views)
  defers the next cycle to a 60s floor so the UI never repaints mid-read.
  When a hidden surface becomes visible again, a snapshot older than
  max(5m, interval) triggers an immediate refresh. The System deck
  additionally polls `/api/system-resources` every 2s while visible
  (`ui/src/system/useLiveSystemResources.ts`).
- **Native popover** (`native_popover_darwin.go` / `.m` / `.h`): on darwin+cgo
  builds the status-item click opens an NSPanel hosting a WKWebView pointed at
  the same loopback popover URL — there is **no second data path**; the
  popover is the web UI. The web view exposes two script message handlers:
  `agentLoadResize` (panel resize, clamped to the screen) and
  `agentLoadAction` (`close` / `open_dashboard` / `quit`); navigation is
  restricted to the server's own loopback origin, and external links open in
  the default browser. `native_popover_stub.go` makes non-darwin builds fall
  back to opening the dashboard in a browser.
- **Tray text rendering** (`tray.go`): the systray title shows `Idle` or
  `<active>A <sessions>S`, the template icon is an 18×18 PNG with three bars
  (active bursts / sessions / PIDs), and the tooltip plus menu rows render the
  live-field counts, top project focus, today/7d peaks, and a meta line with
  transcript-scan stats (cache hit, parsed/deferred/tail counts, foreground
  window). Menu actions: Open Dashboard, Refresh Now, Quit.

## 6. Cadence table

| What | Cadence | Owner |
| --- | --- | --- |
| Full snapshot refresh (ps + lsof + transcript scan + aggregation) | one per refresh slot; default 5m, selectable 30s/60s/2m/5m/manual; slot floor 30s | `tray.go` `refreshLoop` |
| On-demand snapshot build | only when no cached snapshot exists (cold start) | `server.go` `snapshotForInternalUse` |
| Transcript scan TTL cache | 60s (`-cache-ttl`) | `transcripts.go` |
| Foreground transcript window | `idle_gap × 80`, clamped 2h–6h (default 2h); older files deferred | `transcripts.go` |
| Background system resource sampler | 2s | `system_resources.go` |
| Background output-token sampler | 30s; trailing window 180s; stale after 5m | `live_token_rate.go` |
| UI auto-refresh cycle (POST `/api/refresh` + ETag polls) | selected refresh interval (default 5m); slot polls back off 0.5s→8s; deferred to a 60s floor while the user is reading | `ui/src/snapshot/useSnapshotController.ts` |
| UI `/api/system-resources` poll | 2s while the System deck is visible | `ui/src/system/useLiveSystemResources.ts` |
| UI `/api/live-token-rate` poll | 30s while the popover or dashboard is visible | `ui/src/live/useLiveTokenRate.ts` |
| Per-PID disk I/O rates | delta per process-scan batch | `process_io.go` |
| History JSONL append, including throughput trend datum | once per built snapshot | `tray.go`, `history.go` |
| History retention / compaction | 30d retention; compaction check at startup load | `history.go` |
| Tray title/menu/tooltip update | after each background refresh | `tray.go` |
