//go:build !darwin

package main

func newLiveTokenRateWatcher([]liveTokenRateRoot) liveTokenRateWatcher {
	return nil
}
