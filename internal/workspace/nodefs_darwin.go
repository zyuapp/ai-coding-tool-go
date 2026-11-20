package workspace

import (
	"syscall"
	"time"
)

func accessTime(stat *syscall.Stat_t) time.Time {
	return time.Unix(stat.Atimespec.Unix())
}
