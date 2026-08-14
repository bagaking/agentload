package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type agentProcessIdentity interface {
	MatchesCommand(command processCommand) bool
	DisplayIdentity(command processCommand) string
	TranscriptFileForPath(path string) (TranscriptFile, bool)
	RootFromTranscriptPath(path string) string
	RootsFromCommand(command processCommand) []string
}

type agentTranscriptParser interface {
	Parse(file TranscriptFile) (*SessionTrace, error)
	ParseTail(file TranscriptFile) (*SessionTrace, error)
	ParseAppend(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error)
	CanAppend(file TranscriptFile) bool
}

type agentOutputUsageDecoder interface {
	DecodeUsage(line []byte) (liveTokenRateObservation, bool)
}

type agentCapabilities struct {
	Process    agentProcessIdentity
	Discovery  transcriptDiscoveryCapability
	Transcript agentTranscriptParser
	Usage      agentOutputUsageDecoder
}

type codingAgentAdapter struct {
	ID           string
	Roots        []string
	Capabilities agentCapabilities
}

// codingAgentRegistry is the code-owned composition root for coding-agent
// capabilities. Capability slots stay nil until their behavior has moved here;
// this prevents an agent name from implying evidence support.
type codingAgentRegistry struct {
	mu       sync.RWMutex
	adapters []codingAgentAdapter
	byID     map[string]int
}

// snapshotConfig applies registry-owned roots to the public snapshot shape.
// Keeping this projection beside the adapter registry prevents every caller
// that builds a snapshot from growing another vendor switch.
func (r *codingAgentRegistry) snapshotConfig(base SnapshotConfig, observedRoots map[string][]string) SnapshotConfig {
	if r == nil {
		return base
	}
	roots := r.roots()
	for agentID, agentRoots := range observedRoots {
		if len(agentRoots) > 0 {
			roots[agentID] = mergeStringSets(nil, agentRoots)
		}
	}
	base.ClaudeRoots = append([]string(nil), roots["claude"]...)
	base.CodexRoots = append([]string(nil), roots["codex"]...)
	base.TraeRoots = append([]string(nil), roots["trae"]...)
	base.GrokRoots = append([]string(nil), roots["grok"]...)
	return base
}

func newCodingAgentRegistry(adapters ...codingAgentAdapter) *codingAgentRegistry {
	registry := &codingAgentRegistry{byID: make(map[string]int, len(adapters))}
	for _, adapter := range adapters {
		adapter.ID = strings.TrimSpace(strings.ToLower(adapter.ID))
		if adapter.ID == "" {
			panic("coding agent adapter ID is required")
		}
		if _, exists := registry.byID[adapter.ID]; exists {
			panic(fmt.Sprintf("duplicate coding agent adapter %q", adapter.ID))
		}
		adapter.Roots = mergeStringSets(nil, adapter.Roots)
		registry.byID[adapter.ID] = len(registry.adapters)
		registry.adapters = append(registry.adapters, adapter)
	}
	return registry
}

func defaultCodingAgentRegistry(cfg Config) *codingAgentRegistry {
	return newCodingAgentRegistry(
		codingAgentAdapter{
			ID:    "claude",
			Roots: cfg.ClaudeRoots,
			Capabilities: agentCapabilities{
				Process:    newClaudeProcessIdentity(),
				Discovery:  claudeTranscriptDiscovery{},
				Transcript: newClaudeTranscriptParser(),
				Usage:      newClaudeOutputUsageDecoder(),
			},
		},
		codingAgentAdapter{
			ID:    "codex",
			Roots: cfg.CodexRoots,
			Capabilities: agentCapabilities{
				Process:    newCodexProcessIdentity(),
				Discovery:  codexTranscriptDiscovery{},
				Transcript: newCodexTranscriptParser(),
				Usage:      newCodexOutputUsageDecoder(),
			},
		},
		codingAgentAdapter{
			ID:    "trae",
			Roots: cfg.TraeRoots,
			Capabilities: agentCapabilities{
				Process:    newTraeProcessIdentity(),
				Discovery:  traeTranscriptDiscovery{},
				Transcript: newTraeTranscriptParser(),
				Usage:      newTraeOutputUsageDecoder(),
			},
		},
		codingAgentAdapter{
			ID:    "grok",
			Roots: cfg.GrokRoots,
			Capabilities: agentCapabilities{
				Process:    newGrokProcessIdentity(),
				Discovery:  grokTranscriptDiscovery{},
				Transcript: newGrokTranscriptParser(),
				Usage:      newGrokOutputUsageDecoder(),
			},
		},
		codingAgentAdapter{
			ID: "gemini",
			Capabilities: agentCapabilities{
				Process: newGeminiProcessIdentity(),
			},
		},
		codingAgentAdapter{
			ID: "opencode",
			Capabilities: agentCapabilities{
				Process: newOpenCodeProcessIdentity(),
			},
		},
		codingAgentAdapter{ID: "cursor"},
		codingAgentAdapter{
			ID: "hermes",
			Capabilities: agentCapabilities{
				Process: newHermesProcessIdentity(),
			},
		},
		codingAgentAdapter{ID: "openclaw"},
		codingAgentAdapter{ID: "pi"},
	)
}

func (r *codingAgentRegistry) mergeRoots(extra map[string][]string) {
	if r == nil || len(extra) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, roots := range extra {
		id = strings.TrimSpace(strings.ToLower(id))
		index, ok := r.byID[id]
		if !ok {
			continue
		}
		r.adapters[index].Roots = mergeStringSets(r.adapters[index].Roots, roots)
	}
}

func (r *codingAgentRegistry) roots() map[string][]string {
	out := map[string][]string{}
	if r == nil {
		return out
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, adapter := range r.adapters {
		if len(adapter.Roots) == 0 {
			continue
		}
		out[adapter.ID] = append([]string(nil), adapter.Roots...)
	}
	return out
}

func (r *codingAgentRegistry) hasDiscovery(id string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	index, ok := r.byID[strings.TrimSpace(strings.ToLower(id))]
	return ok && r.adapters[index].Capabilities.Discovery != nil
}

func (r *codingAgentRegistry) hasTranscript(id string) bool {
	_, ok := r.transcriptParser(id)
	return ok
}

func (r *codingAgentRegistry) transcriptAgentIDs() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		if adapter.Capabilities.Transcript != nil {
			ids = append(ids, adapter.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (r *codingAgentRegistry) transcriptParser(id string) (agentTranscriptParser, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	index, ok := r.byID[strings.TrimSpace(strings.ToLower(id))]
	if !ok || r.adapters[index].Capabilities.Transcript == nil {
		return nil, false
	}
	return r.adapters[index].Capabilities.Transcript, true
}

func (r *codingAgentRegistry) usageDecoder(id string) (agentOutputUsageDecoder, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	index, ok := r.byID[strings.TrimSpace(strings.ToLower(id))]
	if !ok || r.adapters[index].Capabilities.Usage == nil {
		return nil, false
	}
	return r.adapters[index].Capabilities.Usage, true
}

func (r *codingAgentRegistry) hasUsageRoots() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, adapter := range r.adapters {
		if adapter.Capabilities.Usage != nil && len(adapter.Roots) > 0 {
			return true
		}
	}
	return false
}

func (r *codingAgentRegistry) transcriptFileForEvidencePath(path string) (TranscriptFile, bool) {
	if r == nil {
		return TranscriptFile{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, adapter := range r.adapters {
		if adapter.Capabilities.Discovery == nil || adapter.Capabilities.Transcript == nil {
			continue
		}
		if file, ok := adapter.Capabilities.Discovery.Classify(adapter.ID, adapter.Roots, path); ok {
			return file, true
		}
	}
	return TranscriptFile{}, false
}

func (r *codingAgentRegistry) detectProcess(command string) (string, string) {
	if r == nil {
		return "", ""
	}
	view := newProcessCommand(command)
	if view.Raw == "" || view.Excluded {
		return "", ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, adapter := range r.adapters {
		process := adapter.Capabilities.Process
		if process == nil || !process.MatchesCommand(view) {
			continue
		}
		return adapter.ID, process.DisplayIdentity(view)
	}
	return "", ""
}

func (r *codingAgentRegistry) transcriptFileForPath(path string) (TranscriptFile, bool) {
	if r == nil {
		return TranscriptFile{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, adapter := range r.adapters {
		process := adapter.Capabilities.Process
		if process == nil {
			continue
		}
		if file, ok := process.TranscriptFileForPath(path); ok {
			return file, true
		}
	}
	return TranscriptFile{}, false
}

func (r *codingAgentRegistry) rootsFromCommand(agentID, command string) []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	index, ok := r.byID[strings.TrimSpace(strings.ToLower(agentID))]
	if !ok || r.adapters[index].Capabilities.Process == nil {
		return nil
	}
	return r.adapters[index].Capabilities.Process.RootsFromCommand(newProcessCommand(command))
}

func (r *codingAgentRegistry) rootFromTranscriptFile(file TranscriptFile) string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	index, ok := r.byID[strings.TrimSpace(strings.ToLower(file.Tool))]
	if !ok || r.adapters[index].Capabilities.Process == nil {
		return ""
	}
	return r.adapters[index].Capabilities.Process.RootFromTranscriptPath(file.Path)
}

func (r *codingAgentRegistry) discoverTranscripts(ctx context.Context, cutoff time.Time) transcriptDiscoveryResult {
	result := transcriptDiscoveryResult{}
	if r == nil {
		return result
	}
	r.mu.RLock()
	adapters := append([]codingAgentAdapter(nil), r.adapters...)
	for index := range adapters {
		adapters[index].Roots = append([]string(nil), adapters[index].Roots...)
	}
	r.mu.RUnlock()
	for _, adapter := range adapters {
		if adapter.Capabilities.Discovery == nil {
			continue
		}
		discovered := adapter.Capabilities.Discovery.Discover(ctx, adapter.ID, uniquePhysicalEvidenceRoots(adapter.Roots), cutoff)
		result.Files = append(result.Files, discovered.Files...)
		result.Errors = append(result.Errors, discovered.Errors...)
		result.VisitedEntries += discovered.VisitedEntries
		result.PrunedDirectories += discovered.PrunedDirectories
	}
	return result
}

func uniquePhysicalEvidenceRoots(roots []string) []string {
	seen := make(map[string]struct{}, len(roots))
	unique := make([]string, 0, len(roots))
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		physical := canonicalEvidencePath(root)
		if root == "" || root == "." || physical == "" || physical == "." {
			continue
		}
		if _, exists := seen[physical]; exists {
			continue
		}
		seen[physical] = struct{}{}
		unique = append(unique, root)
	}
	return unique
}

func registryRootsCacheKey(roots map[string][]string) string {
	ids := make([]string, 0, len(roots))
	for id := range roots {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, id+":"+strings.Join(roots[id], "|"))
	}
	return strings.Join(parts, "\n")
}
