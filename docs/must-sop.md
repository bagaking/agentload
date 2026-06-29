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
- When changing `ui/src`, preserve the popover/dashboard split and keep Project / Sessions / Processes available as navigation, not as the only visual shell.
- Keep visual tokens aligned with the console design language: dark material surfaces, blue primary accent, green/yellow/red semantic states, compact bands, and dense evidence panes.
- Before committing UI changes, run `node scripts/validate_locales.js` and `go test ./...`.
- When review feedback reveals a missing durable UI rule, update this design system in the same change.

### Maintaining Reusable Items
Source: `docs/norms-maintaining-reusable-items.md`
- When one pattern, checklist, or example becomes a stable project default, add or update the matching reusable-items catalog entry in the same change.
- When reusable-items route guidance changes, refresh `docs/must-sop.md` by running `sh "$BAGAKIT_LIVING_KNOWLEDGE_SKILL_DIR/scripts/bagakit-living-knowledge.sh" index --root .`.

### Reusable Items - Knowledge
Source: `docs/notes-reusable-items-knowledge.md`
- Update this catalog when one note, index, or query pattern becomes worth reusing across tasks.
- Keep source-of-truth links current and remove duplicate entries.
