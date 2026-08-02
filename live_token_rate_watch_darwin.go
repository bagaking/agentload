//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsevents"
)

type liveTokenRateWatchUpdate struct {
	paths []string
	done  chan bool
}

type darwinLiveTokenRateWatcher struct {
	events   chan liveTokenRateWatchBatch
	updates  chan liveTokenRateWatchUpdate
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func newLiveTokenRateWatcher(roots []liveTokenRateRoot) liveTokenRateWatcher {
	paths, _ := resolvableLiveTokenRateWatchPaths(liveTokenRateWatchPaths(roots))
	stream, err := startLiveTokenRateEventStream(paths)
	if err != nil {
		return nil
	}
	watcher := &darwinLiveTokenRateWatcher{
		events:  make(chan liveTokenRateWatchBatch, 32),
		updates: make(chan liveTokenRateWatchUpdate),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go watcher.run(stream, paths)
	return watcher
}

func (watcher *darwinLiveTokenRateWatcher) Events() <-chan liveTokenRateWatchBatch {
	return watcher.events
}

func (watcher *darwinLiveTokenRateWatcher) Update(paths []string) bool {
	if watcher == nil {
		return false
	}
	done := make(chan bool, 1)
	resolved, complete := resolvableLiveTokenRateWatchPaths(paths)
	select {
	case watcher.updates <- liveTokenRateWatchUpdate{
		paths: resolved,
		done:  done,
	}:
	case <-watcher.done:
		return false
	}
	select {
	case updated := <-done:
		return updated && complete
	case <-watcher.done:
		return false
	}
}

func (watcher *darwinLiveTokenRateWatcher) Stop() {
	if watcher == nil {
		return
	}
	watcher.stopOnce.Do(func() { close(watcher.stop) })
	<-watcher.done
}

func (watcher *darwinLiveTokenRateWatcher) run(stream *fsevents.EventStream, paths []string) {
	defer close(watcher.done)
	defer close(watcher.events)
	defer func() { stream.Stop() }()
	for {
		select {
		case events := <-stream.Events:
			batch := projectLiveTokenRateWatchEvents(events)
			if len(batch.Paths) == 0 && batch.Complete {
				continue
			}
			select {
			case watcher.events <- batch:
			case <-watcher.stop:
				return
			}
		case update := <-watcher.updates:
			if slices.Equal(paths, update.paths) {
				update.done <- true
				continue
			}
			next, err := startLiveTokenRateEventStream(update.paths)
			if err != nil {
				update.done <- false
				continue
			}
			stream.Stop()
			stream = next
			paths = update.paths
			update.done <- true
		case <-watcher.stop:
			return
		}
	}
}

func startLiveTokenRateEventStream(paths []string) (*fsevents.EventStream, error) {
	if len(paths) == 0 {
		return nil, os.ErrNotExist
	}
	stream := &fsevents.EventStream{
		Paths:   paths,
		Flags:   fsevents.FileEvents | fsevents.WatchRoot | fsevents.NoDefer,
		Latency: time.Second,
	}
	if err := stream.Start(); err != nil {
		return nil, err
	}
	return stream, nil
}

func resolvableLiveTokenRateWatchPaths(paths []string) ([]string, bool) {
	seen := map[string]struct{}{}
	existing := make([]string, 0, len(paths))
	complete := true
	for _, path := range paths {
		info, err := os.Stat(path)
		watchPath := path
		if err != nil || !info.IsDir() {
			watchPath = filepath.Dir(path)
			info, err = os.Stat(watchPath)
		}
		if err != nil || !info.IsDir() {
			complete = false
			continue
		}
		if _, ok := seen[watchPath]; !ok {
			seen[watchPath] = struct{}{}
			existing = append(existing, watchPath)
		}
	}
	sort.Strings(existing)
	return existing, complete
}

func projectLiveTokenRateWatchEvents(events []fsevents.Event) liveTokenRateWatchBatch {
	batch := liveTokenRateWatchBatch{Complete: true}
	seen := map[string]struct{}{}
	for _, event := range events {
		if event.Flags&(fsevents.MustScanSubDirs|fsevents.KernelDropped|fsevents.UserDropped|fsevents.EventIDsWrapped|fsevents.RootChanged) != 0 {
			batch.Complete = false
		}
		if event.Flags&fsevents.HistoryDone != 0 || !strings.HasSuffix(strings.ToLower(event.Path), ".jsonl") {
			continue
		}
		path := event.Path
		if !filepath.IsAbs(path) {
			path = string(filepath.Separator) + path
		}
		path = canonicalLiveTokenRatePath(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		batch.Paths = append(batch.Paths, path)
	}
	sort.Strings(batch.Paths)
	return batch
}
