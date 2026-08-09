# Agent Load Agent Guidance

Agent Load is a local macOS menu bar app written in Go. It observes local agent
activity, maps visible AI processes to local session evidence, and serves an
embedded dashboard/popover UI from the Vite build in `ui/dist`.

Useful commands:

- `go test ./...`
- `npm --prefix ui run build`
- `node scripts/validate_locales.js`
- `./build_macos_app.sh`

When changing UI, edit `ui/src`, run the UI build, then run Go tests. The root
`dist/` app bundle stays untracked.

Data truth is the first product standard. UI polish must not change metric
meaning, hide sampling gaps, or mix semantic families. Recent movement, known
sessions, process pressure, CPU, memory, and role splits must route through the
metric semantic layer and remain consistent across header metrics, project rows,
trend charts, hover panels, and detail inspectors. The contracts live in
[docs/agent-load-metric-semantics.md](docs/agent-load-metric-semantics.md) and
[docs/agent-load-ui-design-system.md](docs/agent-load-ui-design-system.md).

## Engineering Principles

- 不保留向后兼容。过时的直接删，别加兼容层、别写 migration、别留
  fallback。
- 选能满足当前需求的最简单实现。不要预防性抽象，不要多此一举的配置层。
- 系统分层长。先跑通一个最小的端到端版本，再往上加东西。绝不为了未完成的
  复杂度拆掉能跑的东西。
- 组件保持模块化，关注点分离。
- 优先用成熟的、有人维护的库。没有明确理由别自己重写。
- 先翻项目里已有的依赖能做什么，再考虑加新包或自己写。别上来就假设库里没有。
- 架构决策往长了做。不接受“先这样以后再换”的临时方案。
- 先看成熟产品怎么解决同一个问题，用已验证的模式，别从零发明。
- 和用户讨论过的内容，要及时更新到需求文档，并尽量用贴近用户原始说法的
  表述方式。

The managed block below applies only when the bagakit tooling is installed.
<!-- BAGAKIT:LIVING-KNOWLEDGE:START -->
This is a managed block for `bagakit-living-knowledge`. Do not hand-edit the
managed region directly; refresh it through the skill operator instead.

Resolve the installed skill dir before using the operator directly:

- `export BAGAKIT_LIVING_KNOWLEDGE_SKILL_DIR="<repo-relative-installed-skill-dir>"`

Boot layer:

- Read the resolved `must-guidebook.md` before relying on memory.
- If a task needs shared knowledge rules, read `must-authority.md`.
- If a task needs maintenance-route guidance or shared directives, read `must-sop.md`.
- If a task needs prior decisions or facts, follow `must-recall.md`.
- `AGENTS.md` is only the bootstrap layer; the shared checked-in knowledge root
  defaults to `docs`, with shared path protocol config in
  `docs/.bagakit-knowledge.toml` when present.

Recall discipline:

- Search first:
  - `sh "$BAGAKIT_LIVING_KNOWLEDGE_SKILL_DIR/scripts/bagakit-living-knowledge.sh" recall search --root . '<query>'`
- Then inspect only the needed lines:
  - `sh "$BAGAKIT_LIVING_KNOWLEDGE_SKILL_DIR/scripts/bagakit-living-knowledge.sh" recall get --root . <path> --from <line> --lines <n>`
- Prefer quoting only needed lines over paraphrasing from memory.

Substrate discipline:

- Shared knowledge belongs under the configured shared root.
- `.bagakit/` is host-local runtime state and may be ignored; do not publish
  shared knowledge there.
- Durable examples and managed bootstrap text must stay repo-relative; never
  record absolute filesystem paths in shared knowledge or AGENTS guidance.
- When imported material needs one durable handle, prefer a short opaque id
  such as `k-2ab7qxk9` instead of a timestamped capture name.
- Research runtime belongs to `bagakit-researcher`.
- Task-level composition/runtime belongs to `bagakit-skill-selector`.
- Repository evolution memory belongs to `bagakit-skill-evolver`.
- `living-knowledge` owns path protocol, normalization, indexing, and recall.
- `living-knowledge` also owns generated `must-sop.md` and reusable-items
  governance inside the shared knowledge root.

Inspection helpers:

- Run these commands from the project root so `--root .` resolves to the
  intended project.
- View the resolved path protocol:
  - `sh "$BAGAKIT_LIVING_KNOWLEDGE_SKILL_DIR/scripts/bagakit-living-knowledge.sh" paths --root .`
- Refresh the guidebook and helper map:
  - `sh "$BAGAKIT_LIVING_KNOWLEDGE_SKILL_DIR/scripts/bagakit-living-knowledge.sh" index --root .`
- Run non-destructive diagnostics:
  - `sh "$BAGAKIT_LIVING_KNOWLEDGE_SKILL_DIR/scripts/bagakit-living-knowledge.sh" doctor --root .`

If the surrounding workflow explicitly asks for `living-knowledge` task
reporting, the response footer may use:

- `[[BAGAKIT]]`
- `- LivingKnowledge: Surface=<updated shared surfaces or none>; Evidence=<commands/checks>; Next=<one deterministic next action>`
<!-- BAGAKIT:LIVING-KNOWLEDGE:END -->
