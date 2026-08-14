package main

import (
	"os"
	"strings"
	"testing"
)

const capabilityMatrixDocPath = "docs/coding-agent-evidence-adapters.md"

// TestCapabilityMatrixDocMatchesTheRegistry is the release gate for the
// published capability table: the registry owns it, the doc only carries it.
//
// This exists because the table drifted for real. The antigravity evidence
// root shipped inside the gemini adapter and the hand-written table never
// mentioned it -- nothing could have caught that, because nothing compared the
// two. Run with -update to regenerate after a deliberate capability change.
func TestCapabilityMatrixDocMatchesTheRegistry(t *testing.T) {
	registry := defaultCodingAgentRegistry(defaultConfig())
	generated := renderCapabilityMatrixMarkdown(registry.capabilityMatrix())

	doc, err := os.ReadFile(capabilityMatrixDocPath)
	if err != nil {
		t.Fatalf("read capability matrix doc: %v", err)
	}
	updated, err := spliceCapabilityMatrix(string(doc), generated)
	if err != nil {
		t.Fatalf("%v -- the generated region must stay delimited by the markers", err)
	}
	if updated == string(doc) {
		return
	}
	if os.Getenv("UPDATE_CAPABILITY_MATRIX") != "" {
		if err := os.WriteFile(capabilityMatrixDocPath, []byte(updated), 0o644); err != nil {
			t.Fatalf("write capability matrix doc: %v", err)
		}
		t.Fatalf("capability matrix doc regenerated; re-run without UPDATE_CAPABILITY_MATRIX")
	}
	t.Fatalf("capability matrix doc is stale.\nRegenerate with:\n  UPDATE_CAPABILITY_MATRIX=1 go test . -run TestCapabilityMatrixDocMatchesTheRegistry\n\nwant generated region:\n%s", generated)
}

// TestCapabilityMatrixReportsNilSlotsAsUnsupported pins the honesty rule: a
// missing capability is reported as unsupported, never softened and never
// inferred from a sibling slot being present.
func TestCapabilityMatrixReportsNilSlotsAsUnsupported(t *testing.T) {
	registry := newCodingAgentRegistry(
		codingAgentAdapter{ID: "identity-only", Note: "host-app ancestry only"},
		codingAgentAdapter{
			ID:       "full",
			Evidence: []string{"sessions/*.jsonl"},
			Capabilities: agentCapabilities{
				Process:    newClaudeProcessIdentity(),
				Discovery:  claudeTranscriptDiscovery{},
				Transcript: newClaudeTranscriptParser(),
				Usage:      newClaudeOutputUsageDecoder(),
			},
		},
	)
	rows := registry.capabilityMatrix()
	if len(rows) != 2 {
		t.Fatalf("expected both adapters, got %+v", rows)
	}
	// Rows are sorted by agent id, so "full" precedes "identity-only".
	full, bare := rows[0], rows[1]
	if full.Agent != "full" || bare.Agent != "identity-only" {
		t.Fatalf("expected rows sorted by agent id, got %+v", rows)
	}
	for name, got := range map[string]string{
		"process": bare.Process, "discovery": bare.Discovery,
		"transcript": bare.Transcript, "usage": bare.Usage,
	} {
		if got != capabilityStateUnsupported {
			t.Fatalf("expected %s unsupported for a bare adapter, got %q", name, got)
		}
	}
	if len(bare.Evidence) != 0 {
		t.Fatalf("expected no evidence for a bare adapter, got %+v", bare.Evidence)
	}

	for name, got := range map[string]string{
		"process": full.Process, "discovery": full.Discovery,
		"transcript": full.Transcript, "usage": full.Usage,
	} {
		if got != capabilityStateSupported {
			t.Fatalf("expected %s supported, got %q", name, got)
		}
	}

	markdown := renderCapabilityMatrixMarkdown(rows)
	if !strings.Contains(markdown, "| identity-only | unsupported | unsupported | unsupported | unsupported | none |") {
		t.Fatalf("expected the bare adapter rendered as unsupported, got:\n%s", markdown)
	}
	if !strings.Contains(markdown, "host-app ancestry only") {
		t.Fatalf("expected the note to survive rendering, got:\n%s", markdown)
	}
}

// TestCapabilityMatrixDeclaresEveryEvidenceRootTheParserReads is the guard for
// the exact failure that motivated this: a second evidence root added to an
// existing adapter, invisible to the published table. A root the walk admits
// must be declared, so it reaches the matrix.
func TestCapabilityMatrixDeclaresEveryEvidenceRootTheParserReads(t *testing.T) {
	registry := defaultCodingAgentRegistry(defaultConfig())
	rows := registry.capabilityMatrix()
	byAgent := map[string]capabilityMatrixRow{}
	for _, row := range rows {
		byAgent[row.Agent] = row
	}
	for _, agent := range []string{"claude", "codex", "trae", "grok", "gemini", "opencode", "hermes", "openclaw", "pi"} {
		row, ok := byAgent[agent]
		if !ok {
			t.Fatalf("missing matrix row for %s", agent)
		}
		if row.Transcript == capabilityStateSupported && len(row.Evidence) == 0 {
			t.Fatalf("%s parses transcripts but declares no evidence shape", agent)
		}
	}
	gemini := byAgent["gemini"]
	if len(gemini.Evidence) != 2 {
		t.Fatalf("gemini reads two evidence roots (tmp chats and antigravity), declared %+v", gemini.Evidence)
	}
	found := false
	for _, item := range gemini.Evidence {
		if strings.Contains(item, "antigravity") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the antigravity root must reach the published matrix, got %+v", gemini.Evidence)
	}
}
