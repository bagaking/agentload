---
title: Agent Load Diagnostic Intelligence Architecture
---

# Agent Load Diagnostic Intelligence Architecture

This feature upgrades Agent Load without changing the product truth model:
local process evidence, local session evidence, and explicit metric semantics
remain the source of truth. UI polish, diagnostics, and future telemetry must
explain this truth; they must not invent or hide it.

## Top-Level Architecture

| Layer | Owner modules | Responsibility | Non-goals |
| --- | --- | --- | --- |
| Metric semantic registry | `metric_registry.go`, `ui/src/lib/metricSemantics.ts`, `docs/agent-load-metric-semantics.md` | Define metric families, sources, units, and missing-state behavior. | Drawing UI-specific labels or mixing semantic families. |
| Live evidence aggregation | `observer.go`, `metric_semantics.go`, `history.go` | Build current process/session/project facts, runtime breakdowns, history samples, and heatmaps. | Treating process-only rows as confirmed sessions. |
| Diagnostics model | `diagnostics.go`, `runtime_telemetry.go`, `server.go` | Produce anomaly-safe signals, evidence gaps, collector capability, runtime telemetry adapter state, and sanitized export. | Forecasting beyond available evidence or exporting private raw material. |
| UI composition | `ui/src/main.tsx`, `ui/src/diagnostics`, `ui/src/lineage`, `ui/src/system`, `ui/src/trend` | Present online workload, trend, system resources, diagnostics, lineage summary, and process evidence groups. | Encoding metric arithmetic outside semantic helpers except display-only formatting. |
| Localization and design rules | `ui/src/i18n.ts`, `ui/src/styles/*.css`, `docs/agent-load-ui-design-system.md` | Keep compact, multilingual, high-density surfaces aligned with page ownership. | Raw backend enum display in operator-facing text when a locale label exists. |
| Validation and release | `scripts/validate_locales.js`, `go test ./...`, `npm --prefix ui run build`, `build_macos_app.sh` | Verify semantic, build, locale, and packaged-app readiness before commit. | Committing generated or runtime state without intentional review. |

## Page Ownership

- Online: current meaning, scan boundary, project/session atlas, compact lineage
  summary, and hover detail.
- Trend: sampled history/runtime trend analysis and selected-window drilldown.
- System: current whole-machine resources and process evidence groups.
- Diagnostics: a first-screen situation map for recent movement, known sessions,
  visible processes, PID mapping, and token coverage; a six-row loss ledger for
  attribution gaps, coverage delay, scan cost, and history-boundary facts; then
  reviewable optimization experiments with an explicit baseline, hypothesis,
  verification, and stop condition. Anomaly/prediction-safe signals, evidence
  gaps, collector capability, optional runtime telemetry state, and safe
  diagnostic export remain below that reading flow.

Prediction/anomaly and safe export belong only to Diagnostics. Other evidence
improvements should fold into the existing Online, Trend, System, Project,
Session, and Process surfaces.

## Module Boundaries

- Backend structs expose explicit families. New metric families require a
  registry entry, semantic documentation, sanitizer review, and tests.
- Runtime telemetry is an optional adapter seam. `not_configured` is a valid
  state, not a failure. Telemetry can strengthen evidence only after a merge rule
  explains how it relates to local process and session facts.
- Token usage is measured only when local usage fields exist. Missing token
  usage stays unavailable; do not infer zero from CPU, memory, elapsed time, or
  process count.
- The loss ledger keeps deferred files and files outside the configured history
  horizon as separate states. Its rows carry the current value, evidence
  family, scope or denominator, freshness, state, source, and next inspection
  direction. These rows are display projections of existing baselines,
  evidence gaps, transcript scan cost, and metric keys; they are not a second
  metric semantic layer.
- Diagnostic export must omit raw prompts, absolute paths, full command
  arguments, environment variables, transcript paths, and bundle paths. It may
  include sanitized identity labels and documented missing-state fields.
- UI domain components should stay in focused directories. `ui/src/main.tsx`
  should remain composition and state wiring rather than a growing feature
  module.

## Expert Workstreams

- Backend SSOT and diagnostics: metric registry, aggregation contracts,
  diagnostics/export API, sanitizer, Go tests.
- Frontend architecture and visual craft: React modules, compact page ownership,
  accessibility, i18n, light/dark density, overflow behavior.
- Privacy and local safety: redaction policy, export payload, durable docs, and
  client-visible strings.
- Validation and release: build order, generated assets, app install, task gates,
  commit scope, and message audit.

Workstreams may run in parallel only when write scopes are disjoint. Shared
types, public API shapes, and semantic docs are integration points owned by the
main integrator.

## Review Loop

Each coherent implementation slice should pass independent review from the
relevant workstreams before commit. Review findings are ordered by severity:

- correctness and SSOT drift
- privacy/export leakage
- API or module-boundary breakage
- robustness and missing-state handling
- complexity, duplication, and maintainability
- UI craft, density, accessibility, and localization
- validation and release gaps

High-priority findings are fixed before commit. Medium or low-priority findings
may become follow-up tasks when they do not compromise data truth, privacy, or
installability.

## Agent evolution (RSI) surface

Diagnostics is also an evidence-led Agent evolution loop. It turns observed
session, coordination, and attribution patterns into a reviewable hypothesis,
one bounded experiment, and a next verification step. It does not score Agent
quality from process pressure or token usage because Agent Load has no task
outcome or code-review ground truth. Collector and attribution health stays a
separate evidence plane, so missing evidence is never presented as a zero
result.

## Commit Protocol

Each commit should describe:

- what changed
- validation commands that passed
- risks deliberately avoided, such as private path leakage or process/session
  semantic mixing
- follow-up plan when the slice intentionally leaves lower-priority work

After each commit, run an independent audit of the diff and message when
practical. If the audit finds durable process or design lessons, update the
matching docs page in the next slice.

## Resume Discipline

After context compaction or handoff, read this page, the metric semantics page,
the UI design system, and the active feature tracker state before continuing.
Then inspect recent diff and commit history, identify active workstreams, and
give each reviewer or worker a concrete improvement suggestion tied to its
ownership area.
