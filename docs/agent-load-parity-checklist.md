---
title: Agent Load Parity Checklist
---

# Agent Load Parity Checklist

Handle: `f-225rx4b7x`

This page is the shared verification checklist for Agent Load parity work. Each
item intentionally starts as `unimplemented` until a current-state audit proves
the requirement with repo-local evidence. Do not mark an item complete from
memory, chat history, or a broad green test run that does not cover the named
surface.

## Status Rules

- `unimplemented`: no accepted current-state evidence has been attached.
- `verified`: source, test, rendered UI, or runtime evidence proves the item.
- `blocked`: repeated validation shows the item cannot be completed without an
  external-state change or explicit product decision.

Status changes must cite repo-relative files, commands, screenshots stored
outside durable docs, or runtime endpoints. Shared docs must not record absolute
machine paths, outside provenance labels, or prior product identifiers.

## Backend Observation

- [verified] Transcript scanning preserves early session metadata while
  reading recent tail activity.
  Evidence refs: `transcripts.go`, `transcripts_test.go`, `observer_test.go`
  Verified by: `transcripts.go:602`, `transcripts.go:654`,
  `transcripts.go:701`, `transcripts.go:762`, `transcripts.go:796`,
  `transcripts_test.go:160`, `transcripts_test.go:191`,
  `transcripts_test.go:223`, `transcripts_test.go:255`,
  `transcripts_test.go:277`, `transcripts_test.go:295`,
  `observer_test.go:130`, `observer_test.go:489`.
  Check: `go test ./... -run 'TestParseTranscriptFileTail|TestFileMayContainEventsAfterCutoff|TestForegroundTranscriptScan.*Tail|TestForegroundTranscriptScanUsesTailParser'`.

- [verified] Foreground snapshot work can defer historical parsing while
  still including live process files and foreground-window transcripts.
  Evidence refs: `observer.go`, `transcripts.go`, `observer_test.go`
  Verified by: `transcripts.go:92`, `transcripts.go:155`,
  `transcripts.go:253`, `transcripts.go:411`, `transcripts.go:429`,
  `observer.go:71`, `observer.go:110`, `observer.go:116`,
  `observer_test.go:351`, `observer_test.go:433`,
  `observer_test.go:489`.
  Check: `go test ./... -run 'TestForegroundTranscriptScan|TestSnapshotNotesDescribeDeferredHistoricalParsing'`.

- [verified] Snapshot API returns consistent refresh-slot metadata for
  cached snapshots, first on-demand snapshots, manual refreshes, HEAD requests,
  and validator responses.
  Evidence refs: `server.go`, `server_test.go`, `history.go`,
  `history_test.go`
  Verified by: `server.go:75`, `server.go:111`, `server.go:220`,
  `server_test.go:70`, `server_test.go:185`, `server_test.go:215`,
  `server_test.go:251`, `server_test.go:308`, `history_test.go:349`.
  Check: `go test ./... -run 'TestHandleRefreshAPI|TestHandleSnapshotAPI|TestRefreshSlotID'`.

- [verified] Tray metadata exposes parsed, scanned, deferred, tail,
  foreground-window, and deferred-history scan coverage.
  Evidence refs: `tray.go`, `server_test.go`
  Verified by: `tray.go:377`, `tray.go:386`, `tray.go:391`,
  `tray.go:398`, `server_test.go:618`.
  Check: `go test ./... -run 'TestFormatTrayMetaTitleIncludesScanCoverage'`.

- [verified] Client snapshot output removes local roots, session paths,
  bundle paths, command arguments, history store paths, and path-like evidence
  while internal cached snapshots retain local evidence for diagnostics.
  Evidence refs: `server.go`, `server_test.go`
  Verified by: `server.go:212`, `server.go:242`, `server.go:321`,
  `server.go:339`, `server.go:373`, `server.go:417`,
  `server_test.go:335`, `server_test.go:377`, `server_test.go:506`,
  `server_test.go:525`, `server_test.go:550`, `server_test.go:846`.
  Check: `go test ./... -run 'TestHandleSnapshotAPIRedacts(ConfigPaths|ClientEvidencePaths|FreshObserverConfigPaths)|TestSanitize(TextForClient|CommandForClient)|TestObservedHostAppFromRequestUsesInternalFreshSnapshot'`.

- [verified] Process, session, project, candidate workitem, risk, and
  evidence models expose the fields needed by the popover and dashboard.
  Evidence refs: `types.go`, `metrics.go`, `evidence_model_test.go`,
  `metrics_test.go`
  Verified by: `types.go:8`, `types.go:64`, `types.go:78`,
  `types.go:155`, `types.go:220`, `types.go:232`, `types.go:268`,
  `types.go:284`, `types.go:314`, `observer.go:703`,
  `observer.go:753`, `observer.go:996`, `observer.go:1121`,
  `observer.go:1197`, `observer.go:1386`, `observer.go:1531`,
  `metrics.go:181`, `metrics.go:285`, `metrics.go:409`,
  `evidence_model_test.go:12`, `evidence_model_test.go:268`,
  `evidence_model_test.go:334`, `evidence_model_test.go:610`,
  `evidence_model_test.go:707`, `evidence_model_test.go:889`,
  `metrics_test.go:180`, `metrics_test.go:230`,
  `metrics_test.go:481`.
  Check: `go test ./... -run 'TestBuildLiveSessions(TracksMappingEvidence|IncludesRecentTranscriptOnlySubagents|PropagatesHostApps)|TestProjectLiveSessionsExposeFreshnessConfidenceAndProvenance|TestBuildProjectFocus(AddsAllocationRiskAndConfidenceSummary|KeepsTranscriptProjectAndCWDHighConfidence)|TestBuildCandidateWorkitemsAndCoordinationRisk|TestBuildCoordinationRisk(CountsMissingTranscriptSessionsInLowConfidenceSignal|IgnoresUnassignedBucketsForProjectSpread|CountsDuplicateOverlapConservatively|SummarizesCandidateCoverageAndConfidence)|TestBuildTranscriptTrendWindowsMarksSampledMetricPresence|TestTrendPointMarshalJSON(KeepsSampledHistoryZeroMetrics|KeepsSampledRuntimeZeroMetrics|OmitsMissingSampledHistoryMetric|OmitsMissingSampledRuntimeMetric)|TestMergeRuntimeTrendsMarksSampledMetricPresence' -count=1`.

## Frontend Surfaces

- [unimplemented] Popover keeps compact online and trend surfaces with current
  meaning, scan boundary, project/session atlas, and trend suite.
  Evidence refs: `ui/src/main.tsx`, `ui/src/styles.css`,
  `docs/agent-load-ui-design-system.md`

- [unimplemented] Dashboard covers front status, evidence, atlas, calibration,
  age, confidence, process ledger, detail, and trend bands without relying on
  Project / Sessions / Processes as the only visual shell.
  Evidence refs: `ui/src/main.tsx`, `ui/src/styles.css`

- [verified] Project rows, expanded session trees, process previews, and
  copy affordances stay compact, aligned, collapsible, and readable in dark and
  light mode.
  Evidence refs: `ui/src/main.tsx`, `ui/src/styles.css`,
  `docs/agent-load-ui-design-system.md`
  Verified by: `ui/src/main.tsx:1310`, `ui/src/main.tsx:1335`,
  `ui/src/main.tsx:1360`, `ui/src/main.tsx:1579`,
  `ui/src/main.tsx:1629`, `ui/src/main.tsx:1635`,
  `ui/src/main.tsx:2423`, `ui/src/main.tsx:2426`,
  `ui/src/main.tsx:2537`, `ui/src/main.tsx:2577`,
  `ui/src/main.tsx:2584`, `ui/src/main.tsx:2615`,
  `ui/src/main.tsx:2648`, `ui/src/main.tsx:2702`,
  `ui/src/main.tsx:2720`, `ui/src/styles.css:6891`,
  `ui/src/styles.css:6902`, `ui/src/styles.css:6998`,
  `ui/src/styles.css:7030`, `ui/src/styles.css:7069`,
  `ui/src/styles.css:7107`, `ui/src/styles.css:7160`,
  `ui/src/styles.css:7171`, `ui/src/styles.css:7208`,
  `ui/src/styles.css:7526`, `ui/src/styles.css:7539`,
  `docs/agent-load-ui-design-system.md:132`,
  `docs/agent-load-ui-design-system.md:136`,
  `docs/agent-load-ui-design-system.md:142`,
  `docs/agent-load-ui-design-system.md:148`,
  `docs/agent-load-ui-design-system.md:151`.
  Check: browser smoke with mocked dense project/session/process data covered
  popover and dashboard in dark and light themes. It confirmed project
  disclosure opens and closes, compact popover session rows stay at 21px
  without wrapping, dashboard session rows stay within the 46px budget, session
  id copy buttons remain inside their identifier controls, and process session
  preview chips expand from `+n` into the full mapped-session set.

- [verified] Trend charts keep all samples interactive while rendering a
  quieter composed plane with selected/anchor marks, readable range labels, and
  accurate SVG-coordinate pointer selection.
  Evidence refs: `ui/src/main.tsx`, `ui/src/styles.css`,
  `docs/agent-load-ui-design-system.md`
  Verified by: `ui/src/main.tsx:1807`, `ui/src/main.tsx:1819`,
  `ui/src/main.tsx:1861`, `ui/src/main.tsx:1866`,
  `ui/src/main.tsx:1888`, `ui/src/main.tsx:1891`,
  `ui/src/main.tsx:3794`, `ui/src/main.tsx:3808`,
  `ui/src/main.tsx:3810`, `ui/src/styles.css:9586`,
  `ui/src/styles.css:9602`, `ui/src/styles.css:9616`,
  `ui/src/styles.css:9621`, `ui/src/styles.css:9635`,
  `ui/src/styles.css:9657`,
  `docs/agent-load-ui-design-system.md:179`,
  `docs/agent-load-ui-design-system.md:184`,
  `docs/agent-load-ui-design-system.md:191`,
  `docs/agent-load-ui-design-system.md:197`.
  Check: `npm --prefix ui run build`; browser smoke with mocked trend data
  confirmed 2 charts, 60 hidden sample hit targets for 60 raw samples, one
  continuous primary path per lane, compact callouts suppressed, two soft
  selection bands, and pointer selection updating the lane readout.

- [verified] Frontend remains TypeScript, React, Tailwind, and Vite, and
  keeps explicit light/dark mode plus visible locale switching.
  Evidence refs: `ui/package.json`, `ui/src/main.tsx`, `ui/src/i18n.ts`,
  `ui/src/styles.css`
  Verified by: `ui/package.json:5`, `ui/package.json:8`,
  `ui/package.json:12`, `ui/package.json:20`, `ui/src/main.tsx:1`,
  `ui/src/main.tsx:20`, `ui/src/main.tsx:294`,
  `ui/src/main.tsx:472`, `ui/src/main.tsx:479`,
  `ui/src/main.tsx:2037`, `ui/src/main.tsx:2038`,
  `ui/src/main.tsx:2887`, `ui/src/main.tsx:3555`,
  `ui/src/main.tsx:3583`, `ui/src/i18n.ts:1`, `ui/src/i18n.ts:3`,
  `ui/src/i18n.ts:245`, `ui/src/i18n.ts:472`,
  `ui/src/styles.css:1`, `ui/src/styles.css:57`,
  `ui/src/styles.css:6780`, `ui/src/styles.css:6800`.
  Check: `node scripts/validate_locales.js`; `npm --prefix ui run build`.

## Packaging And Hygiene

- [verified] Vite output under `ui/dist` is current and embedded by the Go
  app for tests, local serving, and packaged app builds.
  Evidence refs: `ui/dist`, `server.go`, `build_macos_app.sh`
  Verified by: `ui/dist/index.html:1`, `ui/dist/index.html:9`,
  `ui/dist/index.html:10`, `server.go:26`, `server.go:29`,
  `server.go:49`, `server.go:57`, `server.go:65`,
  `build_macos_app.sh:13`, `build_macos_app.sh:18`,
  `build_macos_app.sh:23`, `server_test.go:18`,
  `server_test.go:45`.
  Check: `npm --prefix ui run build`; `go test ./... -run 'TestHandleUIAsset(ServesViteAssets|RejectsInvalidAssetPaths)' -count=1`.

- [verified] Local app build and install smoke prove the packaged app can
  serve `/` and `/api/snapshot` from the installed bundle.
  Evidence refs: `build_macos_app.sh`, `server.go`
  Verified by: `build_macos_app.sh:13`, `build_macos_app.sh:18`,
  `build_macos_app.sh:23`, `server.go:49`, `server.go:75`,
  `server_test.go:185`.
  Check: `./build_macos_app.sh`; installed-app smoke returned
  `GET / -> 200 text/html; charset=utf-8` and
  `GET /api/snapshot -> 200 application/json; charset=utf-8`.

- [verified] Locale validation, UI build, Go tests, and source-trace scans
  pass before any parity closeout.
  Evidence refs: `scripts/validate_locales.js`, `ui/package.json`, `go test ./...`
  Verified by: `scripts/validate_locales.js:113`,
  `scripts/validate_locales.js:153`, `scripts/validate_locales.js:171`,
  `scripts/validate_locales.js:172`, `scripts/validate_locales.js:173`,
  `ui/package.json:8`, `server.go:26`, `server.go:49`,
  `server.go:57`, `build_macos_app.sh:13`,
  `build_macos_app.sh:18`.
  Check: `node scripts/validate_locales.js`;
  `npm --prefix ui run build`; `go test ./...`; source-trace scan for
  forbidden provenance, legacy naming, and local-path terms returned no matches.

- [verified] Durable repository surfaces contain no absolute local paths,
  outside provenance wording, prior product identifiers, or retired app
  identifiers.
  Evidence refs: `AGENTS.md`, `docs`, `ui/src`, `*.go`, `ui/dist`
  Verified by: `AGENTS.md:1`, `AGENTS.md:20`, `AGENTS.md:22`,
  `AGENTS.md:47`, `docs/must-authority.md:21`,
  `docs/must-authority.md:28`, `docs/must-authority.md:30`,
  `docs/agent-load-parity-checklist.md:22`.
  Check: durable-surface scan over `AGENTS.md`, `docs`, `ui/src`,
  `ui/dist`, Go files, and `scripts` returned no forbidden local-path,
  provenance, prior-product, or retired-identifier matches.
