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
}{previous: map[int]processIOCounter{}}

func sampleProcessIO(pid int, now time.Time) processIOSample {
	readBytes, writeBytes, ok := processIOCounters(pid)
	if !ok {
		return processIOSample{}
	}
	processIOSampler.Lock()
	defer processIOSampler.Unlock()

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
