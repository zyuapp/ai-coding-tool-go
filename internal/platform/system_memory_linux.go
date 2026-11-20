package platform

import (
	"context"
	"math"
	"os"
	"strconv"
)

// AvailableMemory is the memory that can be had without swapping, reclaimable caches included, since
// free RAM alone would stop healthy workloads. +Inf when it cannot be read: a failed pressure reading
// must not become a false emergency.
func AvailableMemory(context.Context) float64 {
	info, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return math.Inf(1)
	}
	return availableLinuxMemory(string(info))
}

// TotalMemory is the computer's installed memory, as `os.totalmem()` reports it.
func TotalMemory() float64 {
	info, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	match := memTotal.FindStringSubmatch(string(info))
	if match == nil {
		return 0
	}
	kilobytes, _ := strconv.ParseFloat(match[1], 64)
	return kilobytes * 1024
}
