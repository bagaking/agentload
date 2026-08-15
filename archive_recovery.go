package main

import (
	"agentload/internal/historyfile"
	"agentload/internal/trajectory"
	"context"
	"fmt"
	"time"
)

const archiveAuditInterval = 15 * time.Minute
const archiveCatalogInterval = 5 * time.Second

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
	discover := a.archiveSources
	if a.archiveSourcesFunc != nil {
		discover = a.archiveSourcesFunc
	}
	maintain := a.trajectory.PrepareCatalog
	if a.archiveCatalogFunc != nil {
		maintain = a.archiveCatalogFunc
	}
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
	enabled := a.trajectoryAccess.isEnabled()
	var catalog trajectory.SourceSet
	cleanupPending := false
	lastCatalogAt := time.Time{}
	hintTimer := time.NewTimer(time.Hour)
	hintTimer.Stop()
	defer hintTimer.Stop()
	var hintReady <-chan time.Time
	noteHint := func() {
		if hintReady == nil {
			delay := time.Until(lastCatalogAt.Add(archiveCatalogInterval))
			if delay < 0 {
				delay = 0
			}
			hintTimer.Reset(delay)
			hintReady = hintTimer.C
		}
	}
	prepareCatalog := func() bool {
		more, err := maintain(ctx, catalog, a.trajectoryAccess.isEnabled)
		if err != nil {
			if ctx.Err() == nil && a.logger != nil {
				a.logger.Printf("archive catalog maintenance deferred: %v", err)
			}
			// A failed operation is not evidence of actionable progress. Retain
			// checkpoints and wait for a new hint or the periodic audit.
			return false
		}
		return more
	}
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
			lastCatalogAt = time.Now()
			set := discover(ctx)
			membershipChanged := set.CatalogComplete != catalog.CatalogComplete || set.Coverage.Complete != catalog.Coverage.Complete
			catalog = set
			hintReady = nil
			hintTimer.Stop()
			currentEnabled := a.trajectoryAccess.isEnabled()
			if currentEnabled != enabled {
				force = true
				enabled = currentEnabled
			}
			nextSeen := make(map[string]string, len(set.Sources))
			for _, src := range set.Sources {
				key := src.Agent + "\x00" + src.Path
				signature := archiveSourceSignature(src)
				if _, known := seen[key]; !known {
					membershipChanged = true
				}
				if !queued[key] && (force || seen[key] != signature) {
					queue = append(queue, src)
					queued[key] = true
				}
				nextSeen[key] = signature
			}
			membershipChanged = membershipChanged || len(nextSeen) != len(seen)
			seen = nextSeen
			if enabled && (force || membershipChanged) {
				cleanupPending = prepareCatalog()
			}
			dirty, force = false, false
		}
		if len(queue) == 0 {
			if cleanupPending {
				// Only structural migration retries immediately. Source audits stay
				// in their own queue; retain this catalog until new evidence arrives.
				cleanupPending = prepareCatalog()
				// Yield between bounded structural migration batches.
				timer := time.NewTimer(25 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				case <-a.archiveWake:
					noteHint()
				case <-hintReady:
					dirty, hintReady = true, nil
				case <-audit.C:
					a.observer.evidenceIndex.requestReconcile()
					dirty, force = true, true
				case <-publish.C:
					publishUsage()
				}
				timer.Stop()
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
				noteHint()
			case <-hintReady:
				dirty, hintReady = true, nil
			case <-publish.C:
				publishUsage()
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
			noteHint()
		default:
		}
		// Coalesce frequent writer hints: rebuilding the catalog for each line would
		// consume more CPU than parsing it. Audit is unconditional and infrequent.
		select {
		case <-publish.C:
			publishUsage()
		case <-hintReady:
			dirty, hintReady = true, nil
		case <-audit.C:
			a.observer.evidenceIndex.requestReconcile()
			dirty, force = true, true
		default:
		}
	}
}
