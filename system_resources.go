package main

import (
	"agentload/internal/snapshot"
	"sync"
	"time"
)

type systemResourceCounters struct {
	CPUUser           uint64
	CPUSystem         uint64
	CPUIdle           uint64
	CPUNice           uint64
	LoadAverage1      float64
	LoadAverage5      float64
	LoadAverage15     float64
	UptimeSeconds     int64
	MemoryTotalBytes  uint64
	MemoryUsedBytes   uint64
	MemoryFreeBytes   uint64
	DiskTotalBytes    uint64
	DiskUsedBytes     uint64
	DiskFreeBytes     uint64
	NetworkRxBytes    uint64
	NetworkTxBytes    uint64
	NetworkRxPackets  uint64
	NetworkTxPackets  uint64
	NetworkRxErrors   uint64
	NetworkTxErrors   uint64
	NetworkRxDrops    uint64
	NetworkInterfaces int
}

var systemResourceSampler = struct {
	sync.Mutex
	previous   systemResourceCounters
	previousAt time.Time
}{}

const systemResourceSampleInterval = 2 * time.Second

const systemResourceSampleStaleAfter = 3 * systemResourceSampleInterval

// sampleSystemResourcesNowFunc is the sampler's seam onto the platform read so
// tests can inject a failing read; production always uses the real one.
var sampleSystemResourcesNowFunc = sampleSystemResourcesNow

// backgroundSystemResourceSampler owns the delta baseline at a fixed cadence so
// CPU%/network rates do not depend on whichever client polled last. It is never
// started implicitly; trayApp startup starts it explicitly.
var backgroundSystemResourceSampler = struct {
	sync.Mutex
	running    bool
	have       bool
	generation uint64
	latest     snapshot.SystemResourceSnapshot
	latestAt   time.Time
	staleAfter time.Duration
	stop       chan struct{}
	done       chan struct{}

	// transitionMu covers the full stop-and-join transition. The sampler owns
	// process-wide baselines, so generations must never overlap even briefly.
	transitionMu sync.Mutex
}{}

func startSystemResourceSampler(interval time.Duration) {
	if interval <= 0 {
		interval = systemResourceSampleInterval
	}
	s := &backgroundSystemResourceSampler
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	s.Lock()
	if s.running {
		s.Unlock()
		return
	}
	s.running = true
	s.have = false
	s.latest = snapshot.SystemResourceSnapshot{}
	s.latestAt = time.Time{}
	s.staleAfter = interval * 3
	s.generation++
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	stop := s.stop
	done := s.done
	generation := s.generation
	resetSystemResourceDeltaBaseline()
	// The read function is bound once per generation so the running goroutine
	// never depends on later reassignment of the package seam.
	sample := sampleSystemResourcesNowFunc
	if sample == nil {
		sample = sampleSystemResourcesNow
	}
	go func() {
		// Each sample runs as its own step: a panic in one read drops that
		// sample instead of killing the sampler, and the deferred release
		// clears `running` so a later start can re-arm the loop.
		defer recoverBackgroundPanic("system resource sampler")
		defer releaseSystemResourceSamplerGeneration(generation, done)
		runSystemResourceSample(generation, sample)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				runSystemResourceSample(generation, sample)
			case <-stop:
				return
			}
		}
	}()
	s.Unlock()
}

// releaseSystemResourceSamplerGeneration clears the running flag when the
// goroutine that owns `generation` exits for any reason. Without it a panic that
// escaped the loop would strand running=true, so startSystemResourceSampler
// would no-op forever while latestBackgroundSystemResourceSample kept serving a
// frozen sample.
func releaseSystemResourceSamplerGeneration(generation uint64, done chan struct{}) {
	s := &backgroundSystemResourceSampler
	defer close(done)
	s.Lock()
	if s.running && s.generation == generation && s.done == done {
		s.running = false
		s.have = false
		s.latest = snapshot.SystemResourceSnapshot{}
		s.latestAt = time.Time{}
		s.staleAfter = 0
		s.stop = nil
		s.done = nil
	}
	s.Unlock()
	// Do not reset the process-wide baseline here. A fresh generation may start
	// immediately after this goroutine clears its state; the start/stop owners
	// perform the reset while holding the transition lock, so an old generation
	// cannot wipe the new generation's baseline.
}

func runSystemResourceSample(generation uint64, sample func() snapshot.SystemResourceSnapshot) {
	if sample == nil {
		return
	}
	succeeded := false
	runBackgroundStep("system resource sampler", func() {
		storeBackgroundSystemResourceSample(generation, sample())
		succeeded = true
	})
	if !succeeded {
		invalidateBackgroundSystemResourceSample(generation)
	}
}

func invalidateBackgroundSystemResourceSample(generation uint64) {
	s := &backgroundSystemResourceSampler
	owned := false
	s.Lock()
	if s.running && s.generation == generation {
		owned = true
		s.have = false
		s.latest = snapshot.SystemResourceSnapshot{}
		s.latestAt = time.Time{}
	}
	s.Unlock()
	if owned {
		resetSystemResourceDeltaBaseline()
	}
}

func stopSystemResourceSampler() {
	s := &backgroundSystemResourceSampler
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	s.Lock()
	if !s.running {
		s.have = false
		s.latest = snapshot.SystemResourceSnapshot{}
		s.latestAt = time.Time{}
		s.staleAfter = 0
		s.Unlock()
		resetSystemResourceDeltaBaseline()
		return
	}
	stop := s.stop
	done := s.done
	s.running = false
	s.have = false
	s.latest = snapshot.SystemResourceSnapshot{}
	s.latestAt = time.Time{}
	s.staleAfter = 0
	s.stop = nil
	s.done = nil
	if stop != nil {
		close(stop)
	}
	s.Unlock()
	resetSystemResourceDeltaBaseline()
	if done != nil {
		<-done
	}
	// A read already in progress may have reached the baseline after the first
	// reset; clear it again after the owner has joined.
	resetSystemResourceDeltaBaseline()
}

func storeBackgroundSystemResourceSample(generation uint64, snap snapshot.SystemResourceSnapshot) {
	s := &backgroundSystemResourceSampler
	s.Lock()
	defer s.Unlock()
	if !s.running || s.generation != generation {
		return
	}
	s.latest = snap
	s.latestAt = time.Now()
	s.have = true
}

func latestBackgroundSystemResourceSample() (snapshot.SystemResourceSnapshot, bool) {
	s := &backgroundSystemResourceSampler
	s.Lock()
	defer s.Unlock()
	staleAfter := s.staleAfter
	if staleAfter <= 0 {
		staleAfter = systemResourceSampleStaleAfter
	}
	if !s.running || !s.have || (!s.latestAt.IsZero() && time.Since(s.latestAt) > staleAfter) {
		return snapshot.SystemResourceSnapshot{}, false
	}
	return s.latest, true
}

func sampleSystemResources() snapshot.SystemResourceSnapshot {
	if snap, ok := latestBackgroundSystemResourceSample(); ok {
		return snap
	}
	backgroundSystemResourceSampler.Lock()
	running := backgroundSystemResourceSampler.running
	backgroundSystemResourceSampler.Unlock()
	if running {
		return unavailableSystemResourceSnapshot("System resource sample is unavailable while the background sampler is waiting for a fresh read.")
	}
	return sampleSystemResourcesNow()
}

func unavailableSystemResourceSnapshot(reason string) snapshot.SystemResourceSnapshot {
	return snapshot.SystemResourceSnapshot{Notes: []string{reason}}
}

func resetSystemResourceDeltaBaseline() {
	systemResourceSampler.Lock()
	systemResourceSampler.previous = systemResourceCounters{}
	systemResourceSampler.previousAt = time.Time{}
	systemResourceSampler.Unlock()
}

func sampleSystemResourcesNow() snapshot.SystemResourceSnapshot {
	now := time.Now()
	counters, supported, notes := readSystemResourceCounters()
	thermalState, thermalStateSupported := readSystemThermalState()
	snap := snapshot.SystemResourceSnapshot{
		SampledAt:             now.Format(time.RFC3339Nano),
		Supported:             supported,
		ThermalState:          thermalState,
		ThermalStateSupported: thermalStateSupported,
		CPUPercent:            0,
		LoadAverage1:          counters.LoadAverage1,
		LoadAverage5:          counters.LoadAverage5,
		LoadAverage15:         counters.LoadAverage15,
		UptimeSeconds:         counters.UptimeSeconds,
		MemoryTotalBytes:      counters.MemoryTotalBytes,
		MemoryUsedBytes:       counters.MemoryUsedBytes,
		MemoryFreeBytes:       counters.MemoryFreeBytes,
		MemoryUsedPct:         pctFromUint(counters.MemoryUsedBytes, counters.MemoryTotalBytes),
		DiskTotalBytes:        counters.DiskTotalBytes,
		DiskUsedBytes:         counters.DiskUsedBytes,
		DiskFreeBytes:         counters.DiskFreeBytes,
		DiskUsedPct:           pctFromUint(counters.DiskUsedBytes, counters.DiskTotalBytes),
		NetworkRxBytes:        counters.NetworkRxBytes,
		NetworkTxBytes:        counters.NetworkTxBytes,
		NetworkRxPackets:      counters.NetworkRxPackets,
		NetworkTxPackets:      counters.NetworkTxPackets,
		NetworkRxErrors:       counters.NetworkRxErrors,
		NetworkTxErrors:       counters.NetworkTxErrors,
		NetworkRxDrops:        counters.NetworkRxDrops,
		NetworkInterfaceCount: counters.NetworkInterfaces,
		Notes:                 append([]string(nil), notes...),
	}
	if !thermalStateSupported {
		snap.Notes = append(snap.Notes, "Thermal pressure state is unavailable from public macOS process information.")
	}
	if !supported {
		return snap
	}

	systemResourceSampler.Lock()
	defer systemResourceSampler.Unlock()
	previous := systemResourceSampler.previous
	previousAt := systemResourceSampler.previousAt
	systemResourceSampler.previous = counters
	systemResourceSampler.previousAt = now
	if previousAt.IsZero() {
		snap.Notes = append(snap.Notes, "System resource rates need two samples; the next poll will contain CPU and network deltas.")
		return snap
	}
	interval := now.Sub(previousAt).Seconds()
	if interval <= 0 {
		return snap
	}
	snap.SampleIntervalSeconds = interval
	snap.CPUPercent = cpuPercentDelta(previous, counters)
	rxBytesDelta := counterDelta(previous.NetworkRxBytes, counters.NetworkRxBytes)
	txBytesDelta := counterDelta(previous.NetworkTxBytes, counters.NetworkTxBytes)
	rxPacketDelta := counterDelta(previous.NetworkRxPackets, counters.NetworkRxPackets)
	txPacketDelta := counterDelta(previous.NetworkTxPackets, counters.NetworkTxPackets)
	errorDelta := counterDelta(previous.NetworkRxErrors, counters.NetworkRxErrors) + counterDelta(previous.NetworkTxErrors, counters.NetworkTxErrors)
	dropDelta := counterDelta(previous.NetworkRxDrops, counters.NetworkRxDrops)
	snap.NetworkRxBytesPerSec = float64(rxBytesDelta) / interval
	snap.NetworkTxBytesPerSec = float64(txBytesDelta) / interval
	snap.NetworkRxPacketsPerSec = float64(rxPacketDelta) / interval
	snap.NetworkTxPacketsPerSec = float64(txPacketDelta) / interval
	snap.NetworkErrorPacketsPerSec = float64(errorDelta) / interval
	snap.NetworkDroppedPacketsPerSec = float64(dropDelta) / interval
	snap.NetworkPacketIssuePct = pctFromUint(errorDelta+dropDelta, rxPacketDelta+txPacketDelta+errorDelta+dropDelta)
	return snap
}

func counterDelta(previous, current uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func cpuPercentDelta(previous, current systemResourceCounters) float64 {
	prevIdle := previous.CPUIdle
	nextIdle := current.CPUIdle
	prevTotal := previous.CPUUser + previous.CPUSystem + previous.CPUIdle + previous.CPUNice
	nextTotal := current.CPUUser + current.CPUSystem + current.CPUIdle + current.CPUNice
	if nextTotal <= prevTotal || nextIdle < prevIdle {
		return 0
	}
	totalDelta := nextTotal - prevTotal
	if totalDelta == 0 {
		return 0
	}
	activeDelta := totalDelta - (nextIdle - prevIdle)
	return clampFloat((float64(activeDelta)/float64(totalDelta))*100, 0, 100)
}

func pctFromUint(value, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return clampFloat((float64(value)/float64(total))*100, 0, 100)
}

func clampFloat(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
