# Apple Distribution Readiness

This page is the working distribution record for Agent Load. It covers the
first three Mac App Store readiness areas: sandbox posture, privacy posture,
and product positioning. It does not claim the app is ready for App Store
upload yet.

## Source Baseline

Apple distribution requirements that affect this app:

- Mac App Store builds are expected to use App Sandbox. Apple describes App
  Sandbox as limiting access to system resources and user data through declared
  entitlements.
- App Store Connect requires developers to explain privacy practices and data
  handling for App Store apps.
- Apple review guidance for privacy policies expects clear statements about
  what data is collected, how it is collected, how it is used, sharing,
  retention, deletion, and consent.
- If a macOS app uses temporary sandbox exception entitlements, App Store
  Connect requires usage information for each exception.

## 1. Sandbox Posture

Current status: `not App Store ready`.

Agent Load currently observes local AI coding activity by combining:

- the current user's visible process table
- process resource data such as PID, CPU, memory, elapsed time, and command text
- process-opened session files when available
- local transcript and history files under user-configured tool roots
- a local-only HTTP server bound to loopback for the embedded dashboard

The current local build is suitable for direct development and Developer ID
notarization work. The Mac App Store path needs a sandbox proof pass because
the core observation model depends on process and filesystem visibility outside
the app container.

### Entitlements Draft

`macos/AppStore.entitlements` is a draft baseline only:

- `com.apple.security.app-sandbox`
- `com.apple.security.network.client`
- `com.apple.security.network.server`
- `com.apple.security.files.user-selected.read-only`

Rationale:

- loopback dashboard serving needs local network server behavior
- user-selected read-only file access is the least surprising path for
  transcript/history roots if an App Store variant requires explicit folder
  selection
- no write access outside app-owned storage is requested in the baseline

Known gap:

- The draft does not prove process discovery through `ps` and `lsof` will meet
  App Store sandbox review expectations. The App Store variant may need to
  degrade process mapping, ask the user to select evidence folders, or move the
  full process observer to a non-App-Store distribution channel.

### Sandbox Decision

Recommended distribution split:

- Developer ID notarized build: preserve the full local observer.
- Mac App Store candidate: keep the UI and local transcript analytics, but make
  process/resource mapping an explicitly disclosed, best-effort feature that is
  disabled or degraded if sandbox access is unavailable.

Before an App Store upload, add a sandbox test target and verify:

- app launches with App Sandbox enabled
- local dashboard still loads
- user-selected transcript roots persist through security-scoped bookmarks, if
  implemented
- process discovery either works within allowed APIs or fails closed with a
  visible local-only explanation
- no temporary exception entitlement is added without a matching App Store
  Connect usage explanation

## 2. Privacy Posture

Agent Load should be represented as local-only by default.

Data observed locally:

- running process identity for supported AI coding tools
- process resource values: PID, CPU, memory, elapsed time
- command text when provided by the operating system process table
- local session IDs and mapped project names inferred from local evidence
- local transcript-derived timing, freshness, and token totals when available
- host app identity when it can be inferred locally
- local history samples saved by the app if history is enabled

Data not intended to be collected by the developer:

- no account identity
- no analytics SDK events
- no advertising identifiers
- no tracking across apps or websites
- no cloud upload of transcript contents, session IDs, process commands, or
  resource metrics

Retention:

- live process data is in-memory and refreshes with the dashboard snapshot
- local history retention is controlled by the configured history file
- users can delete local history by deleting the configured history file
- app deletion removes app-container data for a sandboxed build, but not
  user-selected external files

Consent model:

- App Store metadata and onboarding copy should state that the app reads local
  process and local tool evidence to show current AI coding load.
- If an App Store build requires selected folders, folder selection must be an
  explicit user action.
- Any future cloud sync, telemetry, or third-party AI feature must be opt-in and
  must update the privacy policy before release.

Draft App Privacy answers:

- Data collected by developer: `None`, if no telemetry or uploads are added.
- Data processed on device: process/resource information, local session
  metadata, transcript-derived counts, and optional local history.
- Tracking: `No`.
- Linked to user: `No`, unless account sync is later introduced.

## 3. Product Positioning

Safe positioning:

Agent Load is a local macOS menu bar utility for developers who run multiple AI
coding agents. It helps the user understand current local agent activity,
session freshness, process pressure, and recent project load on their own Mac.

Avoid these claims:

- team surveillance
- employee monitoring
- compliance audit
- security scanner
- background exfiltration or cloud analysis
- guaranteed process attribution

Preferred App Store category:

- Developer Tools

Draft subtitle:

Local AI coding activity monitor

Draft short description:

Agent Load shows current local AI coding sessions, process pressure, and recent
project activity from evidence on your Mac. It runs locally and does not upload
your activity data.

Draft review note:

Agent Load is intended for a developer monitoring their own Mac. The app reads
local process and local tool evidence to display current AI coding load and
session freshness. It does not include advertising, analytics SDKs, tracking, or
cloud upload. Local HTTP serving is bound to loopback for the embedded menu bar
dashboard. If the submitted build is sandboxed, process and file access are
limited to permitted system visibility and user-selected local evidence.

## Open App Store Risks

- process command visibility may be considered sensitive
- `lsof`-style file association may not be acceptable in sandboxed review
- token totals and session IDs must be clearly framed as local metadata
- product copy must make it obvious the app is for the current user's own Mac
- any future telemetry changes the App Privacy answer and privacy policy

## Next Step

Build a sandbox test target using `macos/AppStore.entitlements`, then record
which observer features still work under sandbox and which must be disabled for
an App Store build.
