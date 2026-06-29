---
title: Agent Load UI Design System
sop:
  - When changing `ui/src`, preserve the popover/dashboard split and keep Project / Sessions / Processes available as navigation, not as the only visual shell.
  - Keep visual tokens aligned with the console design language: dark material surfaces, blue primary accent, green/yellow/red semantic states, compact bands, and dense evidence panes.
  - Before committing UI changes, run `npm --prefix ui run build` and `go test ./...`.
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
- dashboard surface with front status, evidence column, project/session atlas,
  calibration rail, age rail, confidence grid, process ledger, and trend suite
- Project / Sessions / Processes navigation remains available as a dense
  inspector band for focused lookup
- main/detail panes may show result-style headers and evidence text, but should
  not dominate the first dashboard viewport

## Visual Rules

- Prefer flat material surfaces over nested card stacks.
- Keep padding tight enough for repeated operational use.
- Use thin separators and depth changes; avoid heavy line-box scaffolding and
  oversized framed logs.
- Expanded project rows must keep sessions in a compact tree outline. Avoid
  block-level evidence stacks inside the popover; global audit should not
  require scrolling past full evidence cards to understand parent/child session
  shape.
- Use icons for tabs, commands, status, and metrics where they reduce text load.
- Light mode must keep weak labels, icons, tree rails, and control text readable;
  do not rely on very pale gray text for operator-critical controls.
- Auto refresh defaults to `5m`. A paused refresh state may exist, but it must be
  labeled as refresh pause/off and must not be conflated with an idle session
  state.
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
