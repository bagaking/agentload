//go:build darwin

package main

/*
#include <ifaddrs.h>
#include <mach/host_info.h>
#include <mach/mach_host.h>
#include <net/if.h>
#include <net/if_dl.h>
#include <stdint.h>
#include <stdlib.h>
#include <sys/types.h>
#include <unistd.h>
*/
import "C"

import (
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func readSystemResourceCounters() (systemResourceCounters, bool, []string) {
	counters := systemResourceCounters{}
	notes := []string{}
	if !readCPUCounters(&counters) {
		notes = append(notes, "CPU counters are unavailable from host statistics.")
	}
	if !readMemoryCounters(&counters) {
		notes = append(notes, "Memory counters are unavailable from host statistics.")
	}
	if !readLoadAndUptimeCounters(&counters) {
		notes = append(notes, "Load average or uptime counters are unavailable from sysctl.")
	}
	if !readDiskCounters(&counters) {
		notes = append(notes, "Disk capacity counters are unavailable from statfs.")
	}
	if !readNetworkCounters(&counters) {
		notes = append(notes, "Network counters are unavailable from interface statistics.")
	}
	return counters, true, notes
}

func readCPUCounters(counters *systemResourceCounters) bool {
	var info C.host_cpu_load_info_data_t
	count := C.mach_msg_type_number_t(C.HOST_CPU_LOAD_INFO_COUNT)
	result := C.host_statistics(
		C.mach_host_self(),
		C.HOST_CPU_LOAD_INFO,
		C.host_info_t(unsafe.Pointer(&info)),
		&count,
	)
	if result != C.KERN_SUCCESS {
		return false
	}
	counters.CPUUser = uint64(info.cpu_ticks[C.CPU_STATE_USER])
	counters.CPUSystem = uint64(info.cpu_ticks[C.CPU_STATE_SYSTEM])
	counters.CPUIdle = uint64(info.cpu_ticks[C.CPU_STATE_IDLE])
	counters.CPUNice = uint64(info.cpu_ticks[C.CPU_STATE_NICE])
	return true
}

func readMemoryCounters(counters *systemResourceCounters) bool {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil || total == 0 {
		return false
	}
	var info C.vm_statistics64_data_t
	count := C.mach_msg_type_number_t(C.HOST_VM_INFO64_COUNT)
	result := C.host_statistics64(
		C.mach_host_self(),
		C.HOST_VM_INFO64,
		C.host_info64_t(unsafe.Pointer(&info)),
		&count,
	)
	if result != C.KERN_SUCCESS {
		return false
	}
	pageSize := uint64(unix.Getpagesize())
	freePages := uint64(info.free_count) + uint64(info.speculative_count)
	activePages := uint64(info.active_count) + uint64(info.wire_count) + uint64(info.compressor_page_count)
	used := activePages * pageSize
	free := freePages * pageSize
	if used > total {
		used = total
	}
	if free > total {
		free = total - used
	}
	counters.MemoryTotalBytes = total
	counters.MemoryUsedBytes = used
	counters.MemoryFreeBytes = free
	return true
}

func readLoadAndUptimeCounters(counters *systemResourceCounters) bool {
	var loads [3]C.double
	loadOK := C.getloadavg((*C.double)(unsafe.Pointer(&loads[0])), 3) == 3
	if loadOK {
		counters.LoadAverage1 = float64(loads[0])
		counters.LoadAverage5 = float64(loads[1])
		counters.LoadAverage15 = float64(loads[2])
	}
	boot, err := unix.SysctlTimeval("kern.boottime")
	if err == nil && boot != nil {
		bootAt := time.Unix(int64(boot.Sec), int64(boot.Usec)*1000)
		if seconds := int64(time.Since(bootAt).Seconds()); seconds > 0 {
			counters.UptimeSeconds = seconds
		}
	}
	return loadOK || counters.UptimeSeconds > 0
}

func readDiskCounters(counters *systemResourceCounters) bool {
	var stat unix.Statfs_t
	if err := unix.Statfs("/", &stat); err != nil {
		return false
	}
	total := uint64(stat.Blocks) * uint64(stat.Bsize)
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	if total == 0 || free > total {
		return false
	}
	counters.DiskTotalBytes = total
	counters.DiskFreeBytes = free
	counters.DiskUsedBytes = total - free
	return true
}

func readNetworkCounters(counters *systemResourceCounters) bool {
	var addrs *C.struct_ifaddrs
	if C.getifaddrs(&addrs) != 0 {
		return false
	}
	defer C.freeifaddrs(addrs)
	seen := map[string]struct{}{}
	for ifa := addrs; ifa != nil; ifa = ifa.ifa_next {
		if ifa.ifa_addr == nil || ifa.ifa_data == nil {
			continue
		}
		if ifa.ifa_addr.sa_family != C.AF_LINK {
			continue
		}
		flags := uint(ifa.ifa_flags)
		if flags&C.IFF_UP == 0 || flags&C.IFF_LOOPBACK != 0 {
			continue
		}
		name := C.GoString(ifa.ifa_name)
		if name == "" {
			continue
		}
		data := (*C.struct_if_data)(ifa.ifa_data)
		counters.NetworkRxBytes += uint64(data.ifi_ibytes)
		counters.NetworkTxBytes += uint64(data.ifi_obytes)
		seen[name] = struct{}{}
	}
	counters.NetworkInterfaces = len(seen)
	return true
}
