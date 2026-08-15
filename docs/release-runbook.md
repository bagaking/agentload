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

## Trajectory storage upgrade and restart

The source-backed layout keeps local session files as evidence and stores
source identities, checkpoints, sparse ranges, search candidates and
irreproducible exceptions. The hundreds-of-MiB target applies to the additional
Trajectory index. Original sessions, metric history, usage and user annotations
are retained and accounted for separately. See
[Trajectory requirements](trajectory-requirements.md) for the storage contract.

1. Quit the application normally before offline maintenance. One owner holds
   the history path; another installation or development instance must not write
   to it at the same time.
2. Budget the original index, the sole resumable shadow, journals and system VM
   together. Each write preserves at least 1 GiB of free disk space. An
   interrupted migration retains its original and committed checkpoint.
3. Run the built application's `traj compact` command. Repeating the command
   resumes the same shadow. Legacy recovery that requires a complete original
   digest accepts `--expected-input-sha256`; use the independently recorded
   digest. CLI details are in the [API reference](api-reference.md).
4. Require the final `ready` record after complete fact, identity and recovery
   metadata verification. Only verified cutover retires the old index; never
   delete it manually to make room or shorten the migration.
   When an external tool has removed source transcripts and the user explicitly
   chooses cutover, retain their complete original facts as verified exceptions.
   Keep the original protected inventory and a separate approved absence ledger;
   report missing raw honestly. Every remaining protected prefix still requires
   verification, and newly missing paths are not implicitly approved.
5. Install the exact signed candidate used for verification. Check real cold
   and warm CLI/RPC pages, matched raw evidence, explicit counts, archive
   preparation completion and a later 300-second increment window. Record
   final index allocation and maintenance peak separately under `.bagakit/`.

On restart, archive discovery includes local sessions written while Agent Load
was stopped. Background preparation shares the online decoder, normalization
and generation rules; incomplete preparation remains visible in coverage.
Discovery periodically checks for previously missed files and appended records.
Historical throughput within the retention window is restored from native usage
counts and record timestamps, without replaying it into current TPS. Missing
CPU, memory or other unrecorded measurements remain missing. See
[metric semantics](agent-load-metric-semantics.md).

Native Popover first-paint timing that automation cannot observe is recorded as
unverified. It does not require a human opening/closing gate, and installation
or browser checks do not establish the native 150ms target.

## Baseline Performance Metrics (M01_S01)

Measured on macOS Darwin 25.3.0 (Apple Silicon, arm64, 2026-09-13):

These are historical component measurements, not acceptance evidence for the
current Trajectory build or its complete native Popover first paint.

| Metric | Measured Baseline | Release Gate Target | Measurement Method |
|---|---|---|---|
| **Idle CPU** | 0.0% – 0.1% | < 1.0% | `ps -o %cpu -p <pid>` over 10s idle observation |
| **API Latency (`/api/snapshot`)** | p50: 2.53ms, p95: 97.9ms | p95 < 250ms | HTTP client benchmark across 20 sequential fetches |
| **Popover Hot Opening** | < 16ms | < 50ms | Native NSPanel showURL with pre-warmed WKWebView |
| **Binary Size** | 12 MB (DMG: 8.3 MB, Zip: 7.8 MB) | < 25 MB | `ls -lh "dist/Agent Load.app/Contents/MacOS/agentload"` |
