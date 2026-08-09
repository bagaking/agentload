package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type agentProcessIdentity interface {
	MatchesCommand(command string) bool
	TranscriptFileForPath(path string) (TranscriptFile, bool)
	RootsFromCommand(command string) []string
}

type agentTranscriptParser interface {
	Parse(file TranscriptFile) (*SessionTrace, error)
	ParseTail(file TranscriptFile) (*SessionTrace, error)
	ParseAppend(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error)
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
				Discovery: claudeTranscriptDiscovery{},
			},
		},
		codingAgentAdapter{
			ID:    "codex",
			Roots: cfg.CodexRoots,
			Capabilities: agentCapabilities{
				Discovery: codexTranscriptDiscovery{},
			},
		},
		codingAgentAdapter{
			ID:    "trae",
			Roots: cfg.TraeRoots,
			Capabilities: agentCapabilities{
				Discovery: traeTranscriptDiscovery{},
			},
		},
	)
}

func (r *codingAgentRegistry) mergeRoots(extra map[string][]string) {
	if r == nil || len(extra) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, roots := range extra {
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
		discovered := adapter.Capabilities.Discovery.Discover(ctx, adapter.ID, adapter.Roots, cutoff)
		result.Files = append(result.Files, discovered.Files...)
		result.Errors = append(result.Errors, discovered.Errors...)
		result.VisitedEntries += discovered.VisitedEntries
		result.PrunedDirectories += discovered.PrunedDirectories
	}
	return result
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
