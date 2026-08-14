set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")" && cd .. && pwd)"
APP_BUNDLE="${AGENTLOAD_APP_BUNDLE:-$ROOT/dist/Agent Load.app}"
APP_BIN="${AGENTLOAD_APP_BIN:-$APP_BUNDLE/Contents/MacOS/agentload}"
SUPPORT_DIR="${AGENTLOAD_SUPPORT_DIR:-$HOME/Library/Application Support/AgentLoad}"
WATCH_DIR="${AGENTLOAD_WATCH_DIR:-$SUPPORT_DIR/watcher}"
EVENT_LOG="${AGENTLOAD_WATCH_EVENT_LOG:-$WATCH_DIR/watcher.jsonl}"
STDOUT_LOG="${AGENTLOAD_WATCH_STDOUT_LOG:-$WATCH_DIR/stdout.log}"
STDERR_LOG="${AGENTLOAD_WATCH_STDERR_LOG:-$WATCH_DIR/stderr.log}"
PID_FILE="${AGENTLOAD_WATCH_PID_FILE:-$WATCH_DIR/agentload.pid}"
SNAPSHOT_URL="${AGENTLOAD_WATCH_SNAPSHOT_URL:-http://127.0.0.1:8642/api/snapshot}"
QUIT_URL="${AGENTLOAD_WATCH_QUIT_URL:-http://127.0.0.1:8642/api/quit}"
RUN_ID="$(date '+%Y%m%dT%H%M%S')-$$"
REPLACE=0
HOLD_AFTER_EXIT=0
HOLDING=0
CHILD_PID=""
APP_ARGS=()

usage() {
  cat <<'EOF'
Usage: bash scripts/run_agentload_watched.sh [--replace] [--hold-after-exit] [--] [agentload args...]

Runs the built Agent Load app binary as a child process and records process-level
evidence outside the app process:

  ~/Library/Application Support/AgentLoad/watcher/watcher.jsonl
  ~/Library/Application Support/AgentLoad/watcher/stdout.log
  ~/Library/Application Support/AgentLoad/watcher/stderr.log
  ~/Library/Application Support/AgentLoad/watcher/agentload.pid

By default this records one run and does not restart the app. Use --replace to
ask an already running local Agent Load instance to quit through the quit endpoint
before starting the watched child. Use --hold-after-exit when the watcher is submitted
through launchd; after the child exits it keeps the watcher alive so launchd does
not relaunch the app and hide the failure.
EOF
}

while (($# > 0)); do
  case "$1" in
    --replace)
      REPLACE=1
      shift
      ;;
    --hold-after-exit)
      HOLD_AFTER_EXIT=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      APP_ARGS+=("$@")
      break
      ;;
    *)
      APP_ARGS+=("$1")
      shift
      ;;
  esac
done

mkdir -p "$WATCH_DIR"
: >>"$EVENT_LOG"
: >>"$STDOUT_LOG"
: >>"$STDERR_LOG"

json_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '%s' "$value"
}

json_field() {
  local key="$1"
  local value="$2"
  if [[ "$value" =~ ^-?[0-9]+$ ]]; then
    printf ',"%s":%s' "$key" "$value"
  else
    printf ',"%s":"%s"' "$key" "$(json_escape "$value")"
  fi
}

log_event() {
  local event="$1"
  shift
  local line
  line="{\"at\":\"$(date '+%Y-%m-%dT%H:%M:%S%z')\",\"event\":\"$(json_escape "$event")\",\"run_id\":\"$(json_escape "$RUN_ID")\",\"watcher_pid\":$$"
  while (($# > 0)); do
    line+="$(json_field "$1" "$2")"
    shift 2
  done
  printf '%s}\n' "$line" >>"$EVENT_LOG"
}

snapshot_alive() {
  curl --max-time 2 -fsS "$SNAPSHOT_URL" >/dev/null 2>&1
}

request_existing_quit() {
  curl --max-time 3 -fsS -X POST "$QUIT_URL" >/dev/null 2>&1
}

forward_signal() {
  local signal_name="$1"
  log_event "watcher_signal" "signal" "$signal_name" "child_pid" "${CHILD_PID:-0}"
  if ((HOLDING == 1)); then
    log_event "watcher_exited" "reason" "signal_while_holding" "signal" "$signal_name"
    exit 128
  fi
  if [[ -n "${CHILD_PID:-}" ]] && kill -0 "$CHILD_PID" 2>/dev/null; then
    kill -s "$signal_name" "$CHILD_PID" 2>/dev/null || true
  fi
}

trap 'forward_signal HUP' HUP
trap 'forward_signal INT' INT
trap 'forward_signal TERM' TERM
trap 'forward_signal QUIT' QUIT

if [[ ! -x "$APP_BIN" ]]; then
  log_event "start_failed" "reason" "missing_or_non_executable_binary" "app_bin" "$APP_BIN"
  echo "agentload watcher: missing or non-executable app binary: $APP_BIN" >&2
  exit 66
fi

if snapshot_alive; then
  if ((REPLACE == 0)); then
    log_event "already_running" "snapshot_url" "$SNAPSHOT_URL"
    echo "agentload watcher: Agent Load already responds at $SNAPSHOT_URL; rerun with --replace to quit it first." >&2
    exit 69
  fi

  log_event "replace_requested" "quit_url" "$QUIT_URL"
  if ! request_existing_quit; then
    log_event "replace_failed" "reason" "quit_api_failed" "quit_url" "$QUIT_URL"
    echo "agentload watcher: failed to request existing app quit through $QUIT_URL" >&2
    exit 69
  fi

  for _ in {1..50}; do
    if ! snapshot_alive; then
      break
    fi
    sleep 0.2
  done

  if snapshot_alive; then
    log_event "replace_failed" "reason" "snapshot_still_responding" "snapshot_url" "$SNAPSHOT_URL"
    echo "agentload watcher: existing app still responds at $SNAPSHOT_URL" >&2
    exit 69
  fi
fi

log_event "start" "app_bin" "$APP_BIN" "app_bundle" "$APP_BUNDLE" "stdout_log" "$STDOUT_LOG" "stderr_log" "$STDERR_LOG"
if ((${#APP_ARGS[@]} > 0)); then
  "$APP_BIN" "${APP_ARGS[@]}" >>"$STDOUT_LOG" 2>>"$STDERR_LOG" &
else
  "$APP_BIN" >>"$STDOUT_LOG" 2>>"$STDERR_LOG" &
fi
CHILD_PID=$!
printf '%s\n' "$CHILD_PID" >"$PID_FILE"
log_event "started" "child_pid" "$CHILD_PID" "pid_file" "$PID_FILE"

set +e
wait "$CHILD_PID"
STATUS=$?
set -e

if [[ -f "$PID_FILE" ]] && [[ "$(cat "$PID_FILE" 2>/dev/null)" == "$CHILD_PID" ]]; then
  rm -f "$PID_FILE"
fi

if ((STATUS >= 128)); then
  log_event "exited" "child_pid" "$CHILD_PID" "exit_status" "$STATUS" "signal_number" "$((STATUS - 128))"
else
  log_event "exited" "child_pid" "$CHILD_PID" "exit_status" "$STATUS"
fi

if ((HOLD_AFTER_EXIT == 1)); then
  HOLDING=1
  log_event "hold_after_exit" "child_pid" "$CHILD_PID" "exit_status" "$STATUS"
  while true; do
    sleep 3600
  done
fi

exit "$STATUS"
