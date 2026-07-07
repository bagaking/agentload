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

Data truth is the first design constraint. The interface may improve density,
hierarchy, and visual craft, but it must not alter metric meaning or make
separate semantic families look equivalent. Header numbers, project rows, trend
charts, hover panels, and detail inspectors must agree through the metric
semantic layer before visual polish is accepted.

## Required Structure

- top bar with brand, refresh, language, and theme controls. Dashboard chrome may
  expose loopback/no-upload status, but compact popover live state belongs in the
  footer timestamp/cadence area so the title cluster stays action-focused.
- popover surface with online/trend/system/diagnostics navigation. Online owns
  current meaning, scan boundary, and compact project/session atlas. Trend owns
  historical/runtime chart analysis. System owns whole-machine resource samples
  and process diagnostics. Diagnostics owns anomaly/prediction-safe signals,
  metric collection capability, evidence gaps, semantic contract readouts, and
  safe diagnostic export.
- popover language control remains visible in compact mode; locale switching is
  a first-class operator control, not a dashboard-only setting. Direct links may
  specify `?lang=`, and the page-level `lang` attribute should use the resolved
  locale rather than a generic fallback.
- Compact rows and dashboard readouts must localize visible enum values such as
  confidence, freshness, mapping method, thread source, and agent role. Raw API
  tokens may remain in evidence/log contexts, but scan rows should not expose
  values such as `high` or `transcript_path` when a locale label exists.
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
- Compact footer timestamp and refresh cadence are primary status readouts and
  must keep a protected width across locales. Dashboard launch is a secondary
  shortcut in this surface; use an icon-only button with accessible label rather
  than visible "Dashboard" text when the view switch already consumes the right
  side of the footer.
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
- Process counts are PID concurrency, not session concurrency. UI labels and
  detail text should make unmapped process contribution visible so a high
  process number is not mistaken for confirmed session load.
- Primary workload readouts must not treat raw PID concurrency as an active
  agent count. First-level status should emphasize recent local-log movement,
  known sessions, and the PID match rate. Avoid vague judgment labels such as
  "mapping health"; the visible label should state that the percentage is the
  share of visible PIDs matched back to sessions. Raw process totals belong in
  a diagnostic pressure strip or process ledger using a formula such as
  `PID = matched to sessions + unmatched`.
- Compact popover online and trend views should not carry process ledgers or
  whole-machine resource dashboards. Put process diagnostics, system CPU,
  memory, and network fluctuation into the system view so workload evidence and
  machine pressure stay visually and semantically separate.
- Diagnostics is the only compact page for anomaly/forecast signals and export.
  Do not duplicate these controls into Trend or System. Trend may link runtime
  drilldowns to persisted samples; System may show current process evidence; the
  Diagnostics page explains whether the evidence is complete enough to trust.
- Diagnostics must read as a fact-check and local evidence inspection surface,
  not an AI report, generic health dashboard, or card pile. Put observed
  evidence quality first, keep forecasting explicitly unavailable unless a real
  model exists, and express every priority row as plain user-facing
  issue/evidence/source/next-check language. Backend diagnostic keys may appear
  only as compact source badges or export evidence, never as the primary title
  in a localized UI.
- Diagnostics layout should use a small set of reusable planes: an evidence
  strip, a priority-check table, an evidence-chain map, and a safe-export
  boundary. Avoid one-off diagnostic cards that repeat the same visual frame or
  make the page feel generated rather than deliberately instrumented.
- Compact diagnostics must preserve first-screen audit density. The evidence
  strip should stay horizontal in the popover whenever labels and numbers can
  still fit; move explanatory badges into the heading or table chrome instead
  of letting one badge force metric cards into a tall stacked layout.
- Compact popover tab panels share one content rhythm. Online, trend, and
  system views should use the same first-level side inset, top/bottom padding,
  section gap, heading scale, and secondary text scale; individual charts,
  resource meters, and ledger rows may specialize internally, but the content
  plane under the tab switch must not jump between tabs. Align this rhythm to
  the tightest usable inset and available-width fill; do not make tabs match by
  adding a new bulky wrapper padding around every view.
- Compact popover trend view should prioritize session movement and project
  distribution. Do not place raw process/runtime trend readouts or a process
  selected-window inspector under the project heatmap; those diagnostics belong
  in the system view. The project heatmap must have enough area to read project
  proportions without feeling like a compressed footer.
- Compact system view should use metric-appropriate components instead of one
  repeated card form: CPU may use a gauge with load/uptime, memory and disk use
  capacity rails, network foregrounds inbound/outbound throughput with local
  packet issue counters, and Agent process load uses process-oriented rows.
  Process rows in the system view should show project attribution directly so
  high CPU or memory can be traced without expansion.
- Compact system resource color must be metric-state driven. Capacity rails,
  CPU gauges, and throughput bars should derive fill, track, glow, and tile
  wash from the same semantic pressure color for that metric. Avoid neutral
  gray tracks, dirty yellow-blue blends, and decorative grids that make light
  mode look muddy or imply an unrelated scale.
- System resource labels and metadata remain audit text, not decoration. Keep
  them small but readable, with stable line height and no clipping; if a metric
  card compresses, wrap or reduce secondary metadata before cropping the
  primary label or value.
- Compact system process rows should use a two-line diagnostic structure. The
  first line carries tool/process identity and project attribution without
  losing the names; the second line carries PID, host app, CPU, memory, and I/O
  rate. Tool or host icons should identify the process before the text.
  The second line should be fixed metric rails, not a prose sentence, so disk
  read/write values cannot wrap into visually unrelated fragments.
- Compact trend may keep a process trend lane, but it should use a simpler
  curve or similar low-noise chart rather than a candlestick when the point is
  runtime pressure rather than session movement.
- Unmatched or unmapped processes still count for diagnostics, coverage, trend
  risk, and process ledgers, but they must not be counted as active agents or
  confirmed workload until they are mapped back to local session evidence.
- Tool process recognition should use explicit executable aliases for known
  local agents and avoid broad prefix matches that pull in unrelated commands.
  When a tool has multiple launchers, keep the alias set covered by backend
  tests before changing chart or ledger copy.
- Compact project rows should expose role mix, active/all session totals,
  process count, and observed tool coverage before expansion. Expansion is for
  relationship inspection, not the first moment when distribution becomes
  visible.
- Compact project rows should not compress repeated counts into tiny matrix
  walls. When role/session/process numbers become dense, group them into a few
  readable ledger chips, use modest row breathing room, and add subtle tonal
  rhythm instead of adding more grid labels.
- Compact project ledgers should read as aligned scan rows, not rows of
  button-like metric blocks. Use fixed columns, one-pixel dividers, faint
  alternating planes, and restrained state color so row separation is legible
  without making the list look chunky.
- Compact project-row backgrounds may vary by tone, but their leading and
  trailing edges must stay on the same grid. Do not stagger row planes when the
  list is behaving like a ledger; rank, disclosure, project identity, tool
  coverage, and all large metric numerals must remain column-aligned.
  Background tint may indicate state, but the tint must fill the same row track
  instead of fading at different horizontal stops or ending behind the project
  label like a variable-width chip. Large row numerals must live in fixed
  tabular columns with shared right edges across every visible project row.
- When compact project/session rows start reading as a continuous text wall,
  restore rhythm with small vertical breathing room, alternating plane tones,
  and quieter metric surfaces before shrinking typography further. The target is
  still scan density, not card-like looseness.
- Project lists must not silently hide known projects. The compact popover
  should preserve the current project set, and any intentionally capped project
  list must expose a dense open/close overflow control with the hidden count.
- Expanded project rows must keep sessions in a compact tree outline. Avoid
  block-level evidence stacks inside the popover; global audit should not
  require scrolling past full evidence cards to understand parent/child session
  shape.
- Expanded project rows should include a compact lineage summary when space
  allows. The summary may show direct/subagent mix, linked/unlinked branch
  shape, process pressure, tool lanes, and measured token-session coverage, but
  it must not replace the single-line session tree or promote process-only
  evidence into confirmed sessions.
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
- Compact popover project rows should spend width on role/process metrics before
  secondary identity metadata. Keep the left identity lane to rank, disclosure,
  and truncated project name; move unavailable ages or other low-value metadata
  into hover/detail surfaces so right-side numerals never collide.
- Compact project resource readouts should be independent from the process-count
  numeral. Do not place CPU and memory as a long line inside the process cell;
  keep active, all, process, CPU, and memory on fixed tabular rails so the main
  counts stay readable and resource text cannot overlap the row's large
  numerals.
- Compact project movement counts must match the global recent-movement
  definition: transcript activity inside the active window. Do not fold mapped
  process evidence into the movement count; one visible process can map to many
  historical sessions, so process pressure belongs in the separate process,
  CPU, and memory rails.
- Expanded popover session rows should fit role, agent mark, host mark, short id,
  last activity age, process count, and confidence onto one scan line whenever
  the width allows it. Detail panels may carry longer evidence.
- Expanded project session rows should behave like a compact ledger. Keep age,
  PID pressure, and measured token total on fixed right-aligned rails so rows
  can be scanned vertically. The row may show only the compact token total;
  input/output/cache/reasoning splits belong in hover or detail inspectors.
- Session execution duration is an evidence detail, not a top-level concurrency
  metric. Expose transcript-derived observed span and active burst duration in
  session detail surfaces, and keep global header metrics focused on current
  fresh/session/PID concurrency.
- Row actions in compact session lists and dashboard session trees should attach
  to the identifier they act on. Copy controls belong inline with the session id,
  with hover/focus emphasis and reserved width, rather than as a separate grid
  item that can wrap.
  Treat the session id and copy affordance as one no-wrap identifier component;
  reveal the copy control on identifier hover/focus instead of letting it sit
  after metadata or fall to a second line.
- Dense inspector and rail search may display short ids or project labels, but
  the searchable index must still include the complete local object identity for
  sessions and processes. Compact presentation must not make audit lookup by
  full session id impossible.
- Use icons for tabs, commands, status, and metrics where they reduce text load.
- Core runtime terms such as fresh movement, sessions, processes, PID match
  rate, and scan state should expose short hover/focus explanations so dense
  operator views stay readable without adding permanent copy.
- Metric help affordances must be globally consistent. Explanatory labels use a
  quiet underline or similar text-native affordance instead of repeated question
  marks in circles; the label opens the same explanation on hover, focus, click,
  Enter, and Space. Labels without explanation should not use button semantics
  or look interactive.
- Metric help tooltips are global overlays, not card-local decorations. Render
  them above resource cards, charts, hover inspectors, and decorative meter
  layers so the explanation text is never cut through by sibling graphics or
  clipped by panel scroll containers.
- Dense project/session/process rows should use one deliberate hover detail
  surface at a time. Do not combine browser-native `title` tooltips with custom
  row hover cards inside the same ledger; nested rows must suppress parent hover
  details so project, session, and process explanations do not stack.
- Popover row hover details must render outside the scroll-clipped ledger as a
  single global cursor-following layer. Avoid row-local absolute popovers, but
  keep the detail spatially tied to the pointer with a short hide delay so
  moving across dense rows does not flash or stack competing panels. The global
  hover layer should offset sideways from the pointer and use only a small
  vertical nudge; do not default to placing it a full panel height above the
  cursor, because that breaks the perceived relationship between the row and
  its detail.
  In dense popovers, prefer a complete one-pixel outline, quiet corner light,
  and lightly translucent material over a strong single-side accent rail, which
  repeats too aggressively across project/session rows and blocks scan paths.
  The outline must remain a single visual pixel: do not stack border, outline,
  mask, and spread shadows into a thick rim. Keep tooltip radius small and
  internal padding tight so dense details read as editorial annotation rather
  than a floating card.
- Session hover details should be structured, not a single compressed sentence.
  Process resources must be labeled as process CPU and process memory, and token
  usage should appear as its own section with total/input/output/cache/reasoning
  values when available.
- Token usage surfaces must show provenance when available. Transcript-derived
  token usage is measured evidence; missing token usage is unavailable, not
  zero, and should remain distinct in hover panels, detail inspectors, and
  diagnostics.
- Hover inspector titles should be complete. Project or benchmark names may wrap
  across lines inside the tooltip; do not ellipsize the title itself. Keep
  secondary metadata compact or truncated instead.
- Dense explanation blocks should default to a compact lead sentence and expose
  full details through an accessible disclosure control instead of permanently
  occupying popover height.
- Trend selections should expose the selected time and compact numeric readout
  first. Longer interpretation and trust explanations belong behind an
  accessible disclosure control, especially in the popover.
- Shared trend inspectors should describe the selected bucket in user-facing
  language such as "selected window"; reserve stricter terms like "exact
  bucket" for chart hover or detail affordances where audit precision is the
  main task.
- Selected trend inspectors should express metric relationships instead of
  presenting unrelated readout tiles. Runtime selection should read like
  processes = mapped + unmatched with matched share as a status badge; history
  selection should keep fresh movement and session count visibly paired for the
  same selected window.
- Trend selection readout cells follow the same translucent instrument rule as
  compact metric cells. They should sit above the chart as light material
  overlays rather than opaque cards that compete with plotted values.
- Compact trend views should show both lanes and the selected readout within the
  first reading pass. Do not repeat bulky selected-bucket cards under every
  lane; use one shared inspector strip and keep per-lane readouts inline with
  the lane header.
- Compact trend typography should match the denser current/status popover page:
  suite titles, lane titles, range controls, header readouts, and sample
  metadata stay compact so the chart plane remains the primary object.
- Trend point selection labels inside the chart are audit annotations, not
  passive axis ticks. Keep the selected bucket time and marker label visibly
  larger and higher contrast than ambient axis labels or lane header text.
- Trend selection must not visually move the plotted signal. The visible trend
  curve should be stable across selected buckets; the selected marker may snap
  to the chosen bucket's x position, but it must anchor the exact primary metric
  value for that bucket. If a smoothed contour is used for visual calm, the
  selected bead is the truth layer and the contour is only the reading plane.
- Compact trend lane readouts must label each numeric value. Avoid bare
  slash-separated numbers such as `3 / 6` without nearby metric names; use
  compact labels plus slightly larger tabular numerals. Prefer spacing and a
  faint one-pixel separator over a visible slash so the group reads as a flat
  instrument rather than formula text.
- Compact trend readouts should distinguish the plotted primary metric from
  context metrics. The primary value maps to the selected bead and gets the
  strongest numeric weight; context values such as sessions or matched share
  stay smaller and quieter so they are not mistaken for the plotted line.
- Trend lane headers and chart-local hover/readout overlays must not repeat the
  same selected-bucket absolute values. Put selected time, primary value, and
  context value in the lane header. Chart-local overlays should only appear
  when they add clear audit value beyond the header; compact runtime curves may
  rely on the selected bead and crosshair without any floating readout.
- Compact runtime trend clicks may open a lightweight floating drilldown inside
  the runtime lane. The float should not change the trend page layout or capture
  chart pointer events, and it should explain the selected process count using
  persisted trend semantics. Prefer Coding Agent tool distribution when the
  selected trend sample carries it, fall back to host-process distribution when
  available, and only use mapped/unmatched composition when no sampled
  distribution exists.
- Runtime trend drilldowns should stay subordinate to the chart. Prefer a
  header-integrated distribution strip over chart-covering floats; if a float is
  unavoidable, dock it near an edge, keep it translucent and shallow, and avoid
  covering the selected curve or crosshair. Distribution items should be
  content-sized inline clusters; keep icon, label, and value close together
  instead of stretching each item across equal-width columns.
- Compact trend readout separators should stay subordinate to the chart. Any
  vertical rail near the readout should be short, low-contrast, and one pixel
  wide so it does not compete with the selected trend marker.
- Trend charts should read as composed planes, not dense point clouds. Keep all
  samples interactive through invisible hit targets, but only render anchor and
  selected point marks by default; use smooth continuous strokes, quiet fills,
  and low-frequency grid rhythm so the operator sees the trend shape before the
  sampling mechanics.
- Trend chart rendering should separate audit precision from visual frequency.
  Keep raw sampled buckets available for pointer and keyboard selection, but draw
  the visible series from a reduced, softened path and collapse long-window
  time-of-day bands into a few broad planes so the chart does not fragment into
  sampling stripes. Secondary series are contextual ghosts; avoid giving them
  their own visible point marks unless the secondary metric is the selected
  object.
- If a trend still reads as visually fragmented, lower the visible anchor count
  before adding more styling. The primary contour should come from a smoothed
  low-frequency signal model, while exact bucket values stay in hit targets,
  header readouts, and the inspector. Avoid dashed ghost lines, dense anchor
  dots, or selected-bucket kinks that make the main plane look like raw sample
  mechanics.
- Trend charts should not compress count-based primary signals against
  percentage-based context metrics on a shared 0-100 scale. Scale mixed-unit
  series independently, keep the primary signal visually legible, and let the
  selected readout carry the exact mixed-unit values. Even when both series are
  counts, secondary context should not determine the primary line's vertical
  scale unless the interaction explicitly selects that secondary metric.
- Trend selection should use a soft band or other broad focus treatment before
  resorting to tiny dashed guides, dense point labels, or card-like callouts.
  In compact popover views, prefer the lane header and shared inspector for
  selected values so the chart plane stays continuous.
- When click precision is hard to audit, use a crosshair anchored at the exact
  selected bucket x/y position. The crosshair may replace the soft band in
  dense views, but it must not imply interpolated values between buckets.
- Candlestick-like trend marks are allowed when they clarify bucket-to-bucket
  movement. Their body and wick must derive from adjacent real sampled buckets;
  do not invent open/high/low/close data that the local evidence does not
  contain.
- Professional chart interaction should come from a maintained chart component
  when the trend surface needs candlesticks, crosshair behavior, hover tracking,
  or canvas stability. Keep Agent Load's code responsible for local evidence
  adaptation, selected-bucket truth, and visual skinning, not for rebuilding a
  full chart engine inline.
- Trend chart hover must expose local observation meaning, not implementation
  or library provenance. Browser `title` text and tooltip-like affordances on
  the plot plane should show selected bucket time, primary metric, context
  metrics, and bucket movement. Do not let attribution, package names, bundle
  names, or generic "powered by" copy occupy the chart hover path.
- JavaScript-heavy tab bodies should be split at page ownership boundaries.
  Trend visualizations, diagnostics, and other non-default surfaces may lazy
  load behind their tab shell so the online popover remains quick to open; the
  loading fallback must be compact and must not look like missing evidence.
- Trend hover floats are part of the chart control layer, not content cards.
  They must render above the chart canvas and adjacent lane surfaces without
  clipping, keep to a compact two-row information shape, and use light material
  accents rather than bulky opaque panels.
- Sparse trend windows should still read as one composed signal plane. Do not
  split the visible primary series into many short disconnected strokes during
  ordinary low-sample periods; keep bucket precision in hit targets, axis labels,
  and selected readouts instead of shattering the chart silhouette.
- Trend charts are audit controls, not presentation cards. Use flat chart
  planes, thin separators, short range controls, and dense click targets so the
  operator can compare lanes without scrolling through repeated explanation
  panels.
- Trend project heatmaps share the active trend range. They should render as
  one cut-plane area map, not a list of cards, and must use persisted local
  history rather than sample or decorative data.
- Heatmap area represents cumulative session-window investment for a project
  within the selected range: each retained history sample contributes its
  project session count. Heatmap metadata should also expose sampled window
  count so users can distinguish intensity from evidence coverage.
- Heatmap tiles may compress labels in tiny rectangles, but the strongest
  numeric value remains the session-window count and the full project/window
  detail stays available through hover/focus metadata.
- Compressed heatmap tiles should degrade typography before allowing overlap:
  hide secondary window labels first, reduce the primary numeral next, and only
  remove the numeral entirely for dot-sized cells where hover is the only clean
  detail surface.
- Heatmap hover should use the app's own compact material tooltip, not the
  browser's native `title` bubble. Native bubbles can obscure adjacent tiles and
  make compressed labels look broken.
- Heatmap containers may use an outer radius, but internal project rectangles
  should read as Metro-style square cuts. Do not squeeze project labels into
  small or shallow tiles; hide label/detail text and rely on hover/focus
  metadata when there is not enough area for clean typography.
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
- Trend window labels must use the API window bounds when available and include
  date-qualified endpoints for cross-day ranges. A 1D trend must not render as
  the same clock time on both sides when the chart axis spans different dates.
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
- Large interactive domains should leave `ui/src/main.tsx` as composition and
  state wiring. Reusable surfaces such as trend charts, heatmaps, inspectors,
  and their domain types belong in focused modules under `ui/src/<domain>/`.
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
