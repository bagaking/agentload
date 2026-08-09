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
