package jobs

import (
	"runtime"
	"time"
)

func hostTestWaitBudget(base time.Duration) time.Duration {
	if runtime.GOOS == "windows" {
		return min(base*10, 2*time.Minute)
	}
	return base
}
