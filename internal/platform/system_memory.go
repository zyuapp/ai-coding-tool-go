package platform

import (
	"math"
	"regexp"
	"strconv"
)

var (
	vmStatPageSize = regexp.MustCompile(`page size of (\d+) bytes`)
	vmStatPages    = map[string]*regexp.Regexp{}
)

func init() {
	for _, name := range []string{"free", "inactive", "speculative"} {
		vmStatPages[name] = regexp.MustCompile(`(?m)^Pages ` + name + `:\s+(\d+)\.$`)
	}
}

// AvailableMacMemory reads `vm_stat`: free, inactive and speculative pages, which the system hands out
// without swapping. +Inf when the output is not what it should be.
func AvailableMacMemory(output string) float64 {
	page := vmStatPageSize.FindStringSubmatch(output)
	if page == nil {
		return math.Inf(1)
	}
	var pages float64
	for _, name := range []string{"free", "inactive", "speculative"} {
		match := vmStatPages[name].FindStringSubmatch(output)
		if match == nil {
			return math.Inf(1)
		}
		count, _ := strconv.ParseFloat(match[1], 64)
		pages += count
	}
	size, _ := strconv.ParseFloat(page[1], 64)
	return pages * size
}

var memAvailable = regexp.MustCompile(`(?m)^MemAvailable:\s+(\d+) kB$`)
var memTotal = regexp.MustCompile(`(?m)^MemTotal:\s+(\d+) kB$`)

// availableLinuxMemory reads /proc/meminfo's MemAvailable. +Inf when it has none.
func availableLinuxMemory(info string) float64 {
	match := memAvailable.FindStringSubmatch(info)
	if match == nil {
		return math.Inf(1)
	}
	kilobytes, _ := strconv.ParseFloat(match[1], 64)
	return kilobytes * 1024
}
