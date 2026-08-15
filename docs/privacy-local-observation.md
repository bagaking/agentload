# Privacy: Local Observation Draft

This is the draft privacy-policy source for Agent Load. It is written for a
local-only build. Update it before release if telemetry, sync, cloud processing,
or third-party services are added.

## Summary

Agent Load runs on your Mac and helps you understand local AI coding activity.
The app processes local evidence on device to show current sessions, process
pressure, project activity, and recent trends.

Agent Load does not upload your activity data, transcript contents, session
identifiers, process commands, token totals, or project names to the developer.

## Local Data Processed

Agent Load may process the following data on your device:

- local AI coding process names, process IDs, CPU, memory, and elapsed time
- command text visible to the current macOS user
- local session IDs and session freshness
- project names inferred from local tool evidence
- local transcript-derived timing and token totals when available
- host app names when inferred from local process ancestry
- local history samples if history storage is enabled

Detailed trajectory access is a separate opt-in. When enabled, the local
trajectory service can process requests, assistant text, tool arguments and
results, recorded identities and relationships, context manifests, and source
locators from configured transcript roots. Original records are read on demand
through bounded evidence views. Resource observations continue to follow their
existing metric contracts.

Users may also explicitly save private experience, insight, or solution
annotations. These records contain the user's text, applicability metadata, and
references to local evidence. They are stored separately from the rebuildable
source index.

## Data Sent Off Device

The intended release build sends no observed activity data off device.

The embedded dashboard is served from a loopback address on the same Mac. It is
not a cloud service.

The trajectory CLI connects to the running local instance using a private
capability receipt. It accepts literal loopback HTTP endpoints, disables HTTP
proxies, and rejects redirects. It does not start a second service. The browser
API requires the local origin and instance bearer capability for detailed
content requests, together with the enabled content preference.

The embedded UI's content security policy loads scripts, connections, and
fonts from its own origin. It permits inline styles and data images, and
disables external frames, objects, and form submission. Trajectory observation
does not add remote hooks or agent-control endpoints.

## Data Sharing

The intended release build does not share observed activity data with third
parties.

## Retention

Live process observations are refreshed in memory as the app runs. If local
history is enabled, history samples are stored in the configured local history
file. Users can delete that file to remove retained history.

Native usage recovery stores checkpoints, message maxima and minute token
partitions under `<history-file>.throughput-index/usage.bbolt` (directory `0700`,
file `0600`). It shares configured transcript discovery with trajectory, but
retains no prompt, tool body or cleaned text and does not require detailed
content access. Recovery and online collection use the same native usage
decoders. Historical minute facts follow the metric retention contract; deleting
the usage index while the app is closed causes a resumable, idempotent rebuild
from local sources.

Trajectory data uses a private companion directory named
`<history-file>.trajectory`. Its directory permissions are `0700` and its
stored files use `0600`.

| Local data | Retention and removal |
| --- | --- |
| `trajectory.sqlite` | Private source locators, parser/recovery checkpoints, bounded candidates and irreproducible facts. Disabling content access closes its readers, clears in-memory content and watch cursors, and denies all detailed reads/writes. Useful persistent facts and recovery state are retained. Source logs remain owned by local coding tools. |
| Prior index structures | Read-only migration inputs, retired only after complete lossless verification and atomic cutover. Access revocation does not delete useful records. |
| `annotations.bbolt` | Explicit user annotations and evidence references. Disabling content access closes this store and makes it inaccessible through the API, while retaining its records for a later re-enable. Users can remove this file while the app is closed to erase annotations. |
| `access.json` | The saved local content-access preference. |
| `instance.json` | The running instance's endpoint and capability. The app removes its own receipt on shutdown. |

Removing a configured root withdraws its sources from queries on the next
authorized collection; identity, recovery state and irreproducible facts remain private. Retained annotations validate their evidence
against the current authorized sources when read. Changed, deleted, or
withdrawn evidence is reported as stale, missing, unavailable, or partial; an
old source locator never silently points to replacement content.

## User Control

Users control which local tool roots are configured for transcript evidence.
For an App Store build, any required folder access should be granted through
explicit user folder selection.

The native UI and `agentload traj access on/off` control detailed trajectory
access. All trajectory query, get, watch, and annotation operations require
that preference and the current local capability. An in-flight RPC checks the
preference again before returning detailed results.

Private trajectory text, annotations, event references, context evidence, and
attention explanations are excluded from `/api/snapshot` and the sanitized
diagnostic export. The opt-in trajectory API is documented in
[API reference](api-reference.md); its attention evidence rules are documented
in [neutral observation](neutral-observation-principles.md#trajectory-attention-evidence).

## Future Changes

If Agent Load adds telemetry, sync, cloud analysis, account features, crash
reporting, or third-party AI processing, this policy and App Store privacy
answers must be updated before release.
