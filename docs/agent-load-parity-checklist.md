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

- [unimplemented] Process, session, project, candidate workitem, risk, and
  evidence models expose the fields needed by the popover and dashboard.
  Evidence refs: `types.go`, `metrics.go`, `evidence_model_test.go`,
  `metrics_test.go`

## Frontend Surfaces

- [unimplemented] Popover keeps compact online and trend surfaces with current
  meaning, scan boundary, project/session atlas, and trend suite.
  Evidence refs: `ui/src/main.tsx`, `ui/src/styles.css`,
  `docs/agent-load-ui-design-system.md`

- [unimplemented] Dashboard covers front status, evidence, atlas, calibration,
  age, confidence, process ledger, detail, and trend bands without relying on
  Project / Sessions / Processes as the only visual shell.
  Evidence refs: `ui/src/main.tsx`, `ui/src/styles.css`

- [unimplemented] Project rows, expanded session trees, process previews, and
  copy affordances stay compact, aligned, collapsible, and readable in dark and
  light mode.
  Evidence refs: `ui/src/main.tsx`, `ui/src/styles.css`,
  `docs/agent-load-ui-design-system.md`

- [unimplemented] Trend charts keep all samples interactive while rendering a
  quieter composed plane with selected/anchor marks, readable range labels, and
  accurate SVG-coordinate pointer selection.
  Evidence refs: `ui/src/main.tsx`, `ui/src/styles.css`,
  `docs/agent-load-ui-design-system.md`

- [unimplemented] Frontend remains TypeScript, React, Tailwind, and Vite, and
  keeps explicit light/dark mode plus visible locale switching.
  Evidence refs: `ui/package.json`, `ui/src/main.tsx`, `ui/src/i18n.ts`,
  `ui/src/styles.css`

## Packaging And Hygiene

- [unimplemented] Vite output under `ui/dist` is current and embedded by the Go
  app for tests, local serving, and packaged app builds.
  Evidence refs: `ui/dist`, `server.go`, `build_macos_app.sh`

- [unimplemented] Local app build and install smoke prove the packaged app can
  serve `/` and `/api/snapshot` from the installed bundle.
  Evidence refs: `build_macos_app.sh`, `server.go`

- [unimplemented] Locale validation, UI build, Go tests, and source-trace scans
  pass before any parity closeout.
  Evidence refs: `scripts/validate_locales.js`, `ui/package.json`, `go test ./...`

- [unimplemented] Durable repository surfaces contain no absolute local paths,
  outside provenance wording, prior product identifiers, or retired app
  identifiers.
  Evidence refs: `AGENTS.md`, `docs`, `ui/src`, `*.go`, `ui/dist`
