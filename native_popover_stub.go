//go:build !darwin || !cgo

package main

func nativePopoverConfigureDashboard(string)         {}
func nativePopoverShow(string)                       {}
func nativePopoverInstallStatusClickFallback(string) {}
func nativePopoverHide()                             {}
func nativePopoverSupported() bool                   { return false }
