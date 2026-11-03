//go:build !windows

package review

func resolveReviewerPathAliases(path string) string { return path }
