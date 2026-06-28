#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
APP_NAME="Agent Load"
APP_DIR="$ROOT/dist/${APP_NAME}.app"
EXECUTABLE_NAME="agentload"
VERSION="$(date +%Y.%m.%d.%H%M%S)"

if [ ! -d "$ROOT/ui/node_modules" ]; then
  npm --prefix "$ROOT/ui" install
fi
npm --prefix "$ROOT/ui" run build

rm -rf "$APP_DIR"
mkdir -p "$APP_DIR/Contents/MacOS" "$APP_DIR/Contents/Resources"

CGO_ENABLED=1 go build -trimpath -o "$APP_DIR/Contents/MacOS/$EXECUTABLE_NAME" .

sed \
  -e "s/__EXECUTABLE_NAME__/$EXECUTABLE_NAME/g" \
  -e "s/__APP_VERSION__/$VERSION/g" \
  "$ROOT/macos/Info.plist" > "$APP_DIR/Contents/Info.plist"

echo "$APP_DIR"
