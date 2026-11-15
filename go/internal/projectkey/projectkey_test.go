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
				upperInfo, err := os.Stat(upper)
				if err == nil {
					firstInfo, statErr := os.Stat(first)
					if statErr == nil && os.SameFile(firstInfo, upperInfo) {
						if For(first) != For(upper) {
							t.Fatalf("filesystem-equivalent case changed key for %q", first)
						}
						return
					}
				}
				if runtime.GOOS == "windows" {
					t.Fatalf("Windows case changed key for %q", first)
				}
				if For(first) == For(upper) {
					t.Fatalf("distinct case-sensitive paths shared a key: %q", first)
				}
			},
		},
		{
			name: "symlink alias",
			check: func(t *testing.T) {
				link := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(first, link); err != nil {
					t.Skipf("cannot create directory symlink: %v", err)
				}
				if For(first) != For(link) {
					t.Fatalf("symlink alias changed key: For(%q) = %q, For(%q) = %q", first, For(first), link, For(link))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.check)
	}
}
