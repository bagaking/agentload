package main

import (
	"path/filepath"
	"sort"
	"strings"
)

type evidenceWatchBatch struct {
	Paths    []string
	Complete bool
}

type evidenceWatcher interface {
	Events() <-chan evidenceWatchBatch
	Update([]string) bool
	Stop()
}

func canonicalEvidenceWatchPaths(roots map[string][]string) []string {
	seen := map[string]struct{}{}
	paths := make([]string, 0)
	for _, agentRoots := range roots {
		for _, root := range agentRoots {
			path := canonicalEvidencePath(root)
			if path == "" || path == "." {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, filepath.Clean(path))
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) == len(paths[j]) {
			return paths[i] < paths[j]
		}
		return len(paths[i]) < len(paths[j])
	})
	owned := paths[:0]
	for _, path := range paths {
		nested := false
		for _, parent := range owned {
			relative, err := filepath.Rel(parent, path)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				nested = true
				break
			}
		}
		if !nested {
			owned = append(owned, path)
		}
	}
	sort.Strings(owned)
	return owned
}
