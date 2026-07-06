package main

func defaultRuntimeTelemetrySnapshot() RuntimeTelemetrySnapshot {
	return RuntimeTelemetrySnapshot{
		Configured: false,
		Status:     "not_configured",
		EventCount: 0,
		Detail:     "Optional local runtime telemetry adapter is not configured; local process and session evidence remain the source of truth.",
		Adapters: []RuntimeTelemetryAdapterState{
			{
				Key:    "otel_local",
				Label:  "OpenTelemetry local receiver",
				Status: "not_configured",
				Detail: "Reserved adapter seam for future local spans or metrics.",
			},
			{
				Key:    "jsonl_local",
				Label:  "JSONL runtime events",
				Status: "not_configured",
				Detail: "Reserved adapter seam for local append-only runtime event files.",
			},
		},
	}
}
