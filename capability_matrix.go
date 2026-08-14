package main

import (
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
	Agent      string   `json:"agent"`
	Process    string   `json:"process_identity"`
	Discovery  string   `json:"evidence_discovery"`
	Transcript string   `json:"transcript_evidence"`
	Usage      string   `json:"output_usage"`
	Evidence   []string `json:"evidence,omitempty"`
	Note       string   `json:"note,omitempty"`
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
			Agent:      adapter.ID,
			Process:    capabilityState(adapter.Capabilities.Process != nil),
			Discovery:  capabilityState(adapter.Capabilities.Discovery != nil),
			Transcript: capabilityState(adapter.Capabilities.Transcript != nil),
			Usage:      capabilityState(adapter.Capabilities.Usage != nil),
			Evidence:   append([]string(nil), adapter.Evidence...),
			Note:       adapter.Note,
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
