//go:build !darwin || !cgo

package main

func readSystemThermalState() (string, bool) {
	return "", false
}
