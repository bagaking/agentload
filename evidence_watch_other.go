//go:build !darwin

package main

func newEvidenceWatcher([]string) evidenceWatcher {
	return nil
}
