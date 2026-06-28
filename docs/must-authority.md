# Authority

This page defines where truth lives for the shared knowledge substrate.

## Shared Checked-In Knowledge

- root:
  - `docs`

## Runtime Roots Declared By Protocol

- researcher:
  - `.bagakit/researcher`
- selector:
  - `.bagakit/skill-selector`
- evolver:
  - `.bagakit/evolver`

## Rules

- shared durable project knowledge belongs under the shared root
- `.bagakit/` is host-local runtime state by default and may be ignored
- material created under `.bagakit/` must be promoted to `docs/`, `mem/`,
  `gate_validation/`, `gate_eval/`, or `skills/` before it becomes committed
  public repository truth
- path-local `AGENTS.md` may narrow execution guidance, but must not redefine the
  shared knowledge root
- shared pages, managed bootstrap text, and durable examples use repo-relative
  paths only
- absolute filesystem paths are forbidden in durable shared surfaces
- if one imported reference needs a durable handle, prefer a short opaque id
  such as `k-2ab7qxk9`
- do not carry forward timestamp-derived names, raw source file names, raw
  source file contents, or raw source-path/action-time metadata into shared
  knowledge pages
- research runtime is not shared knowledge by default
- evolver memory is not shared knowledge by default
- selector runtime is not shared knowledge by default
