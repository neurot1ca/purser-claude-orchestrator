package jobs

import (
	"testing"
	"time"
)

func TestValidationBudget(t *testing.T) {
	for _, tc := range []struct {
		packages int
		want     time.Duration
	}{
		{packages: 0, want: 10 * time.Minute},
		{packages: 4, want: 10 * time.Minute},
		{packages: 5, want: 11*time.Minute + 30*time.Second},
		{packages: 11, want: 20*time.Minute + 30*time.Second},
		{packages: 18, want: 30 * time.Minute},
		{packages: 100, want: 30 * time.Minute},
	} {
		if got := validationBudget(tc.packages); got != tc.want {
			t.Errorf("validationBudget(%d) = %s, want %s", tc.packages, got, tc.want)
		}
	}
}
