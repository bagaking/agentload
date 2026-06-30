---
title: Agent Load UI Design System
sop:
  - When changing `ui/src`, preserve the popover/dashboard split and keep Project / Sessions / Processes available as navigation, not as the only visual shell.
  - Keep visual tokens aligned with the console design language: dark material surfaces, blue primary accent, green/yellow/red semantic states, compact bands, and dense evidence panes.
  - Before committing UI changes, run `node scripts/validate_locales.js` and `go test ./...`.
  - When review feedback reveals a missing durable UI rule, update this design system in the same change.
---

# Agent Load UI Design System

Agent Load is an operator console, not a generic dashboard or landing page.
The UI should feel like a local developer console for inspecting machine-local
agent evidence.

## Required Structure

- top bar with brand, refresh, language, and theme controls. Dashboard chrome may
  expose loopback/no-upload status, but compact popover live state belongs in the
  footer timestamp/cadence area so the title cluster stays action-focused.
- popover surface with an online/trend switch, current meaning strip, scan
  boundary, compact project/session atlas, and trend suite
- popover language control remains visible in compact mode; locale switching is
  a first-class operator control, not a dashboard-only setting. Direct links may
  specify `?lang=`, and the page-level `lang` attribute should use the resolved
  locale rather than a generic fallback.
- Compact popover chrome should not spend primary horizontal space on the full
  loopback address; keep local-only status available through a small status mark,
  tooltip, or detail surface.
- dashboard surface with a compact report masthead, front status, evidence
  column, project/session atlas, calibration rail, age rail, confidence grid,
  process ledger, and trend suite
- dashboard uses document-level scrolling as a full report surface. Do not lock
  it to a single viewport or hide lower audit bands behind nested panel scroll;
  the popover is the constrained-height surface.
- Project / Sessions / Processes navigation remains available as a dense
  inspector band for focused lookup
- Inspector navigation lists may start compact, but capped Project / Sessions /
  Processes results must expose an open/close overflow control so focused
  lookup never silently drops reachable observations.
- main/detail panes may show result-style headers and evidence text, but should
  not dominate the first dashboard viewport
- The dashboard masthead is the first report-level identity block. It should
  show the Agent Load brand, current observation state, generated time,
  refresh cadence, and refresh action without becoming a marketing hero or
  pushing runtime evidence below the fold.
- Snapshot load or refresh failures must appear in the active surface as a
  compact warning banner. The top bar status chip is useful ambient state, but
  it is not enough for dashboard or popover error recovery.

## Visual Rules

- Prefer flat material surfaces over nested card stacks.
- Keep padding tight enough for repeated operational use.
- Use thin separators and depth changes; avoid heavy line-box scaffolding and
  oversized framed logs.
- Popover first-viewport status areas should not stack multiple visible frames.
  The top bar, live-state pill, runtime summary, metric cells, and scan readouts
  should rely on planar fills, subtle separators, and state color rather than
  each drawing its own strong border.
- Compact popover metric cells should read as translucent planar instruments,
  not solid cards. Use subtle material tint, one-pixel separation, and semantic
  color emphasis so the numeric readout stays prominent without adding box
  chrome.
- Metric clusters should avoid unexplained decorative spines, dots, or rails.
  If activity needs visual atmosphere, use a faint background motion layer whose
  color follows the active state while leaving the data and labels unobstructed.
- Popover primary metrics should not reserve a full line for duplicated ambient
  state. The compact footer status mark owns live/active hover text next to the
  observation timestamp and cadence; foreground scan-window duration belongs
  with scan-boundary metadata unless it becomes an exceptional warning.
- Compact status hover text should state the current observation state only.
  Loopback address and no-upload reassurance are detail metadata; do not show
  them in default hover text where they can cover primary readings.
- In compact popover chrome, manual refresh may sit with the brand/title cluster,
  but live state should not duplicate the footer status mark. The right control
  group should stay focused on language, theme, dashboard, and window actions.
- Active live-state marks should breathe subtly when local movement is present.
  Prefer slow, soft core-and-ring motion from the footer mark; the animation must
  read as ambient state feedback, not a decorative loading spinner or loading
  control.
- The compact popover footer should read as one flat control plane with timestamp,
  cadence, view switch, and dashboard launch composed into a single graphic band.
  Avoid separate chunky pills in the footer; use one-pixel dividers, planar
  accents, restrained hover states, and theme-specific contrast so the strip
  feels like an intentional control artifact rather than leftover chrome.
- When a compact popover leaves vertical slack below the audit list, treat the
  lower area as a quiet composed tail plane, not dead empty space. Use subtle
  fades, one-pixel rhythm marks, and the footer's state color to connect the
  content area to the control plane without adding fake data or new frames.
- Compact explanatory rows should avoid decorative icons when the label and
  adjacent controls already identify the row. Compact scan readouts should stay
  on one row whenever the available width can hold the observed fields.
- Favor scan-line density for audit lists. Primary rows should expose only the
  fields needed to understand global state; secondary identifiers such as full
  local addresses, long paths, and verbose evidence belong in tooltips, detail
  panes, or disclosure surfaces.
- Dashboard process ledgers may keep a compact initial row window, but any cap
  must expose the hidden count through an open/close overflow control; observed
  process totals must never imply a fuller table than the operator can reach.
- Compact project rows should expose role mix, active/all session totals,
  process count, and observed tool coverage before expansion. Expansion is for
  relationship inspection, not the first moment when distribution becomes
  visible.
- Compact project rows should not compress repeated counts into tiny matrix
  walls. When role/session/process numbers become dense, group them into a few
  readable ledger chips, use modest row breathing room, and add subtle staggered
  planes or tonal rhythm instead of adding more grid labels.
- Compact project ledgers should read as aligned scan rows, not rows of
  button-like metric blocks. Use fixed columns, one-pixel dividers, faint
  alternating planes, and restrained state color so row separation is legible
  without making the list look chunky.
- Compact project-row backgrounds may vary by tone, but their leading and
  trailing edges must stay on the same grid. Do not stagger row planes when the
  list is behaving like a ledger; rank, disclosure, project identity, tool
  coverage, and all large metric numerals must remain column-aligned.
- When compact project/session rows start reading as a continuous text wall,
  restore rhythm with small vertical breathing room, alternating plane offsets,
  and quieter metric surfaces before shrinking typography further. The target is
  still scan density, not card-like looseness.
- Project lists must not silently hide known projects. The compact popover
  should preserve the current project set, and any intentionally capped project
  list must expose a dense open/close overflow control with the hidden count.
- Expanded project rows must keep sessions in a compact tree outline. Avoid
  block-level evidence stacks inside the popover; global audit should not
  require scrolling past full evidence cards to understand parent/child session
  shape.
- Tree selection and disclosure state are separate. Selecting a project or
  session must not make it impossible to collapse the row, and overflow labels
  such as `more` counts must be interactive open/close controls instead of dead
  summary text.
  Compact previews that show `+n` or hidden-item counts must use the same
  open/close behavior, including process-to-session preview chips.
- Popover project rows must stay single-line when collapsed: reserve explicit
  columns for rank, disclosure, project identity, metrics, and tool coverage so
  tool badges never wrap into a second row. Expanded selection rails should be
  one-pixel guides offset from text, not thick bars over content.
- Compact project tool coverage must reserve enough right gutter for the visible
  icon/count pair. Do not use fade masks that make the final count look clipped.
- Expanded popover session rows should fit role, agent mark, host mark, short id,
  last activity age, process count, and confidence onto one scan line whenever
  the width allows it. Detail panels may carry longer evidence.
- Row actions in compact session lists and dashboard session trees should attach
  to the identifier they act on. Copy controls belong inline with the session id,
  with hover/focus emphasis and reserved width, rather than as a separate grid
  item that can wrap.
  Treat the session id and copy affordance as one no-wrap identifier component;
  reveal the copy control on identifier hover/focus instead of letting it sit
  after metadata or fall to a second line.
- Use icons for tabs, commands, status, and metrics where they reduce text load.
- Core runtime terms such as fresh movement, sessions, processes, mapping
  health, and scan state should expose short hover/focus explanations so dense
  operator views stay readable without adding permanent copy.
- Dense explanation blocks should default to a compact lead sentence and expose
  full details through an accessible disclosure control instead of permanently
  occupying popover height.
- Trend selections should expose the selected time and compact numeric readout
  first. Longer interpretation and trust explanations belong behind an
  accessible disclosure control, especially in the popover.
- Trend selection readout cells follow the same translucent instrument rule as
  compact metric cells. They should sit above the chart as light material
  overlays rather than opaque cards that compete with plotted values.
- Compact trend views should show both lanes and the selected readout within the
  first reading pass. Do not repeat bulky selected-bucket cards under every
  lane; use one shared inspector strip and keep per-lane readouts inline with
  the lane header.
- Compact trend typography should stay readable at popover size. Range controls,
  lane titles, sample metadata, selected time, and selected values need one
  clear step above axis and callout microtext; do not shrink them to the point
  where the chart becomes legible but its controls feel like footnotes.
- Trend charts are audit controls, not presentation cards. Use flat chart
  planes, thin separators, short range controls, and dense click targets so the
  operator can compare lanes without scrolling through repeated explanation
  panels.
- Trend axes should expose compact in-between time ticks when the chart has
  enough width. These segment labels should be faint ledges for reading rhythm,
  and should yield when they collide with the selected time label.
- Trend chart callouts must render above series points and point hit targets,
  and must not intercept chart clicks. Dense point clusters should still allow
  nearest-bucket selection by clicking the chart plane.
- Trend chart pointer selection must use the SVG viewBox coordinate transform,
  not raw element width ratios, so clicked positions match plotted points even
  when the SVG letterboxes or scales responsively.
- Compact trend charts should use the available popover width for the visible
  sampled series. Do not let incomplete source windows reserve large blank
  horizontal ranges that make the plotted trend look artificially narrow; keep
  full-window context in labels and detail metrics instead.
- Light mode must keep weak labels, icons, tree rails, and control text readable;
  do not rely on very pale gray text for operator-critical controls.
- Auto refresh defaults to `5m`. A paused refresh state may exist, but it must be
  labeled as refresh pause/off and must not be conflated with an idle session
  state.
- The compact top bar should not repeat ambient idle state when the footer
  already exposes timestamp and refresh cadence. Show top-bar status only when
  it is actionable or exceptional, such as refreshing or failed.
- Observation timestamp areas should expose the user-facing refresh cadence as
  a compact click-to-cycle control. Raw refresh slot identifiers belong in
  protocol metadata or diagnostics, not in primary dashboard chrome.
- Multilingual UI copy is part of the design surface. New operator-facing text
  must be added to every supported locale with matching placeholder tokens.
- Locale resources should stay in `ui/src/i18n.ts`; UI components should call
  the translation helper instead of embedding language-specific copy inline.
- Review feedback is design input. If a critique exposes a reusable rule about
  hierarchy, density, contrast, controls, terminology, or auditability, fold it
  back into this document instead of leaving it only in chat history.
- Use the token family in `ui/src/styles.css` as the source of truth:
  - primary accent: refined blue
  - local movement / mapped / ok: mint green
  - running / attention / sampled process activity: brass amber
  - mismatch / error: coral red
  - secondary operator surfaces: graphite in dark mode and porcelain slate in
    light mode
  - dark and light themes must define their own semantic tokens instead of
    relying on a single accent color with automatic inversion

## Implementation Contract

- Source UI lives under `ui/src`.
- Vite output lives under `ui/dist` because Go embeds it for `go test`, `go run`,
  and packaged app builds.
- `build_macos_app.sh` must build the UI before compiling the Go app.
- The app name shown to users is `Agent Load`; the module, executable, and
  configuration namespace stay `agentload`.
