# SOP

This page is generated from optional frontmatter in shared knowledge pages.
Do not hand-edit it directly.

## Update Rules
- Refresh this page with `sh "$BAGAKIT_LIVING_KNOWLEDGE_SKILL_DIR/scripts/bagakit-living-knowledge.sh" index --root .`.
- Add or update route guidance in page frontmatter under `sop:` and optional `directives:`.
- Keep items short, concrete, and scoped to the source page.

## Sources
- shared root: `docs`
- system root: `docs`

## SOP Items

### Agent Load UI Design System
Source: `docs/agent-load-ui-design-system.md`
- When changing `ui/src`, preserve the operator-console structure unless the product workflow changes materially.
- Keep visual tokens aligned with the console design language: dark material surfaces, blue primary accent, green/yellow/red semantic states, compact rails, and dense result panes.
- Before committing UI changes, run `npm --prefix ui run build` and `go test ./...`.

### Maintaining Reusable Items
Source: `docs/norms-maintaining-reusable-items.md`
- When one pattern, checklist, or example becomes a stable project default, add or update the matching reusable-items catalog entry in the same change.
- When reusable-items route guidance changes, refresh `docs/must-sop.md` by running `sh "$BAGAKIT_LIVING_KNOWLEDGE_SKILL_DIR/scripts/bagakit-living-knowledge.sh" index --root .`.

### Reusable Items - Knowledge
Source: `docs/notes-reusable-items-knowledge.md`
- Update this catalog when one note, index, or query pattern becomes worth reusing across tasks.
- Keep source-of-truth links current and remove duplicate entries.
