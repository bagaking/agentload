package main

import (
	"agentload/internal/historyfile"
	"agentload/internal/trajectory"
	"context"
	"fmt"
	"time"
)

const archiveAuditInterval = 15 * time.Minute

func (a *trayApp) notifyArchive() {
	if a.trajectory != nil {
		a.trajectory.NotifyEvidence()
	}
	if a.archiveWake != nil {
		select {
		case a.archiveWake <- struct{}{}:
		default:
		}
	}
}
func (a *trayApp) startArchiveRecovery(ctx context.Context) {
	if a.trajectory == nil || a.trajectoryAccess == nil || a.throughputHistory == nil {
		return
	}
	a.archiveDone = make(chan struct{})
	go func() {
		defer close(a.archiveDone)
		defer recoverBackgroundPanic("archive recovery")
		a.recoverArchive(ctx, archiveAuditInterval)
	}()
}
func archiveSourceSignature(src trajectory.Source) string {
	if src.Info == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d:%d", src.Info.Size(), src.Info.ModTime().UnixNano())
}

// The registry owns discovery. Recovery yields between source chunks and sleeps
// when caught up. New hints join a fair queue during a long initial backfill;
// periodic audit repairs omissions even when no watcher hint was delivered.
func (a *trayApp) recoverArchive(ctx context.Context, auditInterval time.Duration) {
	replay, err := openThroughputRecovery(a.cfg.HistoryFile, a.throughputHistory, a.observer.adapters)
	if err != nil && a.logger != nil {
		a.logger.Printf("throughput recovery unavailable: %v", err)
	}
	if replay != nil {
		defer replay.db.Close()
	}
	audit := time.NewTicker(auditInterval)
	defer audit.Stop()
	refresh := time.NewTicker(5 * time.Second)
	defer refresh.Stop()
	publish := time.NewTicker(30 * time.Second)
	defer publish.Stop()
	publishUsage := func() {
		if replay != nil && replay.dirty {
			if err := replay.publish(time.Now()); err != nil && a.logger != nil {
				a.logger.Printf("throughput recovery publish failed: %v", err)
			}
		}
	}
	queue := []trajectory.Source{}
	queued := map[string]bool{}
	seen := map[string]string{}
	dirty, force := true, true
	hints := false
	enabled := a.trajectoryAccess.isEnabled()
	var catalog trajectory.SourceSet
	cleanupPending := false
	for ctx.Err() == nil {
		if historyfile.CheckStorageBudget(a.cfg.HistoryFile) != nil {
			// Keep committed checkpoints; external disk cleanup needs no file hint.
			select {
			case <-ctx.Done():
				return
			case <-refresh.C:
				continue
			}
		}
		if dirty {
			set := a.archiveSources(ctx)
			catalog = set
			currentEnabled := a.trajectoryAccess.isEnabled()
			if currentEnabled != enabled {
				force = true
				enabled = currentEnabled
			}
			for _, src := range set.Sources {
				key := src.Agent + "\x00" + src.Path
				signature := archiveSourceSignature(src)
				if !queued[key] && (force || seen[key] != signature) {
					queue = append(queue, src)
					queued[key] = true
				}
				seen[key] = signature
			}
			dirty, force = false, false
			if enabled {
				cleanupPending, _ = a.trajectory.PrepareCatalog(ctx, catalog, a.trajectoryAccess.isEnabled)
			}
		}
		if len(queue) == 0 && hints {
			dirty, hints = true, false
			continue
		}
		if len(queue) == 0 {
			if cleanupPending {
				catalog = a.archiveSources(ctx)
				cleanupPending, _ = a.trajectory.PrepareCatalog(ctx, catalog, a.trajectoryAccess.isEnabled)
				// Yield on bounded cleanup and retry failures without a busy loop.
				timer := time.NewTimer(25 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				continue
			}
			publishUsage()
			select {
			case <-ctx.Done():
				return
			case <-audit.C:
				a.observer.evidenceIndex.requestReconcile()
				dirty, force = true, true
			case <-a.archiveWake:
				dirty = true
			}
			continue
		}
		src := queue[0]
		queue = queue[1:]
		key := src.Agent + "\x00" + src.Path
		delete(queued, key)
		pending := false
		if replay != nil {
			more, err := replay.prepareSource(ctx, src, time.Now())
			pending = err == nil && more
		}
		if a.trajectoryAccess.isEnabled() && src.Decoder != nil {
			more, err := a.trajectory.PrepareSource(ctx, src, a.trajectoryAccess.isEnabled)
			pending = pending || (err == nil && more)
		}
		if pending {
			queue = append(queue, src)
			queued[key] = true
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		select {
		case <-a.archiveWake:
			hints = true
		default:
		}
		// Coalesce frequent writer hints: rebuilding the catalog for each line would
		// consume more CPU than parsing it. Audit is unconditional and infrequent.
		select {
		case <-publish.C:
			publishUsage()
		case <-refresh.C:
			if hints {
				dirty, hints = true, false
			}
		case <-audit.C:
			a.observer.evidenceIndex.requestReconcile()
			dirty, force = true, true
		default:
		}
	}
}
