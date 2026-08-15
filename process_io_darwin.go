//go:build darwin

package main

/*
#include <libproc.h>
#include <sys/resource.h>
*/
import "C"
import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

func selfProcessCounters() (cpuNS, rss uint64, ok bool) {
	var info C.struct_rusage_info_v2
	if C.proc_pid_rusage(C.int(os.Getpid()), C.RUSAGE_INFO_V2, (*C.rusage_info_t)(unsafe.Pointer(&info))) != 0 {
		return 0, 0, false
	}
	var usage unix.Rusage
	if unix.Getrusage(unix.RUSAGE_SELF, &usage) != nil {
		return 0, 0, false
	}
	return uint64(usage.Utime.Nano() + usage.Stime.Nano()), uint64(info.ri_resident_size), true
}

func processIOCounters(pid int) (readBytes, writeBytes uint64, ok bool) {
	var info C.struct_rusage_info_v2
	result := C.proc_pid_rusage(C.int(pid), C.RUSAGE_INFO_V2, (*C.rusage_info_t)(unsafe.Pointer(&info)))
	if result != 0 {
		return 0, 0, false
	}
	return uint64(info.ri_diskio_bytesread), uint64(info.ri_diskio_byteswritten), true
}
