//go:build !darwin

package main

func selfProcessCounters() (cpuNS, rss uint64, ok bool) { return 0, 0, false }

func processIOCounters(pid int) (readBytes, writeBytes uint64, ok bool) {
	return 0, 0, false
}
