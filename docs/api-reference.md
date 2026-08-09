# Agent Load API Reference

> Status: living document, last verified 2026-07-26.

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
- **Throughput trends**: numeric `throughput_trends.windows[].points[]`
  throughput samples expose `output_tokens_per_second`,
  `output_token_throughput_state`,
  `output_token_throughput_window_seconds`,
  `output_token_active_sessions`, `output_token_projects`, and
  `throughput_sampled`. Each project item uses the same events and 180-second
  denominator as the aggregate, and the items sum to that aggregate. `stale`,
  `no_data`, and `unavailable` points omit the numeric rate and project list.
  Samples without a stored project partition are not reconstructed. Each range
  filters the stored time series independently from process bucketing. Sparse
  ranges retain all samples; dense ranges retain up to 240 time-distributed
  exact samples.
- **Sanitization** (`sanitizeSnapshotForClient`): see "Sanitize layer" below.

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
`file_capacity`, `directory_capacity`, `watch_incomplete`, or
`watch_pending_capacity`. High token volume and raw usage-update frequency do
not produce an unavailable state.
For numeric samples, `projects` contains positive per-project contributions from
the same event window (`project`, `output_tokens_per_second`, and
`active_sessions`); an empty array means measured zero. Missing or conflicting
attribution is reported as project `unassigned`, so the project partition sums
to the aggregate.
The denominator is the fixed trailing 180-second wall-time window. This is
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
  path; `502` with the `open` output when launching fails.

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
