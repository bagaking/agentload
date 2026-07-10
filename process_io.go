package main

import (
	"sync"
	"time"
)

type processIOSample struct {
	ReadBytes        uint64
	WriteBytes       uint64
	ReadBytesPerSec  float64
	WriteBytesPerSec float64
}

type processIOCounter struct {
	ReadBytes  uint64
	WriteBytes uint64
	At         time.Time
}

var processIOSampler = struct {
	sync.Mutex
	previous map[int]processIOCounter
	batchAt  time.Time
}{previous: map[int]processIOCounter{}}

func sampleProcessIO(pid int, now time.Time) processIOSample {
	readBytes, writeBytes, ok := processIOCounters(pid)
	if !ok {
		return processIOSample{}
	}
	processIOSampler.Lock()
	defer processIOSampler.Unlock()
	rotateProcessIOBatchLocked(now)

	out := processIOSample{ReadBytes: readBytes, WriteBytes: writeBytes}
	if previous, exists := processIOSampler.previous[pid]; exists && now.After(previous.At) {
		seconds := now.Sub(previous.At).Seconds()
		if seconds > 0 && readBytes >= previous.ReadBytes && writeBytes >= previous.WriteBytes {
			out.ReadBytesPerSec = float64(readBytes-previous.ReadBytes) / seconds
			out.WriteBytesPerSec = float64(writeBytes-previous.WriteBytes) / seconds
		}
	}
	processIOSampler.previous[pid] = processIOCounter{ReadBytes: readBytes, WriteBytes: writeBytes, At: now}
	return out
}

// rotateProcessIOBatchLocked evicts counters for PIDs that the just-finished
// discovery batch did not sample. Every call in one batch shares the same now,
// so a newer now marks the batch boundary; entries older than the previous
// batch time belong to exited processes. Caller must hold the sampler lock.
func rotateProcessIOBatchLocked(now time.Time) {
	if !now.After(processIOSampler.batchAt) {
		return
	}
	for pid, previous := range processIOSampler.previous {
		if previous.At.Before(processIOSampler.batchAt) {
			delete(processIOSampler.previous, pid)
		}
	}
	processIOSampler.batchAt = now
}
