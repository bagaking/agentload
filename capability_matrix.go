package main

import (
	"agentload/internal/snapshot"
	"fmt"
	"sort"
	"strings"
)

// Capability matrix rendering.
//
// The published capability table used to be hand-written, which let code and
// docs drift apart silently: the antigravity evidence root shipped in the
// gemini adapter and the table never mentioned it. The registry is the only
// thing that knows what each adapter actually parses, so the table is rendered
// from the registry and a test pins the doc against this output.
//
// A cell says what the capability slots say and nothing more. A nil slot is
// "unsupported" -- never "probably" and never an inferred parity.

const (
	capabilityStateSupported   = "supported"
	capabilityStateUnsupported = "unsupported"
)

// capabilityMatrixMarkers delimit the generated region inside the doc. Text
// outside them is hand-written prose and is never touched.
const (
	capabilityMatrixBeginMarker = "<!-- BEGIN GENERATED CAPABILITY MATRIX -->"
	capabilityMatrixEndMarker   = "<!-- END GENERATED CAPABILITY MATRIX -->"
)

type capabilityMatrixRow struct {
	Agent          string                   `json:"agent"`
	Process        string                   `json:"process_identity"`
	Discovery      string                   `json:"evidence_discovery"`
	Transcript     string                   `json:"transcript_evidence"`
	Usage          string                   `json:"output_usage"`
	Evidence       []string                 `json:"evidence,omitempty"`
	Note           string                   `json:"note,omitempty"`
	SignalFamilies []capabilitySignalFamily `json:"signal_families,omitempty"`
}

type capabilitySignalFamily struct {
	Key      string
	State    string
	Evidence []string
}

const (
	capabilityStatePartial       = "partial"
	capabilityStateUnavailable   = "unavailable"
	capabilityStateNotConfigured = "not_configured"
	capabilityStateObserved      = "observed"
)

// signalFamilies projects the four registered evidence slots into the seven
// user-facing families. The mapping is deliberately conservative: a family is
// supported only when the slot that produces it exists, partial when only one
// half of a compound signal exists, and not_configured for opt-in telemetry.
func signalFamilies(adapter codingAgentAdapter) []capabilitySignalFamily {
	process := adapter.Capabilities.Process != nil
	discovery := adapter.Capabilities.Discovery != nil
	transcript := adapter.Capabilities.Transcript != nil
	usage := adapter.Capabilities.Usage != nil
	state := func(ok bool) string {
		if ok {
			return capabilityStateObserved
		}
		return capabilityStateUnavailable
	}
	compound := func(a, b bool) string {
		switch {
		case a && b:
			return capabilityStateObserved
		case a || b:
			return capabilityStatePartial
		default:
			return capabilityStateUnavailable
		}
	}
	return []capabilitySignalFamily{
		{Key: "presence_resources", State: state(process), Evidence: []string{"process identity"}},
		{Key: "sessions_roles", State: compound(discovery, transcript), Evidence: []string{"evidence discovery", "transcript evidence"}},
		{Key: "recent_movement", State: state(transcript), Evidence: []string{"transcript event timestamps"}},
		{Key: "tokens_cost", State: state(usage), Evidence: []string{"output usage decoder"}},
		{Key: "quota_windows", State: capabilityStateUnavailable, Evidence: []string{"no local quota ledger registered"}},
		{Key: "workspace_context", State: compound(discovery, transcript), Evidence: []string{"transcript/project evidence"}},
		{Key: "consented_telemetry", State: capabilityStateNotConfigured, Evidence: []string{"runtime telemetry is opt-in"}},
	}
}

func (r *codingAgentRegistry) capabilitySnapshot() []snapshot.CapabilityMatrixRow {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rows := make([]snapshot.CapabilityMatrixRow, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		families := signalFamilies(adapter)
		familyRows := make([]snapshot.CapabilitySignalFamily, 0, len(families))
		for _, family := range families {
			familyRows = append(familyRows, snapshot.CapabilitySignalFamily{Key: family.Key, State: family.State, Evidence: append([]string(nil), family.Evidence...)})
		}
		row := capabilityMatrixRow{Agent: adapter.ID, Process: capabilityState(adapter.Capabilities.Process != nil), Discovery: capabilityState(adapter.Capabilities.Discovery != nil), Transcript: capabilityState(adapter.Capabilities.Transcript != nil), Usage: capabilityState(adapter.Capabilities.Usage != nil), Evidence: append([]string(nil), adapter.Evidence...), Note: adapter.Note, SignalFamilies: families}
		rows = append(rows, snapshot.CapabilityMatrixRow{Agent: row.Agent, Process: row.Process, Discovery: row.Discovery, Transcript: row.Transcript, Usage: row.Usage, Evidence: row.Evidence, Note: row.Note, SignalFamilies: familyRows})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Agent < rows[j].Agent })
	return rows
}

func capabilityState(supported bool) string {
	if supported {
		return capabilityStateSupported
	}
	return capabilityStateUnsupported
}

// capabilityMatrix reads the registry's own capability slots. It takes no
// opinion about what an agent "should" support: the slots are the evidence.
func (r *codingAgentRegistry) capabilityMatrix() []capabilityMatrixRow {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rows := make([]capabilityMatrixRow, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		rows = append(rows, capabilityMatrixRow{
			Agent:          adapter.ID,
			Process:        capabilityState(adapter.Capabilities.Process != nil),
			Discovery:      capabilityState(adapter.Capabilities.Discovery != nil),
			Transcript:     capabilityState(adapter.Capabilities.Transcript != nil),
			Usage:          capabilityState(adapter.Capabilities.Usage != nil),
			Evidence:       append([]string(nil), adapter.Evidence...),
			Note:           adapter.Note,
			SignalFamilies: signalFamilies(adapter),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Agent < rows[j].Agent })
	return rows
}

// renderCapabilityMatrixMarkdown produces the generated region verbatim,
// markers included, so a writer can splice it and a test can compare it.
func renderCapabilityMatrixMarkdown(rows []capabilityMatrixRow) string {
	var b strings.Builder
	b.WriteString(capabilityMatrixBeginMarker)
	b.WriteString("\n")
	b.WriteString("<!-- Generated from defaultCodingAgentRegistry by TestCapabilityMatrixDocMatchesTheRegistry. Do not edit by hand. -->\n\n")
	b.WriteString("| Agent | Process identity | Evidence discovery | Transcript evidence | Output usage | Evidence read |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, row := range rows {
		evidence := "none"
		if len(row.Evidence) > 0 {
			quoted := make([]string, 0, len(row.Evidence))
			for _, item := range row.Evidence {
				quoted = append(quoted, "`"+item+"`")
			}
			evidence = strings.Join(quoted, "<br>")
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n",
			row.Agent, row.Process, row.Discovery, row.Transcript, row.Usage, evidence))
	}
	notes := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Note != "" {
			notes = append(notes, fmt.Sprintf("- **%s**: %s\n", row.Agent, row.Note))
		}
	}
	if len(notes) > 0 {
		b.WriteString("\n")
		for _, note := range notes {
			b.WriteString(note)
		}
	}
	b.WriteString("\n### Signal family coverage\n\n")
	b.WriteString("| Agent | Signal family | State | Evidence |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, row := range rows {
		for _, family := range row.SignalFamilies {
			evidence := "none"
			if len(family.Evidence) > 0 {
				evidence = strings.Join(family.Evidence, "<br>")
			}
			b.WriteString(fmt.Sprintf("| %s | `%s` | %s | %s |\n", row.Agent, family.Key, family.State, evidence))
		}
	}
	b.WriteString("\n")
	b.WriteString(capabilityMatrixEndMarker)
	return b.String()
}

// spliceCapabilityMatrix replaces the generated region and leaves every other
// byte alone. It fails rather than guessing when the markers are missing, so a
// mangled doc is never silently rewritten wholesale.
func spliceCapabilityMatrix(doc, generated string) (string, error) {
	start := strings.Index(doc, capabilityMatrixBeginMarker)
	end := strings.Index(doc, capabilityMatrixEndMarker)
	if start < 0 || end < 0 || end < start {
		return "", fmt.Errorf("capability matrix markers not found in document")
	}
	return doc[:start] + generated + doc[end+len(capabilityMatrixEndMarker):], nil
}
