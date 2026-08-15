//go:build !darwin || !cgo

package main

func nativePopoverConfigureDashboard(string)            {}
func nativePopoverPrepare(string)                       {}
func nativePopoverSetPaintCallback(func(float64, bool)) {}
func nativePopoverShow(string)                          {}
func nativePopoverInstallStatusClickFallback(string)    {}
func nativePopoverHide()                                {}
func nativePopoverSupported() bool                      { return false }
func nativeStatusBoxUpdate(statusBoxPayload)            {}
func nativeStatusBoxSupported() bool                    { return false }
