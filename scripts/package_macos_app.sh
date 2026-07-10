#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP_NAME="Agent Load"
APP_DIR="$ROOT/dist/${APP_NAME}.app"

"$ROOT/build_macos_app.sh"

VERSION="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$APP_DIR/Contents/Info.plist")"

# Direct-distribution signing. AppStore.entitlements is the sandboxed App Store
# profile and must not be applied here: the sandbox blocks the ps/lsof scans
# this build relies on.
SIGN_ID="$(security find-identity -v -p codesigning 2>/dev/null | awk -F'"' '/Developer ID Application/ {print $2; exit}')"
if [ -n "$SIGN_ID" ]; then
  MODE="developer-id"
  codesign --force --options runtime --timestamp --sign "$SIGN_ID" "$APP_DIR"
else
  MODE="ad-hoc"
  codesign --force --sign - "$APP_DIR"
  echo "notice: no Developer ID Application identity found; ad-hoc signed." >&2
  echo "notice: other Macs will see a Gatekeeper warning until a Developer ID + notarization is used (see docs/release-runbook.md)." >&2
fi

codesign --verify --deep --strict "$APP_DIR"
if spctl --assess --type execute "$APP_DIR" 2>/dev/null; then
  echo "spctl: accepted"
else
  echo "spctl: not accepted (expected for ${MODE} builds; informational only)"
fi

ZIP_PATH="$ROOT/dist/AgentLoad-${VERSION}.zip"
rm -f "$ZIP_PATH"
ditto -c -k --keepParent "$APP_DIR" "$ZIP_PATH"

DMG_PATH="$ROOT/dist/AgentLoad-${VERSION}.dmg"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
cp -R "$APP_DIR" "$STAGE/"
ln -s /Applications "$STAGE/Applications"
rm -f "$DMG_PATH"
hdiutil create -volname "$APP_NAME" -srcfolder "$STAGE" -ov -format UDZO "$DMG_PATH" >/dev/null

echo "version: $VERSION"
echo "signing: $MODE"
ls -lh "$ZIP_PATH" "$DMG_PATH" | awk '{print $9 ": " $5}'
