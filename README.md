# Agent Load

Agent Load is a local macOS menu bar monitor for AI agent activity. It tracks
visible local AI processes, maps them to known sessions when local evidence is
available, and serves a dashboard for current load and recent trends.

## Run

```sh
go run .
```

The app listens on `127.0.0.1:8642` by default and falls back to a random local
port when that address is busy.

## Configuration

Environment variables:

- `AGENTLOAD_LISTEN_ADDR`: dashboard listen address.
- `AGENTLOAD_HISTORY_FILE`: JSONL history file path.
- `AGENTLOAD_CLAUDE_DIRS`: path-list of Claude config roots.
- `AGENTLOAD_CODEX_DIRS`: path-list of Codex config roots.
- `AGENTLOAD_TRAE_DIRS`: path-list of Trae config roots.

When the `AGENTLOAD_*_DIRS` variables are unset, the tool-native fallbacks
`CLAUDE_CONFIG_DIR`, `CODEX_HOME`, and `TRAE_CLI_HOME` are honored before the
default home-directory roots.

CLI flags (defaults come from the environment and built-in config):

- `-listen`: listen address.
- `-idle-gap`: idle gap for active burst segmentation.
- `-min-interval`: minimum interval for zero-length spans.
- `-lookback`: historic lookback window.
- `-cache-ttl`: transcript scan cache ttl.
- `-refresh-interval`: background refresh interval.
- `-history-file`: local JSONL history file.

Background refresh defaults to every 5 minutes; non-zero intervals below the
30-second minimum are clamped up to 30 seconds.

## Documentation

- [Docs index](docs/README.md)
- [Architecture](docs/architecture.md)
- [API reference](docs/api-reference.md)
- [Metric semantics](docs/agent-load-metric-semantics.md)
- [UI design system](docs/agent-load-ui-design-system.md)

## Development

```sh
npm --prefix ui install
npm --prefix ui run build
go test ./...
node scripts/validate_locales.js
./build_macos_app.sh
```

## Distribution Prep

- [Apple distribution readiness](docs/apple-distribution-readiness.md)
- [Local observation privacy draft](docs/privacy-local-observation.md)
- [App Store positioning](docs/app-store-positioning.md)
