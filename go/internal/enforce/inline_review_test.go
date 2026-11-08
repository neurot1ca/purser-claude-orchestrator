package enforce

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func inlineReviewBundle(patch string) map[string]any {
	sum := sha256.Sum256([]byte(patch))
	return map[string]any{
		"phase": "implementation", "claude_review_mode": "FULL", "findings": []any{},
		"implementer_report": "review required", "changed_paths": []string{"src/file.go"},
		"changed_paths_sha256":       hex.EncodeToString(sum[:]),
		"_complete_review_requested": true,
		"_complete_review_patch":     patch, "_complete_review_revision": hex.EncodeToString(sum[:]),
	}
}

func TestInlineReviewCompletenessAndRevisionBinding(t *testing.T) {
	patch := "diff --git a/src/file.go b/src/file.go\n--- a/src/file.go\n+++ b/src/file.go\n@@ -1 +1 @@\n-old\n+new café\n"
	root := filepath.Join(t.TempDir(), "review space 雪")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	compact, _, err := finalizePostArtifacts(root, map[string]any{"session_id": "s", "tool_use_id": "small"}, inlineReviewBundle(patch), 8000, 1200)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonicalJSON(compact)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(patch))
	if compact["review_diff_complete"] != true || compact["review_diff_inline"] != patch || compact["review_diff_bytes"] != len([]byte(patch)) || compact["review_diff_revision"] != hex.EncodeToString(sum[:]) || compact["changed_paths_sha256"] != compact["review_diff_revision"] || len(encoded) > reviewDiffResponseLimit {
		t.Fatalf("inline diff lost completeness or revision binding: bytes=%d bundle=%#v", len(encoded), compact)
	}
	stored, err := os.Stat(filepath.Join(root, "review_diffs", hex.EncodeToString(sum[:])+".diff"))
	if err != nil || runtime.GOOS != "windows" && stored.Mode().Perm()&0077 != 0 {
		t.Fatalf("persisted review diff permissions or path are unsafe: stat=%v err=%v", stored, err)
	}
	t.Logf("small delivered response bytes=%d patch bytes=%d", len(encoded), len([]byte(patch)))
}

func TestInlineReviewUsesReferenceForLargeOrUnsupportedPatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch string
	}{
		{name: "oversized", patch: "diff --git a/a b/a\n" + strings.Repeat("x", reviewDiffInlineLimit+1)},
		{name: "mode change", patch: "diff --git a/a b/a\nold mode 100644\nnew mode 100755\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			compact, _, err := finalizePostArtifacts(root, map[string]any{"session_id": "s", "tool_use_id": tc.name}, inlineReviewBundle(tc.patch), 8000, 1200)
			if err != nil {
				t.Fatal(err)
			}
			path, ok := compact["review_diff_path"].(string)
			if !ok || compact["review_diff_inline"] != nil || compact["review_diff_complete"] != true || compact["review_diff_bytes"] != len([]byte(tc.patch)) || compact["review_diff_revision"] != compact["changed_paths_sha256"] {
				t.Fatalf("unsupported patch did not use a complete reference: %#v", compact)
			}
			stored, readErr := os.ReadFile(path)
			if readErr != nil || string(stored) != tc.patch {
				t.Fatalf("reference is not exact: read err=%v bytes=%d want=%d", readErr, len(stored), len(tc.patch))
			}
			if !strings.Contains(stringValue(compact["review_diff_notice"]), path) {
				t.Fatalf("review notice omits path %q: %#v", path, compact["review_diff_notice"])
			}
			if tc.name == "oversized" {
				encoded, encodeErr := canonicalJSON(compact)
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				t.Logf("oversized delivered response bytes=%d patch bytes=%d", len(encoded), len([]byte(tc.patch)))
			}
		})
	}
}

func TestInlineReviewPublicationFailureKeepsReviewRequired(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "review_diffs"), []byte("locked"), 0600); err != nil {
		t.Fatal(err)
	}
	compact, _, err := finalizePostArtifacts(root, map[string]any{"session_id": "s", "tool_use_id": "blocked"}, inlineReviewBundle("text patch"), 8000, 1200)
	if err != nil {
		t.Fatal(err)
	}
	if compact["review_diff_complete"] != false || compact["review_diff_notice"] != "Review diff unavailable; complete review is still required." || compact["claude_review_mode"] != "FULL" {
		t.Fatalf("publication failure weakened review requirement: %#v", compact)
	}
	root = t.TempDir()
	event := map[string]any{"session_id": "s", "tool_use_id": "missing"}
	referenced, path, err := finalizePostArtifacts(root, event, inlineReviewBundle("diff --git a/a b/a\nold mode 100644\nnew mode 100755\n"), 8000, 1200)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(stringValue(referenced["review_diff_path"])); err != nil {
		t.Fatal(err)
	}
	saved, err := readAsyncObject(path)
	if err != nil {
		t.Fatal(err)
	}
	recovered, _, err := finalizePostArtifacts(root, event, saved, 8000, 1200)
	if err != nil || recovered["review_diff_complete"] != false || recovered["review_diff_path"] != nil || recovered["review_diff_notice"] != "Review diff unavailable; complete review is still required." {
		t.Fatalf("missing patch lost the truthful fallback: %#v err=%v", recovered, err)
	}
	artifact, err := readAsyncObject(path)
	if err != nil || !asyncSuccessSubset(artifact, recovered) {
		t.Fatalf("read failure left stale artifact presentation: %#v err=%v", artifact, err)
	}
}

func TestInlineReviewReplayAndLegacyCompatibility(t *testing.T) {
	root := t.TempDir()
	event := map[string]any{"session_id": "s", "tool_use_id": "replay"}
	bundle := inlineReviewBundle("diff --git a/a b/a\n@@ -1 +1 @@\n-old\n" + strings.Repeat("+new\n", 2000))
	first, path, err := finalizePostArtifacts(root, event, bundle, 8000, 1200)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := readAsyncObject(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []map[string]any{bundle, persisted} {
		for key := range value {
			if strings.HasPrefix(key, "_complete_review_") {
				t.Fatalf("transient review input retained: %s", key)
			}
			if strings.HasPrefix(key, "review_diff_") {
				got, _ := canonicalJSON(value[key])
				want, _ := canonicalJSON(first[key])
				if string(got) != string(want) {
					t.Fatalf("persisted presentation differs for %s", key)
				}
			}
		}
	}
	second, _, err := finalizePostArtifacts(root, event, persisted, 8000, 1200)
	if err != nil {
		t.Fatal(err)
	}
	if first["review_diff_inline"] == nil || !asyncSuccessEqual(first, second) {
		t.Fatalf("replayed review reference changed: first=%#v second=%#v", first, second)
	}
	noDelta := inlineReviewBundle("")
	noDelta["delta_files"] = 0
	empty, _, err := finalizePostArtifacts(t.TempDir(), event, noDelta, 8000, 1200)
	if err != nil || empty["review_diff_complete"] != nil || empty["review_diff_notice"] != nil {
		t.Fatalf("no-delta bundle gained review presentation: %#v err=%v", empty, err)
	}
	legacy := map[string]any{"phase": "implementation", "claude_review_mode": "FULL", "findings": []any{}, "implementer_report": "legacy"}
	old, _, err := finalizePostArtifacts(t.TempDir(), event, legacy, 8000, 1200)
	if err != nil || old["claude_review_mode"] != "FULL" || old["review_diff_notice"] != nil || old["review_diff_complete"] != nil {
		t.Fatalf("legacy bundle did not retain safe behavior: %#v err=%v", old, err)
	}
}
