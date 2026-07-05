//go:build !darwin

package main

func processIOCounters(pid int) (readBytes, writeBytes uint64, ok bool) {
	return 0, 0, false
}
