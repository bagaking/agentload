---
title: Agent Load UI Design System
sop:
  - When changing `ui/src`, preserve the operator-console structure unless the product workflow changes materially.
  - Keep visual tokens aligned with the console design language: dark material surfaces, blue primary accent, green/yellow/red semantic states, compact rails, and dense result panes.
  - Before committing UI changes, run `npm --prefix ui run build` and `go test ./...`.
---

# Agent Load UI Design System

Agent Load is an operator console, not a generic dashboard or landing page.
The UI should feel like a local developer console for inspecting machine-local
agent evidence.

## Required Structure

- top bar with brand, loopback/no-upload status, live state, refresh, language,
  and theme controls
- left rail for selecting observation groups
- dense rail rows with kind color, status dot, short description, evidence path,
  and compact tags
- main pane with result-style header, status badge, command/evidence path,
  timeline, metric strip, and log-like detail body
- popover uses the same visual language in a tighter responsive layout

## Visual Rules

- Prefer flat material surfaces over nested card stacks.
- Keep padding tight enough for repeated operational use.
- Use thin separators and depth changes; avoid heavy line-box scaffolding.
- Use icons for tabs, commands, status, and metrics where they reduce text load.
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
