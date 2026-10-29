package projectkey

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFor(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()

	tests := []struct {
		name  string
		check func(t *testing.T)
	}{
		{
			name: "stable for same path",
			check: func(t *testing.T) {
				if got, again := For(first), For(first); got != again {
					t.Fatalf("For(%q) = %q, repeated call = %q", first, got, again)
				}
			},
		},
		{
			name: "different repositories",
			check: func(t *testing.T) {
				if For(first) == For(second) {
					t.Fatalf("different paths have the same key: %q", first)
				}
			},
		},
		{
			name: "clean separators and trailing separator",
			check: func(t *testing.T) {
				withTrailingSeparator := first + string(os.PathSeparator)
				if For(first) != For(withTrailingSeparator) {
					t.Fatalf("trailing separator changed key for %q", first)
				}
				if runtime.GOOS == "windows" {
					withSlashes := strings.ReplaceAll(first, `\`, "/")
					if For(first) != For(withSlashes) {
						t.Fatalf("alternate Windows separators changed key for %q", first)
					}
				}
				if filepath.Clean(first) != filepath.Clean(withTrailingSeparator) {
					t.Fatal("test paths did not clean to the same path")
				}
			},
		},
		{
			name: "case normalization",
			check: func(t *testing.T) {
				upper := strings.ToUpper(first)
				if runtime.GOOS == "windows" {
					if For(first) != For(upper) {
						t.Fatalf("Windows case changed key for %q", first)
					}
				} else if For(first) == For(upper) {
					t.Fatalf("non-Windows path casing was unexpectedly normalized: %q", first)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.check)
	}
}
