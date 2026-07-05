package main

import (
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

func sampleSystemResources() SystemResourceSnapshot {
	now := time.Now()
	counters, supported, notes := readSystemResourceCounters()
	snapshot := SystemResourceSnapshot{
		SampledAt:             now.Format(time.RFC3339Nano),
		Supported:             supported,
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
	if !supported {
		return snapshot
	}

	systemResourceSampler.Lock()
	defer systemResourceSampler.Unlock()
	previous := systemResourceSampler.previous
	previousAt := systemResourceSampler.previousAt
	systemResourceSampler.previous = counters
	systemResourceSampler.previousAt = now
	if previousAt.IsZero() {
		snapshot.Notes = append(snapshot.Notes, "System resource rates need two samples; the next poll will contain CPU and network deltas.")
		return snapshot
	}
	interval := now.Sub(previousAt).Seconds()
	if interval <= 0 {
		return snapshot
	}
	snapshot.SampleIntervalSeconds = interval
	snapshot.CPUPercent = cpuPercentDelta(previous, counters)
	rxBytesDelta := counterDelta(previous.NetworkRxBytes, counters.NetworkRxBytes)
	txBytesDelta := counterDelta(previous.NetworkTxBytes, counters.NetworkTxBytes)
	rxPacketDelta := counterDelta(previous.NetworkRxPackets, counters.NetworkRxPackets)
	txPacketDelta := counterDelta(previous.NetworkTxPackets, counters.NetworkTxPackets)
	errorDelta := counterDelta(previous.NetworkRxErrors, counters.NetworkRxErrors) + counterDelta(previous.NetworkTxErrors, counters.NetworkTxErrors)
	dropDelta := counterDelta(previous.NetworkRxDrops, counters.NetworkRxDrops)
	snapshot.NetworkRxBytesPerSec = float64(rxBytesDelta) / interval
	snapshot.NetworkTxBytesPerSec = float64(txBytesDelta) / interval
	snapshot.NetworkRxPacketsPerSec = float64(rxPacketDelta) / interval
	snapshot.NetworkTxPacketsPerSec = float64(txPacketDelta) / interval
	snapshot.NetworkErrorPacketsPerSec = float64(errorDelta) / interval
	snapshot.NetworkDroppedPacketsPerSec = float64(dropDelta) / interval
	snapshot.NetworkPacketIssuePct = pctFromUint(errorDelta+dropDelta, rxPacketDelta+txPacketDelta+errorDelta+dropDelta)
	return snapshot
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
