//go:build darwin

package main

/*
#include <libproc.h>
#include <sys/resource.h>
*/
import "C"
import "unsafe"

func processIOCounters(pid int) (readBytes, writeBytes uint64, ok bool) {
	var info C.struct_rusage_info_v2
	result := C.proc_pid_rusage(C.int(pid), C.RUSAGE_INFO_V2, (*C.rusage_info_t)(unsafe.Pointer(&info)))
	if result != 0 {
		return 0, 0, false
	}
	return uint64(info.ri_diskio_bytesread), uint64(info.ri_diskio_byteswritten), true
}
