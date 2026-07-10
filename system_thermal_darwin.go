//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -fmodules
#cgo LDFLAGS: -framework Foundation
#import <Foundation/Foundation.h>

static int agentload_thermal_state(void) {
	if (@available(macOS 10.10.3, *)) {
		return (int)[NSProcessInfo processInfo].thermalState;
	}
	return -1;
}
*/
import "C"

func readSystemThermalState() (string, bool) {
	return normalizeThermalState(int(C.agentload_thermal_state()))
}
