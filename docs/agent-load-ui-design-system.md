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
  historical, runtime, and output-throughput chart analysis. System owns
  whole-machine resource samples
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
- Ban the narrow horizontal two-strip metric pattern: oversized values in one
  strip followed by colored calibration bars, repeated rails, or per-number
  status treatments. In constrained surfaces, use one unframed row-based
  readout with aligned label, value, and evidence detail columns. A scale is
  allowed only when it has an explicit unit and a real comparison task; it must
  not be decorative chrome.
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
- Hover inspectors are cursor-proximate readouts, not detail panels. Keep them
  narrow, shallow, and content-dense; use compact metric grids and one- or
  two-line token chips so the reader can continue scanning adjacent rows while
  the inspector is visible.
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
- Compact popover online and trend views should not carry a full process ledger
  or whole-machine resource dashboard. Online may show a short, directly
  visible process-evidence list when current PID presence is part of the user's
  question, while detailed process diagnostics and system CPU, memory, and
  network fluctuation remain in the system view. Keep workload evidence and
  machine pressure visually and semantically separate.
- Diagnostics is the only compact page for anomaly/forecast signals and export.
  Do not duplicate these controls into Trend or System. Trend may link runtime
  drilldowns to persisted samples; System may show current process evidence; the
  Diagnostics page explains whether the evidence is complete enough to trust.
- Diagnostics must read as a fact-check and local evidence inspection surface,
  not an AI report, generic health dashboard, or card pile. Put observed
  evidence quality first, keep forecasting explicitly unavailable unless a real
  model exists, and express every priority row as plain user-facing
  issue/evidence/source/next-check language. Backend diagnostic keys may appear
  only as hidden titles or export evidence; visible source badges should prefer
  localized source names over raw codes such as parser or risk ids.
- Diagnostics layout should use a small set of reusable planes: a situation map,
  a fixed loss ledger, an optimization experiment queue, the priority checks and
  evidence-chain map, and a safe-export boundary. The loss ledger keeps current
  value, evidence family, scope, freshness, state, source, and next check in
  the same row. Avoid one-off diagnostic cards that repeat the same visual frame
  or make the page feel generated rather than deliberately instrumented.
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
- Compact popover trend view combines active sessions, known sessions, and
  visible PIDs in one clearly labeled count chart. They share a time plane, not
  a semantic family: session lines retain transcript-derived samples and the
  PID line retains persisted runtime samples. Process composition remains a
  process-series drilldown, throughput remains a separate project-separated TPS
  river, and neither may be folded into the project heatmap. The heatmap must
  keep enough area to read project proportions.
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
- System resource selectors must read as one unframed telemetry plane, not a
  repeated card stack. The first read is an equal three-value headline strip;
  capacity, network, thermal, and Agent process evidence follow as icon-led
  rows separated only by hairlines. The selected inspector stays compact by
  default with only its trend visible; a discrete disclosure reveals exact fact
  rails, source, and scope. Its trend is limited to samples observed while the
  panel is open. Keep whole-machine readings separate from the per-PID process
  ledger.
- Thermal pressure may appear when the public macOS API supplies it, but its
  label must not imply a temperature. Show exact temperature and fan RPM as
  unavailable rather than inferring either value from load.
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
- The combined session/process chart should use low-noise curves with a stable
  legend for `active sessions`, `known sessions`, and `visible PIDs`. Never call
  the session lines "records": they count sessions, not transcript entries.
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
- Human-review cues must be visually distinct from active work. Use blue
  micro-badges only for main conversations whose local freshness evidence is
  idle/waiting; do not remind on subagents, unknown-role rows, stale sessions,
  or unmapped process-only rows, and do not reuse the green active indicator for
  this state.
- Compact project rows should not compress repeated counts into tiny matrix
  walls. When role/session/process numbers become dense, group them into a few
  readable ledger chips, use modest row breathing room, and add subtle tonal
  rhythm instead of adding more grid labels.
- Compact project ledgers should read as aligned scan rows, not rows of
  button-like metric blocks. Use fixed columns, one-pixel dividers, faint
  alternating planes, and restrained state color so row separation is legible
  without making the list look chunky.
- Project expansion must be a vertical disclosure only. Expanded session trees
  must stay inside the same ledger width as collapsed rows; set explicit
  shrink boundaries on row, head, tree, branch, lineage summary, and
  session-line grids so session evidence, relationship summaries, or resource
  columns cannot widen the popover list.
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
  presenting unrelated readout tiles. Process selection should read like
  processes = mapped + unmatched with matched share as a status badge; session
  selection should keep active and known session counts visibly paired for the
  same selected window.
- Trend selection readout cells follow the same translucent instrument rule as
  compact metric cells. They should sit above the chart as light material
  overlays rather than opaque cards that compete with plotted values.
- Compact trend views should show all available lanes and the selected readout
  within the first reading pass. Do not repeat bulky selected-bucket cards under every
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
  same selected-bucket absolute values. Put selected values in the combined
  legend. A chart tooltip may add audit value by showing each series' actual
  sample time, especially when transcript and runtime samples do not align.
- Selecting the visible-PID series may open a lightweight drilldown inside the
  combined count lane. It should not change the trend page layout or capture
  chart pointer events, and it should explain the selected process count using
  persisted trend semantics. Prefer Coding Agent tool distribution when the
  selected trend sample carries it, fall back to host-process distribution when
  available, and only use mapped/unmapped composition when no sampled
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
- Professional chart interaction should come from a maintained chart component
  when the trend surface needs crosshair behavior, hover tracking, or canvas
  stability. Count and pressure lanes use the existing maintained area-series
  component; do not manufacture OHLC semantics from point samples.
- Output throughput uses a zero-baseline stacked area river with one layer per
  project, including `unassigned`. Horizontal positions follow the derived point
  timestamps inside the selected API range, including blank space where minute
  facts were not recorded. `1D / 3D / ...` changes only the horizontal domain;
  the separate `1m / 5m / 15m` segmented control changes only the rolling
  denominator. Do not center the stack like a decorative streamgraph, infer past
  project shares from the current snapshot, bridge missing coverage, or
  synthesize a layer for history without project partitions.
- The existing chart dependency has no stacked-area series. Keep the small
  throughput SVG local to the trend module and limited to stacking persisted
  project values, time-based hit testing, and selection; do not add a second
  chart framework or turn it into a generic chart abstraction.
- The throughput lane header must make the selected period readable at a glance
  as `MAX / P95 / AVG / CUR(<window>)`. The first three values summarize the full
  derived numeric series for that range before chart-point reduction; `CUR`
  names the selected rolling window and is current evidence rather than the last
  hovered point. Keep exact point inspection in chart hover and selection instead
  of replacing the period summary when the pointer moves.
- Current minute-derived options are the default family, with `5m` selected
  initially. Versioned legacy rolling-rate series may appear as visibly labeled
  legacy options, but the UI must never select one as a fallback when a current
  series is empty and must never show `CUR` for legacy evidence.
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
- The throughput river must keep the selected API window as its horizontal
  domain even when source history is incomplete. Blank horizontal space is the
  truthful representation of unrecorded time and makes range changes visible;
  do not stretch a short throughput history to fill every selected range.
- Trend window labels must use the API window bounds when available and include
  date-qualified endpoints for cross-day ranges. A 1D trend must not render as
  the same clock time on both sides when the chart axis spans different dates.
- Light mode must keep weak labels, icons, tree rails, and control text readable;
  do not rely on very pale gray text for operator-critical controls.
- Auto refresh defaults to `5m`. A paused refresh state may exist, but it must be
  labeled as refresh pause/off and must not be conflated with an idle session
  state.
- The compact top bar should not repeat ambient idle state when the footer
  already exposes timestamp and refresh cadence. If it carries session status,
  make it an actionable rotating digest: main conversations needing review,
  active-window movement, and main/subagent mix. Do not reduce it to unlabeled
  abbreviations, and do not count stopped subagents as human-review work.
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

## 第二轮界面 Review：新增改动要求（2026-09-13）

用户诉求：“现在再 review 一轮，提出新的改动要求”。本轮以紧凑弹窗的
快速查看、趋势比较和项目归因为目标，参考本项目已有的本地监控控制台规范。
证据为用户提供的截图及当前工作区源码；新版运行截图因浏览器连接受限未取得。
以下是待实施、待验收的 review 要求，不代表用户已逐项确认或界面已经修复。

| 优先级 / 编号 | 当前证据 | 改动要求与验收标准 |
| --- | --- | --- |
| P0 / R2-01 | `ThroughputRiver.tsx` 的指针查询在过滤后的有效样本中无条件寻找最近点。 | 采样空档必须能被读出来：悬停到缺口或观测范围之外时显示“无采样”，不跨缺口吸附到旧值；保持已知零值、缺失和过期的区别。用两个有效片段之间的空档验收。 |
| P0 / R2-02 | 项目贡献比例使用 `Math.max(1, ...)`，小于 1% 的非零贡献被显示成 1%。 | 数字比例按真实分母计算，小贡献显示 `<1%` 或适当小数；零贡献显示 0%。若为了可见性保留最小图形宽度，必须与数值比例分离。覆盖零值、0.2% 和多个小贡献项目。 |
| P1 / R2-03 | 紧凑项目列表固定取周期排序的前四项，但行内读数随选中时刻变化，且没有剩余项目入口。 | 默认列表明确标注排序口径，显示“查看全部 N 个项目”；选中时刻的贡献项目必须可达。悬停只更新数值，不让行位置随鼠标移动反复重排。用仅在选中时刻贡献很高的第五个项目验收。 |
| P1 / R2-04 | 河流图绘制网格与面积，但 SVG 没有时间刻度、纵轴数值和单位。 | 常驻显示时间起止、至少一个中间刻度及纵轴零值/上界和 token/s 单位；跨日端点带日期。用户不悬停也能判断时间位置和数量级，切换范围后坐标同步。 |
| P1 / R2-05 | 有有效样本即显示 tooltip；离开时清除 hover 后仍回到默认或选中样本，键盘无 Escape 退出处理。 | 明确区分默认、悬停、固定选择：默认图面不被浮层遮挡；悬停临时查看，点击或 Enter 固定并给出选中标记，Escape 取消。固定时刻在刷新后保持稳定；样本退出范围时明确解除。键盘读数与鼠标读数等价可达。 |
| P1 / R2-06 | 底栏只给选中项显示文字，并改变其 flex；刷新频率是点击轮换，控制高度为 20–22px。 | 导航槽位保持固定，切换页面不挪动其他入口；频率点击后列出可选值并标记当前值。目标点击区域至少 28×28 CSS px。默认 430px 宽度下覆盖所有页面、中英日文及有无角标；长文字不能挤掉时间和导航。此项将现有 click-to-cycle 约定替换为显式选择，实施时同步更新旧规则。 |
| P1 / R2-07 | 周期指标标签为 8px、筛选标签为 8.8px，多个区块禁止收缩/换行。 | 不用继续缩字解决拥挤：主要操作和读数标签建议至少 11px，辅助说明至少 10px；长语言优先分行或披露次要元信息。按 430px 实际 CSS 宽度、100% 缩放检查，不能用放大的 Retina 截图代替字号验收。 |
| P1 / R2-08 | 历史窗口可选 1m/5m/15m，而右侧 live 读数使用自己的采样窗口。 | 历史控制明确标注“历史平滑窗口”；live 读数保留其独立窗口说明。选中历史时刻时，项目贡献区显示该时刻，不能暗示右上 live 值也属于它。用历史 1m 与 live 5m 并存场景验收；没有同口径对照数据时不增加涨跌百分比。 |

本轮纠正上一轮建议：语言入口继续保留在弹窗内；会话、进程与项目的颜色
依照现有语义规范，不能仅为统一外观重新解释状态。完整视觉验收仍需新版
实际弹窗的深浅主题截图，以及空数据、长名称、多项目和采样缺口的交互检查。

## Implementation Contract

- Source UI lives under `ui/src`.
- Vite output lives under `ui/dist` because Go embeds it for `go test`, `go run`,
  and packaged app builds.
- `build_macos_app.sh` must build the UI before compiling the Go app.
- The app name shown to users is `Agent Load`; the module, executable, and
  configuration namespace stay `agentload`.
