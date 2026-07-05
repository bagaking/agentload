//go:build !darwin

package main

func readSystemResourceCounters() (systemResourceCounters, bool, []string) {
	return systemResourceCounters{}, false, []string{"System resource counters are implemented for macOS builds."}
}
