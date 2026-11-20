package platform

import (
	"context"
	"math"
	"time"

	"golang.org/x/sys/unix"
)

// AvailableMemory is the memory that can be had without swapping, reclaimable caches included, since
// free RAM alone would stop healthy workloads. +Inf when it cannot be read: a failed pressure reading
// must not become a false emergency.
func AvailableMemory(ctx context.Context) float64 {
	stdout, _, err := Run(ctx, "/usr/bin/vm_stat", nil, RunOptions{Timeout: 2 * time.Second, Env: []string{"LC_ALL=C"}})
	if err != nil {
		return math.Inf(1)
	}
	return AvailableMacMemory(string(stdout))
}

// TotalMemory is the computer's installed memory, as `os.totalmem()` reports it.
func TotalMemory() float64 {
	size, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return float64(size)
}
