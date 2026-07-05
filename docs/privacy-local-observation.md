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

## Data Sent Off Device

The intended release build sends no observed activity data off device.

The embedded dashboard is served from a loopback address on the same Mac. It is
not a cloud service.

## Data Sharing

The intended release build does not share observed activity data with third
parties.

## Retention

Live process observations are refreshed in memory as the app runs. If local
history is enabled, history samples are stored in the configured local history
file. Users can delete that file to remove retained history.

## User Control

Users control which local tool roots are configured for transcript evidence.
For an App Store build, any required folder access should be granted through
explicit user folder selection.

## Future Changes

If Agent Load adds telemetry, sync, cloud analysis, account features, crash
reporting, or third-party AI processing, this policy and App Store privacy
answers must be updated before release.
