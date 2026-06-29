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

- top bar with brand, loopback/no-upload status, live state, refresh, language,
  and theme controls
- popover surface with an online/trend switch, current meaning strip, scan
  boundary, compact project/session atlas, and trend suite
- popover language control remains visible in compact mode; locale switching is
  a first-class operator control, not a dashboard-only setting. Direct links may
  specify `?lang=`, and the page-level `lang` attribute should use the resolved
  locale rather than a generic fallback.
- Compact popover chrome should not spend primary horizontal space on the full
  loopback address; keep local-only status available through a small status mark,
  tooltip, or detail surface.
- dashboard surface with front status, evidence column, project/session atlas,
  calibration rail, age rail, confidence grid, process ledger, and trend suite
- dashboard uses document-level scrolling as a full report surface. Do not lock
  it to a single viewport or hide lower audit bands behind nested panel scroll;
  the popover is the constrained-height surface.
- Project / Sessions / Processes navigation remains available as a dense
  inspector band for focused lookup
- main/detail panes may show result-style headers and evidence text, but should
  not dominate the first dashboard viewport

## Visual Rules

- Prefer flat material surfaces over nested card stacks.
- Keep padding tight enough for repeated operational use.
- Use thin separators and depth changes; avoid heavy line-box scaffolding and
  oversized framed logs.
- Favor scan-line density for audit lists. Primary rows should expose only the
  fields needed to understand global state; secondary identifiers such as full
  local addresses, long paths, and verbose evidence belong in tooltips, detail
  panes, or disclosure surfaces.
- Compact project rows should expose role mix, active/all session totals,
  process count, and observed tool coverage before expansion. Expansion is for
  relationship inspection, not the first moment when distribution becomes
  visible.
- Expanded project rows must keep sessions in a compact tree outline. Avoid
  block-level evidence stacks inside the popover; global audit should not
  require scrolling past full evidence cards to understand parent/child session
  shape.
- Expanded popover session rows should fit role, agent mark, host mark, short id,
  last activity age, process count, and confidence onto one scan line whenever
  the width allows it. Detail panels may carry longer evidence.
- Row actions in compact session lists should attach to the identifier they act
  on. Copy controls belong inline with the session id, with hover/focus emphasis
  and reserved width, rather than as a separate grid item that can wrap.
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
- Light mode must keep weak labels, icons, tree rails, and control text readable;
  do not rely on very pale gray text for operator-critical controls.
- Auto refresh defaults to `5m`. A paused refresh state may exist, but it must be
  labeled as refresh pause/off and must not be conflated with an idle session
  state.
- Multilingual UI copy is part of the design surface. New operator-facing text
  must be added to every supported locale with matching placeholder tokens.
- Locale resources should stay in `ui/src/i18n.ts`; UI components should call
  the translation helper instead of embedding language-specific copy inline.
- Review feedback is design input. If a critique exposes a reusable rule about
  hierarchy, density, contrast, controls, terminology, or auditability, fold it
  back into this document instead of leaving it only in chat history.
- Use the token family in `ui/src/styles.css` as the source of truth:
  - primary accent: blue
  - active/running: yellow
  - mapped/ok: green
  - mismatch/error: red
  - secondary operator surfaces: dark neutral grays

## Implementation Contract

- Source UI lives under `ui/src`.
- Vite output lives under `ui/dist` because Go embeds it for `go test`, `go run`,
  and packaged app builds.
- `build_macos_app.sh` must build the UI before compiling the Go app.
- The app name shown to users is `Agent Load`; the module, executable, and
  configuration namespace stay `agentload`.
