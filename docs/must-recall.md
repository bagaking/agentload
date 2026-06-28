# Recall

This page defines the default recall workflow for shared knowledge.

## Workflow

1. Search first.
2. Read only the needed lines.
3. Quote only the needed lines.
4. Answer with references when useful.

## Commands

- search:
  - `sh "$BAGAKIT_LIVING_KNOWLEDGE_SKILL_DIR/scripts/bagakit-living-knowledge.sh" recall search --root . '<query>'`
- get:
  - `sh "$BAGAKIT_LIVING_KNOWLEDGE_SKILL_DIR/scripts/bagakit-living-knowledge.sh" recall get --root . <path> --from <line> --lines <n>`

## Scope

Default recall searches:

- the configured shared root
- root and path-applicable `AGENTS.md`

Default recall does not search other runtime systems unless the task asks for
them explicitly.
