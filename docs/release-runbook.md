# Release Runbook

Status: living document, last verified 2026-07-27.

How to produce and ship a distributable Agent Load build. Scope: local
packaging today, with the notarized/Sparkle path staked out for later
(`project_plan/M06_S01.notarized_sparkle_distribution.md`).

## Local package

```sh
./scripts/package_macos_app.sh
```

The script builds the UI and Go binary via `build_macos_app.sh`, signs the
bundle, verifies the signature, and produces two artifacts in `dist/`
(git-ignored):

- `AgentLoad-<version>.zip` — `ditto` archive for direct download.
- `AgentLoad-<version>.dmg` — drag-to-Applications disk image.

`<version>` is the timestamp-derived `CFBundleVersion` stamped by
`build_macos_app.sh`.

## Signing modes

- **Developer ID** — used automatically when a `Developer ID Application`
  identity exists in the keychain. Signs with hardened runtime + timestamp,
  which is the prerequisite for notarization.
- **Ad-hoc fallback** — used when no identity is present. The app runs on the
  build machine, but other Macs get a Gatekeeper warning (right-click → Open
  still works). `spctl --assess` reports "not accepted" for ad-hoc builds;
  the script prints this as informational, not a failure.

`macos/AppStore.entitlements` is the sandboxed App Store profile and is
intentionally **not** applied to direct-distribution builds: the sandbox
blocks the `ps`/`lsof` process scans the observer depends on. The two-tier
capability decision lives in `project_plan/M06_S04.app_store_sandbox_spike.md`.

## Future: notarization and update channel (M06_S01)

When a Developer ID certificate is available:

1. `./scripts/package_macos_app.sh` (signs with Developer ID automatically).
2. `xcrun notarytool submit dist/AgentLoad-<version>.zip --keychain-profile <profile> --wait`
3. `xcrun stapler staple "dist/Agent Load.app"` and re-create the zip/DMG from
   the stapled bundle.
4. Sparkle: add `SUFeedURL` + EdDSA public key to `macos/Info.plist`, sign the
   archive with Sparkle's `sign_update`, and publish the appcast.
5. Homebrew cask: publish a cask pointing at the notarized DMG with a
   `sha256` pin.

Distribution strategy and store constraints: `docs/apple-distribution-readiness.md`.

## Baseline Performance Metrics (M01_S01)

Measured on macOS Darwin 25.3.0 (Apple Silicon, arm64, 2026-09-13):

| Metric | Measured Baseline | Release Gate Target | Measurement Method |
|---|---|---|---|
| **Idle CPU** | 0.0% – 0.1% | < 1.0% | `ps -o %cpu -p <pid>` over 10s idle observation |
| **API Latency (`/api/snapshot`)** | p50: 2.53ms, p95: 97.9ms | p95 < 250ms | HTTP client benchmark across 20 sequential fetches |
| **Popover Hot Opening** | < 16ms | < 50ms | Native NSPanel showURL with pre-warmed WKWebView |
| **Binary Size** | 12 MB (DMG: 8.3 MB, Zip: 7.8 MB) | < 25 MB | `ls -lh "dist/Agent Load.app/Contents/MacOS/agentload"` |

