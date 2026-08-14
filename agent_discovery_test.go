package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRegistryDiscoveryIsInjectedAndPriorityFilesAreDirect(t *testing.T) {
	root := t.TempDir()
	priorityPath := filepath.Join(root, "outside-layout", "priority.jsonl")
	writeDiscoveryFixture(t, priorityPath, time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC))

	discovery := &recordingTranscriptDiscovery{}
	registry := newCodingAgentRegistry(codingAgentAdapter{
		ID:    "test-agent",
		Roots: []string{root},
		Capabilities: agentCapabilities{
			Discovery:  discovery,
			Transcript: inertTranscriptParser{},
		},
	})
	evidenceIndex := newTranscriptEvidenceIndex(registry)
	candidates, errs := collectTranscriptCandidates(
		context.Background(),
		evidenceIndex,
		registry,
		[]TranscriptFile{{Tool: "test-agent", Path: priorityPath}},
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		time.Time{},
	)
	if len(errs) != 0 {
		t.Fatalf("unexpected discovery errors: %#v", errs)
	}
	if discovery.Calls != 1 {
		t.Fatalf("expected injected discovery capability to run once, got %d", discovery.Calls)
	}
	if len(candidates) != 1 || !candidates[0].Priority || candidates[0].File.Path != priorityPath {
		t.Fatalf("expected direct priority candidate outside walked layouts, got %#v", candidates)
	}
}

func TestClaudeDiscoveryKeepsWorkflowTranscriptsAndPrunesNonEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".claude")
	project := filepath.Join(root, "projects", "project-a")
	mainPath := filepath.Join(project, "session.jsonl")
	workflowPath := filepath.Join(project, "session-id", "subagents", "workflows", "wf-1", "agent.jsonl")
	ignored := []string{
		filepath.Join(project, "memory", "memory.jsonl"),
		filepath.Join(project, "session-id", "tool-results", "result.jsonl"),
	}
	for _, path := range append([]string{mainPath, workflowPath}, ignored...) {
		writeDiscoveryFixture(t, path, time.Now())
	}

	registry := defaultCodingAgentRegistry(Config{ClaudeRoots: []string{root}})
	result := registry.discoverTranscripts(context.Background(), time.Time{})
	paths := discoveredPaths(result.Files)
	if !paths[mainPath] || !paths[workflowPath] {
		t.Fatalf("expected main and workflow transcripts, got %#v", paths)
	}
	for _, path := range ignored {
		if paths[path] {
			t.Fatalf("non-evidence Claude directory was traversed: %s", path)
		}
	}
	if result.PrunedDirectories != 2 {
		t.Fatalf("expected two known Claude directories to be pruned, got %+v", result)
	}
}

func TestDatedDiscoveryPrunesOldPartitionsAndTraeArtifactsBeforeVisitingFiles(t *testing.T) {
	root := t.TempDir()
	codexRoot := filepath.Join(root, ".codex")
	traeRoot := filepath.Join(root, ".trae", "cli")
	cutoff := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	recentTime := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)

	want := []string{
		filepath.Join(codexRoot, "sessions", "2026", "08", "10", "codex.jsonl"),
		filepath.Join(traeRoot, "sessions", "2026", "08", "10", "trae.jsonl"),
	}
	for _, path := range want {
		writeDiscoveryFixture(t, path, recentTime)
	}
	for index := 0; index < 128; index++ {
		writeDiscoveryFixture(t,
			filepath.Join(codexRoot, "sessions", "2026", "07", "01", "nested", fileName(index)),
			recentTime,
		)
		writeDiscoveryFixture(t,
			filepath.Join(traeRoot, "sessions", "2026", "08", "10", "rollout.artifacts", "nested", fileName(index)),
			recentTime,
		)
	}

	registry := defaultCodingAgentRegistry(Config{
		CodexRoots: []string{codexRoot},
		TraeRoots:  []string{traeRoot},
	})
	result := registry.discoverTranscripts(context.Background(), cutoff)
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected discovery errors: %#v", result.Errors)
	}
	paths := discoveredPaths(result.Files)
	for _, path := range want {
		if !paths[path] {
			t.Fatalf("missing recent transcript %s in %#v", path, paths)
		}
	}
	if len(paths) != len(want) {
		t.Fatalf("old partition or artifacts leaked into candidates: %#v", paths)
	}
	if result.PrunedDirectories < 2 {
		t.Fatalf("expected old date and artifact directories to be pruned, got %+v", result)
	}
	if result.VisitedEntries >= 32 {
		t.Fatalf("pruned fixture had 256 irrelevant files but visited %d entries", result.VisitedEntries)
	}
}

func TestDatedDiscoveryFailsClosedOnUnknownLayout(t *testing.T) {
	traeRoot := filepath.Join(t.TempDir(), ".trae", "cli")
	unknownPath := filepath.Join(traeRoot, "sessions", "legacy", "session.jsonl")
	writeDiscoveryFixture(t, unknownPath, time.Now())

	registry := defaultCodingAgentRegistry(Config{TraeRoots: []string{traeRoot}})
	result := registry.discoverTranscripts(context.Background(), time.Time{})
	if len(result.Files) != 0 {
		t.Fatalf("unknown layout must not fall back to recursive discovery: %#v", result.Files)
	}
	foundGap := false
	for _, message := range result.Errors {
		if strings.Contains(message, "evidence layout gap") && strings.Contains(message, "legacy") {
			foundGap = true
		}
	}
	if !foundGap {
		t.Fatalf("expected explicit layout gap, got %#v", result.Errors)
	}
}

func BenchmarkTraeDiscoveryPrunesArtifacts(b *testing.B) {
	traeRoot := filepath.Join(b.TempDir(), ".trae", "cli")
	day := filepath.Join(traeRoot, "sessions", "2026", "08", "10")
	writeBenchmarkFixture(b, filepath.Join(day, "session.jsonl"))
	for index := 0; index < 1024; index++ {
		writeBenchmarkFixture(b, filepath.Join(day, "session.artifacts", "nested", fileName(index)))
	}
	registry := defaultCodingAgentRegistry(Config{TraeRoots: []string{traeRoot}})

	b.ResetTimer()
	for range b.N {
		result := registry.discoverTranscripts(context.Background(), time.Time{})
		if len(result.Errors) != 0 || len(result.Files) != 1 {
			b.Fatalf("unexpected discovery result: %+v", result)
		}
		b.ReportMetric(float64(result.VisitedEntries), "entries/op")
	}
}

type recordingTranscriptDiscovery struct {
	Calls int
}

type inertTranscriptParser struct{}

func (inertTranscriptParser) Parse(TranscriptFile) (*SessionTrace, error) {
	return nil, nil
}

func (inertTranscriptParser) ParseTail(TranscriptFile) (*SessionTrace, error) {
	return nil, nil
}

func (inertTranscriptParser) ParseAppend(TranscriptFile, *SessionTrace, int64) (*SessionTrace, error) {
	return nil, nil
}

func (inertTranscriptParser) CanAppend(TranscriptFile) bool {
	return false
}

func (d *recordingTranscriptDiscovery) Discover(context.Context, string, []string, time.Time) transcriptDiscoveryResult {
	d.Calls++
	return transcriptDiscoveryResult{}
}

func (*recordingTranscriptDiscovery) Classify(string, []string, string) (TranscriptFile, bool) {
	return TranscriptFile{}, false
}

func writeDiscoveryFixture(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set mtime %s: %v", path, err)
	}
}

func writeBenchmarkFixture(b *testing.B, path string) {
	b.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		b.Fatalf("write %s: %v", path, err)
	}
}

func discoveredPaths(files []discoveredTranscriptFile) map[string]bool {
	paths := make(map[string]bool, len(files))
	for _, file := range files {
		paths[file.File.Path] = true
	}
	return paths
}

func fileName(index int) string {
	return "irrelevant-" + strconv.Itoa(index) + ".jsonl"
}

func TestGrokDiscoveryTakesUpdatesJSONLAndPrunesNonTranscripts(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".grok")
	session := filepath.Join(root, "sessions", "%2FUsers%2Fdev%2Fproj%2Fagentload", "01a058b6-f632-7102-abc9-6767c9ed332d")
	recent := time.Now().Add(-time.Hour)
	transcript := filepath.Join(session, "updates.jsonl")
	writeDiscoveryFixture(t, transcript, recent)
	// chat_history.jsonl has no timestamps at all, so it is not evidence; the
	// pruned subdirectories hold request payloads and terminal logs.
	writeDiscoveryFixture(t, filepath.Join(session, "chat_history.jsonl"), recent)
	writeDiscoveryFixture(t, filepath.Join(session, "recap_requests", "updates.jsonl"), recent)
	writeDiscoveryFixture(t, filepath.Join(session, "terminal", "updates.jsonl"), recent)

	registry := defaultCodingAgentRegistry(Config{GrokRoots: []string{root}})
	result := registry.discoverTranscripts(context.Background(), recent.Add(-24*time.Hour))

	var found []string
	for _, file := range result.Files {
		if file.File.Tool == "grok" {
			found = append(found, file.File.Path)
		}
	}
	if len(found) != 1 || found[0] != transcript {
		t.Fatalf("expected only the session updates.jsonl, got %#v", found)
	}

	if file, ok := registry.transcriptFileForEvidencePath(transcript); !ok || file.Tool != "grok" {
		t.Fatalf("expected the transcript to classify as grok, got %#v ok=%t", file, ok)
	}
	// A nested updates.jsonl is not a session transcript: the layout is exactly
	// <encoded-cwd>/<session-id>/updates.jsonl.
	if file, ok := registry.transcriptFileForEvidencePath(filepath.Join(session, "recap_requests", "updates.jsonl")); ok {
		t.Fatalf("expected nested updates.jsonl to be rejected, got %#v", file)
	}
}

// Cursor remains process-only; the local transcript-backed adapters must expose
// transcript support even before they gain a live output decoder.
func TestVendorsWithoutEvidenceDeclareNoTranscriptOrUsageCapability(t *testing.T) {
	registry := defaultCodingAgentRegistry(defaultConfig())
	for _, id := range []string{"gemini", "opencode", "hermes", "openclaw", "pi"} {
		if !registry.hasTranscript(id) {
			t.Fatalf("%s must declare transcript support", id)
		}
	}
	for _, id := range []string{"opencode", "hermes"} {
		if _, ok := registry.usageDecoder(id); ok {
			t.Fatalf("%s must not declare live usage support for a database-only source", id)
		}
	}
	for _, id := range []string{"cursor"} {
		if registry.hasTranscript(id) {
			t.Fatalf("%s must not declare transcript support without an adapter", id)
		}
		if _, ok := registry.usageDecoder(id); ok {
			t.Fatalf("%s must not declare usage support without token evidence on disk", id)
		}
	}
	if !registry.hasTranscript("grok") {
		t.Fatal("grok has timestamped transcripts and must declare transcript support")
	}
	if _, ok := registry.usageDecoder("grok"); !ok {
		t.Fatal("grok records per-turn token usage and must declare usage support")
	}
}
