//go:build darwin && cgo

package main

// native_popover_darwin.go bridges Go to the Cocoa NSPanel + WKWebView
// implementation in native_popover_darwin.m. The Go surface stays small:
// show, hide, dashboard hand-off, and the status-item click fallback.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -fmodules
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
#include "native_popover_darwin.h"
*/
import "C"

import "unsafe"

// nativePopoverConfigureDashboard registers the loopback dashboard URL the
// popover can hand to NSWorkspace when the embedded HTML triggers the explicit
// open_dashboard action. Pass an empty string to disable the action.
func nativePopoverConfigureDashboard(url string) {
	cs := C.CString(url)
	defer C.free(unsafe.Pointer(cs))
	C.agentLoadPopoverConfigureDashboard(cs)
}

// nativePopoverShow opens / toggles the popover, loading the supplied URL.
func nativePopoverShow(url string) {
	cs := C.CString(url)
	defer C.free(unsafe.Pointer(cs))
	C.agentLoadPopoverShow(cs)
}

// nativePopoverInstallStatusClickFallback catches status-item clicks that do
// not reach fyne/systray on newer macOS Control Center replica scenes.
func nativePopoverInstallStatusClickFallback(url string) {
	cs := C.CString(url)
	defer C.free(unsafe.Pointer(cs))
	C.agentLoadPopoverInstallStatusClickFallback(cs)
}

// nativePopoverHide closes the popover. Safe even if it is not visible.
func nativePopoverHide() {
	C.agentLoadPopoverHide()
}

// nativePopoverSupported reports whether the host has WKWebView available.
func nativePopoverSupported() bool {
	return C.agentLoadPopoverIsSupported() != 0
}
