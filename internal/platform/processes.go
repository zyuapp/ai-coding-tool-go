package platform

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ProcessSample is one process as `ps` reports it.
type ProcessSample struct {
	PID    int
	Parent int
	Bytes  float64
	// Born is when the process started, which tells it from a later one that reuses its pid.
	Born string
}

var processLine = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(\d+)\s+([A-Za-z]{3}\s+[A-Za-z]{3}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\s+\d{4})\s*$`)
var spaces = regexp.MustCompile(`\s+`)

// ParseProcessSamples reads `ps -axo pid=,ppid=,rss=,lstart=` output. `lstart` tells a live process
// from another that later reuses its pid, on macOS and Linux.
func ParseProcessSamples(output string) []ProcessSample {
	var samples []ProcessSample
	for line := range strings.SplitSeq(output, "\n") {
		match := processLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		pid, err1 := strconv.Atoi(match[1])
		parent, err2 := strconv.Atoi(match[2])
		rss, err3 := strconv.ParseInt(match[3], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || pid <= 1 || rss > (1<<53-1)/1024 {
			continue
		}
		samples = append(samples, ProcessSample{PID: pid, Parent: parent, Bytes: float64(rss * 1024), Born: spaces.ReplaceAllString(match[4], " ")})
	}
	return samples
}

// ReadProcessSamples lists every process on the computer.
func ReadProcessSamples(ctx context.Context) ([]ProcessSample, error) {
	stdout, _, err := Run(ctx, "/bin/ps", []string{"-axo", "pid=,ppid=,rss=,lstart="}, RunOptions{Env: []string{"LC_ALL=C"}, Timeout: 3 * time.Second, MaxBuffer: 8 * 1024 * 1024})
	if err != nil {
		return nil, err
	}
	return ParseProcessSamples(string(stdout)), nil
}
