package main

import (
	"bufio"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentload/internal/snapshot"
)

// M01_S02 KR3: adding a vendor must stay a bounded, local job.
//
// The claim the sprint makes is "a new vendor touches only its own code and
// costs under 200 lines". That claim is only worth something if something
// measures it, so this file IS the stub vendor -- a complete adapter for a
// fictional agent, filling all four capability slots against a plausible JSONL
// layout -- and the test at the bottom counts the lines between the markers
// and fails if the number grows past the budget.
//
// It is a test file on purpose. A stub vendor in production would be a vendor
// nobody has, shipped to everybody.

// --- stub vendor: begin ---

const stubVendorID = "stubvendor"

// stubVendorProcess identifies the agent's processes and maps a session id
// back to the file that holds it.
type stubVendorProcess struct{}

func (stubVendorProcess) MatchesCommand(command processCommand) bool {
	return strings.Contains(command.ExecutableBase, stubVendorID)
}

func (stubVendorProcess) DisplayIdentity(processCommand) string { return stubVendorID }

func (stubVendorProcess) TranscriptFileForPath(path string) (snapshot.TranscriptFile, bool) {
	if !strings.HasSuffix(path, ".jsonl") {
		return snapshot.TranscriptFile{}, false
	}
	return snapshot.TranscriptFile{Tool: stubVendorID, Path: filepath.Clean(path)}, true
}

func (stubVendorProcess) RootFromTranscriptPath(path string) string {
	return filepath.Dir(filepath.Clean(path))
}

func (stubVendorProcess) TranscriptForSessionID(roots []string, sessionID string) (snapshot.TranscriptFile, bool) {
	if strings.TrimSpace(sessionID) == "" {
		return snapshot.TranscriptFile{}, false
	}
	for _, root := range roots {
		candidate := filepath.Join(root, sessionID+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return snapshot.TranscriptFile{Tool: stubVendorID, Path: candidate}, true
		}
	}
	return snapshot.TranscriptFile{}, false
}

func (stubVendorProcess) RootsFromCommand(processCommand) []string { return nil }

// stubVendorDiscovery walks the roots for session files.
type stubVendorDiscovery struct{}

func (d stubVendorDiscovery) Discover(_ context.Context, agentID string, roots []string, cutoff time.Time) transcriptDiscoveryResult {
	result := transcriptDiscoveryResult{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			file, ok := d.Classify(agentID, roots, filepath.Join(root, entry.Name()))
			if !ok {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				result.Errors = append(result.Errors, entry.Name()+": "+err.Error())
				continue
			}
			result.VisitedEntries++
			// An aged-out file is reported, never dropped silently: it is real
			// evidence this snapshot does not cover.
			if !cutoff.IsZero() && info.ModTime().Before(cutoff) {
				result.AgedOutFiles++
				continue
			}
			result.Files = append(result.Files, discoveredTranscriptFile{File: file, Info: info})
		}
	}
	return result
}

func (stubVendorDiscovery) Classify(agentID string, _ []string, path string) (snapshot.TranscriptFile, bool) {
	if !strings.HasSuffix(path, ".jsonl") {
		return snapshot.TranscriptFile{}, false
	}
	return snapshot.TranscriptFile{Tool: agentID, Path: filepath.Clean(path)}, true
}

type stubVendorLine struct {
	At     string `json:"at"`
	Role   string `json:"role"`
	Output int    `json:"output_tokens"`
}

// stubVendorParser reads the session file into a trace. It declares
// CanAppend false, so the scanner re-reads rather than trusting an offset --
// the honest default until incremental parsing is actually proven.
type stubVendorParser struct{}

func (stubVendorParser) Parse(file snapshot.TranscriptFile) (*snapshot.SessionTrace, error) {
	handle, err := os.Open(file.Path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	trace := &snapshot.SessionTrace{
		Tool:      stubVendorID,
		Path:      file.Path,
		SessionID: strings.TrimSuffix(filepath.Base(file.Path), ".jsonl"),
	}
	scanner := bufio.NewScanner(handle)
	for scanner.Scan() {
		var line stubVendorLine
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		at, err := time.Parse(time.RFC3339, line.At)
		if err != nil {
			continue
		}
		trace.EventTimes = append(trace.EventTimes, at)
		trace.TokenUsage.OutputTokens += line.Output
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	// finalizeTrace derives first/last from EventTimes, so the adapter reports
	// the times it observed rather than computing the window itself.
	finalizeTrace(trace)
	return trace, nil
}

func (p stubVendorParser) ParseTail(file snapshot.TranscriptFile) (*snapshot.SessionTrace, error) {
	return p.Parse(file)
}

func (stubVendorParser) ParseAppend(snapshot.TranscriptFile, *snapshot.SessionTrace, int64) (*snapshot.SessionTrace, error) {
	return nil, nil
}

func (stubVendorParser) CanAppend(snapshot.TranscriptFile) bool { return false }

// stubVendorUsage decodes per-line output tokens for the live rate.
type stubVendorUsage struct{}

func (stubVendorUsage) DecodeUsage(line []byte) (liveTokenRateObservation, bool) {
	var parsed stubVendorLine
	if json.Unmarshal(line, &parsed) != nil || parsed.Output <= 0 {
		return liveTokenRateObservation{}, false
	}
	at, err := time.Parse(time.RFC3339, parsed.At)
	if err != nil {
		return liveTokenRateObservation{}, false
	}
	return liveTokenRateObservation{At: at, OutputTokens: int64(parsed.Output)}, true
}

func stubVendorAdapter(roots []string) codingAgentAdapter {
	return codingAgentAdapter{
		ID:       stubVendorID,
		Roots:    roots,
		Evidence: []string{"<root>/*.jsonl"},
		Capabilities: agentCapabilities{
			Process:    stubVendorProcess{},
			Discovery:  stubVendorDiscovery{},
			Transcript: stubVendorParser{},
			Usage:      stubVendorUsage{},
		},
	}
}

// --- stub vendor: end ---

const stubVendorLineBudget = 200

// TestStubVendorAdapterStaysUnderTheIntegrationBudget is KR3's measurement.
// The budget is a real gate: if wiring a vendor grows past it, the abstraction
// stopped paying for itself and the sprint's promise is no longer true.
func TestStubVendorAdapterStaysUnderTheIntegrationBudget(t *testing.T) {
	const self = "stub_vendor_adapter_test.go"
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("read %s: %v", self, err)
	}
	body := string(raw)
	start := strings.Index(body, "// --- stub vendor: begin ---")
	end := strings.Index(body, "// --- stub vendor: end ---")
	if start < 0 || end < 0 {
		t.Fatalf("stub vendor markers missing from %s", self)
	}
	lines := 0
	for _, line := range strings.Split(body[start:end], "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		lines++
	}
	if lines > stubVendorLineBudget {
		t.Fatalf("a new vendor now costs %d lines, over the %d-line budget M01_S02 promised.\n"+
			"Either the adapter seam grew a requirement that belongs in shared code,\n"+
			"or the budget needs renegotiating in the sprint -- not silently raising here.",
			lines, stubVendorLineBudget)
	}
	t.Logf("stub vendor integration cost: %d lines (budget %d)", lines, stubVendorLineBudget)
}

// TestStubVendorTouchesOnlyItsOwnFile is the other half of the KR3 claim: the
// cost is bounded AND local. A new vendor that needs edits scattered through
// observer.go or transcripts.go has not been made cheap, only made to look
// cheap. This asserts every stub identifier is declared in this file.
func TestStubVendorTouchesOnlyItsOwnFile(t *testing.T) {
	const self = "stub_vendor_adapter_test.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, self, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", self, err)
	}
	declared := map[string]bool{}
	for _, decl := range file.Decls {
		switch node := decl.(type) {
		case *ast.FuncDecl:
			declared[node.Name.Name] = true
		case *ast.GenDecl:
			for _, spec := range node.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					declared[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, name := range s.Names {
						declared[name.Name] = true
					}
				}
			}
		}
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == self || !strings.HasSuffix(name, ".go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(body), stubVendorID) {
			t.Fatalf("%s mentions %q: wiring a vendor must not require edits outside its own file",
				name, stubVendorID)
		}
	}
	for _, want := range []string{"stubVendorProcess", "stubVendorDiscovery", "stubVendorParser", "stubVendorUsage", "stubVendorAdapter"} {
		if !declared[want] {
			t.Fatalf("%s is not declared in %s", want, self)
		}
	}
}

// TestStubVendorRegistersAndParsesEndToEnd proves the stub is a working
// adapter rather than four types that merely satisfy interfaces. A budget
// measured against code that cannot parse anything measures nothing.
func TestStubVendorRegistersAndParsesEndToEnd(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session-a.jsonl")
	body := `{"at":"2026-09-20T10:00:00Z","role":"user","output_tokens":0}
{"at":"2026-09-20T10:00:05Z","role":"assistant","output_tokens":42}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}

	registry := newCodingAgentRegistry(stubVendorAdapter([]string{root}))

	discovered := registry.discoverTranscripts(context.Background(), time.Time{})
	if len(discovered.Files) != 1 || discovered.Files[0].File.Tool != stubVendorID {
		t.Fatalf("expected the stub root to yield one transcript, got %+v", discovered.Files)
	}

	parser, ok := registry.transcriptParser(stubVendorID)
	if !ok {
		t.Fatalf("stub vendor declared a transcript capability but the registry did not expose it")
	}
	trace, err := parser.Parse(snapshot.TranscriptFile{Tool: stubVendorID, Path: path})
	if err != nil {
		t.Fatalf("parse stub transcript: %v", err)
	}
	if len(trace.EventTimes) != 2 || trace.TokenUsage.OutputTokens != 42 {
		t.Fatalf("expected 2 events and 42 output tokens, got %d events / %+v", len(trace.EventTimes), trace.TokenUsage)
	}
	if trace.SessionID != "session-a" {
		t.Fatalf("expected the session id from the filename, got %q", trace.SessionID)
	}

	usage, ok := registry.usageDecoder(stubVendorID)
	if !ok {
		t.Fatalf("stub vendor declared a usage capability but the registry did not expose it")
	}
	observation, ok := usage.DecodeUsage([]byte(`{"at":"2026-09-20T10:00:05Z","output_tokens":42}`))
	if !ok || observation.OutputTokens != 42 {
		t.Fatalf("expected the usage decoder to read 42 output tokens, got %+v (ok=%v)", observation, ok)
	}

	rows := registry.capabilityMatrix()
	if len(rows) != 1 || rows[0].Transcript != capabilityStateSupported || len(rows[0].Evidence) != 1 {
		t.Fatalf("the stub vendor must reach the capability matrix fully declared, got %+v", rows)
	}
}
