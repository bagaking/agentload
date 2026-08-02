package main

import (
	"path/filepath"
	"sort"
)

type liveTokenRateWatchBatch struct {
	Paths    []string
	Complete bool
}

type liveTokenRateWatcher interface {
	Events() <-chan liveTokenRateWatchBatch
	Update([]string) bool
	Stop()
}

func liveTokenRateWatchPaths(roots []liveTokenRateRoot) []string {
	seen := map[string]struct{}{}
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		path := canonicalLiveTokenRatePath(root.Path)
		if path == "" || path == "." {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, filepath.Clean(path))
	}
	sort.Strings(paths)
	return paths
}
