---
title: Maintaining Reusable Items
sop:
  - When one pattern, checklist, or example becomes a stable project default, add or update the matching reusable-items catalog entry in the same change.
  - When reusable-items route guidance changes, refresh `docs/must-sop.md` by running `sh "$BAGAKIT_LIVING_KNOWLEDGE_SKILL_DIR/scripts/bagakit-living-knowledge.sh" index --root .`.
---

# Maintaining Reusable Items

This page is the governance entrypoint for project-local reusable items.

Reusable items are durable assets worth reusing across iterations, such as:

- canonical notes and knowledge indexes
- coding patterns and implementation mechanisms
- design patterns and review artifacts
- writing patterns and terminology anchors

## Rule

- keep one canonical entry per reusable item
- link to the real source of truth instead of copying it
- record when an item is `MUST`, `SHOULD`, or `NICE`
- update the relevant catalog when the item changes materially

## Recommended Catalogs

- `docs/notes-reusable-items-knowledge.md`
- `docs/notes-reusable-items-coding.md`
- `docs/notes-reusable-items-design.md`
- `docs/notes-reusable-items-writing.md`

## Starting Rule

- keep the governance page even if only one catalog is active
- start with the domains the repository actually uses
- prefer small curated catalogs over broad stale inventories
