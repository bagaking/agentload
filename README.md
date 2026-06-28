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

- `AGENTLOAD_LISTEN_ADDR`: dashboard listen address.
- `AGENTLOAD_HISTORY_FILE`: JSONL history file path.
- `AGENTLOAD_CLAUDE_DIRS`: path-list of Claude config roots.
- `AGENTLOAD_CODEX_DIRS`: path-list of Codex config roots.
- `AGENTLOAD_TRAE_DIRS`: path-list of Trae config roots.

## Development

```sh
npm --prefix ui install
npm --prefix ui run build
go test ./...
node scripts/validate_locales.js
./build_macos_app.sh
```
