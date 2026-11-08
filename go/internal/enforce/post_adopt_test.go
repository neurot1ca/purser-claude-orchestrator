package enforce

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/ledger"
)

func TestPostAdopt(t *testing.T) {
	for _, name := range []string{"modify", "create", "delete", "out_of_scope", "dirty_at_admission", "nonempty_delta", "moved_head", "missing_head", "unknown_inspection", "truncated_baseline", "too_many_paths", "directory", "symlink", "failed_copy", "failed_restore"} {
		t.Run(name, func(t *testing.T) {
			repo := postDeltaRepo(t)
			if name == "failed_copy" || name == "failed_restore" {
				adoptTestWrite(t, repo, "scope/second.txt", "second base\n")
				git(t, repo, "add", "scope/second.txt")
				if name == "failed_restore" {
					adoptTestWrite(t, repo, ".gitattributes", "scope/second.txt filter=adoptFailure\n")
					git(t, repo, "add", ".gitattributes")
				}
				git(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "second fixture")
			}
			if name == "dirty_at_admission" {
				adoptTestWrite(t, repo, "scope/file.txt", "dirty before admission\n")
			}
			worktree := filepath.Join(t.TempDir(), "worktree")
			git(t, repo, "worktree", "add", "--detach", worktree, "HEAD")
			mainBaseline := CaptureMainCheckoutBaseline(repo, nil)
			baseline := postScopeBaseline(t, worktree, []string{"scope"})
			snapshot, err := ledger.CheckoutSnapshot(worktree)
			if err != nil {
				t.Fatal(err)
			}
			pending := map[string]any{"phase": "implementation", "scope": []string{"scope"}, "baseline": baseline, "lease_owner": "fixture", "worktree_path": worktree, "worktree_snapshot": snapshot, "main_checkout_baseline": mainBaseline}
			path := "scope/file.txt"
			if name == "create" {
				path = "scope/new/nested.txt"
			}
			if name == "out_of_scope" {
				path = "outside/file.txt"
			}
			if name == "moved_head" {
				git(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "advance")
			}
			if name == "missing_head" {
				delete(baseline, "head")
			}
			if name == "unknown_inspection" {
				mainBaseline["status"] = "UNKNOWN"
			}
			if name == "truncated_baseline" {
				mainBaseline["truncated"] = true
			}
			if name == "nonempty_delta" {
				adoptTestWrite(t, worktree, path, "real worktree change\n")
				path = "scope/misplaced.txt"
			}
			if name == "delete" {
				if err := os.Remove(filepath.Join(repo, path)); err != nil {
					t.Fatal(err)
				}
			} else {
				adoptTestWrite(t, repo, path, "worker change\n")
			}
			if name == "modify" {
				mode := os.FileMode(0755)
				if runtime.GOOS == "windows" {
					mode = 0400
				}
				if err := os.Chmod(filepath.Join(repo, path), mode); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = os.Chmod(filepath.Join(repo, path), 0600)
					_ = os.Chmod(filepath.Join(worktree, path), 0600)
				})
			}
			if name == "create" || name == "delete" {
				git(t, repo, "add", "-A", "--", path)
			}
			if name == "directory" || name == "symlink" {
				if err := os.Remove(filepath.Join(repo, path)); err != nil {
					t.Fatal(err)
				}
				if name == "directory" {
					if err := os.Mkdir(filepath.Join(repo, path), 0700); err != nil {
						t.Fatal(err)
					}
					adoptTestWrite(t, repo, path+"/child", "directory content\n")
				} else if err := os.Symlink(filepath.Join(repo, "outside/file.txt"), filepath.Join(repo, path)); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if name == "too_many_paths" {
				for i := 0; i < mainCheckoutPathCap; i++ {
					adoptTestWrite(t, repo, filepath.Join("scope", strings.Repeat("x", i+1)), "extra\n")
				}
			}
			if name == "failed_copy" || name == "failed_restore" {
				adoptTestWrite(t, repo, "scope/second.txt", "second change\n")
			}
			if name == "failed_restore" {
				git(t, repo, "config", "filter.adoptFailure.clean", "cat")
				git(t, repo, "config", "filter.adoptFailure.smudge", "false")
				git(t, repo, "config", "filter.adoptFailure.required", "true")
			}
			if name == "failed_copy" {
				prior := replaceAdoptFile
				calls := 0
				replaceAdoptFile = func(src, dst string) error {
					calls++
					if calls == 2 {
						return errors.New("injected filesystem failure")
					}
					return prior(src, dst)
				}
				t.Cleanup(func() { replaceAdoptFile = prior })
			}
			before := adoptTestFiles(t, repo)
			jobBefore := adoptTestFiles(t, worktree)
			beforeIndex, err := adoptGit(repo, "write-tree")
			if err != nil {
				t.Fatal(err)
			}
			bundle := baseBundle("")
			if _, err := assemblePostDelta(repo, pending, bundle, nil, nil); err != nil {
				t.Fatal(err)
			}
			success := name == "modify" || name == "create" || name == "delete"
			if success {
				pending["task_contract_grounded"], pending["route"] = true, "codex-main"
				schedulePostReview(worktree, pending, bundle, postProfile{}, nil,
					map[string]any{"small_delta_max_files": 0, "small_delta_max_lines": 0}, time.Second, true, true)
				if bundle["claude_review_mode"] != "FULL" || bundle["reviewer1_skipped_reason"] != nil {
					t.Fatalf("adoption did not reach reviewer gate: %#v", bundle)
				}
			}
			found := false
			findings, _ := bundle["findings"].([]any)
			for _, raw := range findings {
				f := raw.(map[string]any)
				if f["code"] == "MAIN_CHECKOUT_CHANGES_ADOPTED" {
					found = true
					if f["path_count"] != 1 || f["truncated"] != false || f["message"] != "Moved these files from the main checkout into the job worktree; review them as the job's change: "+path+"." {
						t.Fatalf("adoption finding: %#v", f)
					}
				}
				if success && (f["code"] == "MAIN_CHECKOUT_CHANGED_DURING_WORKTREE_JOB" || f["code"] == "IMPLEMENTATION_NO_DELTA") {
					t.Fatalf("stale finding: %#v", f)
				}
			}
			if found != success {
				t.Fatalf("adopted=%v want %v: %#v", found, success, bundle)
			}
			if bundle["force_full_review"] != true {
				t.Fatal("missing FULL review")
			}
			if !success {
				adoptTestEqualFiles(t, before, adoptTestFiles(t, repo))
				adoptTestEqualFiles(t, jobBefore, adoptTestFiles(t, worktree))
				afterIndex, err := adoptGit(repo, "write-tree")
				if err != nil || !bytes.Equal(beforeIndex, afterIndex) {
					t.Fatalf("main index changed after refusal/rollback: %q -> %q: %v", beforeIndex, afterIndex, err)
				}
				if name == "nonempty_delta" {
					patch, err := ledger.JobPatch(worktree, snapshot)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(patch, "real worktree change") || bundle["delta_files"] != 1 {
						t.Fatalf("own delta lost: %s", patch)
					}
					if applied := ledger.ApplyJobPatch(repo, patch); applied["applied"] != true {
						t.Fatalf("own patch no longer applies: %#v", applied)
					}
				}
				clean := ledger.ComputeJobDelta(worktree, snapshot)
				if name != "nonempty_delta" && clean["changed_file_count"] != 0 {
					t.Fatalf("rollback left delta: %#v", clean)
				}
				return
			}
			if bundle["delta_files"] != 1 || bundle["changed_paths_sha256"] == "" {
				t.Fatalf("recomputed delta missing: %#v", bundle)
			}
			entries, err := ledger.StatusEntries(repo)
			if err != nil || len(entries) != 0 {
				t.Fatalf("main not restored: %v, %v", entries, err)
			}
			if content, ok := before[filepath.FromSlash(path)]; ok {
				jobBefore[filepath.FromSlash(path)] = content
			} else {
				delete(jobBefore, filepath.FromSlash(path))
			}
			adoptTestEqualFiles(t, jobBefore, adoptTestFiles(t, worktree))
			if name == "modify" {
				info, err := os.Stat(filepath.Join(worktree, path))
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS != "windows" && info.Mode()&0111 != 0111 {
					t.Fatal("executable bit lost")
				}
				if runtime.GOOS == "windows" && info.Mode()&0200 != 0 {
					t.Fatal("read-only copy changed mode")
				}
			}
			patch, err := ledger.JobPatch(worktree, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			result := ledger.ApplyJobPatch(repo, patch)
			if result["applied"] != true {
				t.Fatalf("patch did not apply: %#v", result)
			}
			// Git apply may honor core.autocrlf; the byte-exact transfer was
			// checked above, before the normal apply path ran.
			applied := adoptTestFiles(t, repo)
			for path, data := range before {
				before[path] = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
			}
			for path, data := range applied {
				applied[path] = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
			}
			adoptTestEqualFiles(t, before, applied)
		})
	}
}

func TestPostAdoptAutocrlf(t *testing.T) {
	repo := postDeltaRepo(t)
	git(t, repo, "config", "core.autocrlf", "true")
	path := "scope/file.txt"
	if err := os.Remove(filepath.Join(repo, path)); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "restore", "--source=HEAD", "--staged", "--worktree", "--", ":(literal)"+path)
	worktree := filepath.Join(t.TempDir(), "worktree")
	git(t, repo, "worktree", "add", "--detach", worktree, "HEAD")
	fresh, err := os.ReadFile(filepath.Join(worktree, path))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := adoptGit(repo, "cat-file", "blob", "HEAD:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(blob, []byte("base\n")) || !bytes.Equal(fresh, []byte("base\r\n")) {
		t.Fatalf("fixture must have LF blob and fresh CRLF checkout: blob=%q checkout=%q", blob, fresh)
	}
	mainBaseline := CaptureMainCheckoutBaseline(repo, nil)
	if entries, ok := postStatusEntries(mainBaseline["status_entries"]); !ok || len(entries) != 0 {
		t.Fatalf("fixture must be clean at admission: %#v", mainBaseline)
	}
	baseline := postScopeBaseline(t, worktree, []string{"scope"})
	snapshot, err := ledger.CheckoutSnapshot(worktree)
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]any{
		"phase": "implementation", "scope": []string{"scope"}, "baseline": baseline,
		"lease_owner": "fixture", "worktree_path": worktree, "worktree_snapshot": snapshot,
		"main_checkout_baseline": mainBaseline,
	}
	adoptTestWrite(t, repo, path, "worker change\r\n")
	bundle := baseBundle("")
	if _, err := assemblePostDelta(repo, pending, bundle, nil, nil); err != nil {
		t.Fatal(err)
	}
	if bundle["delta_files"] != 1 {
		t.Fatalf("change was not adopted: %#v", bundle)
	}
	restored, err := os.ReadFile(filepath.Join(repo, path))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, fresh) {
		t.Fatalf("restored bytes=%q, fresh checkout=%q", restored, fresh)
	}
	status, err := adoptGit(repo, "status", "--porcelain=v1", "--", ":(literal)"+path)
	if err != nil || len(status) != 0 {
		t.Fatalf("restored path is dirty: %q: %v", status, err)
	}
	patch, err := ledger.JobPatch(worktree, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if applied := ledger.ApplyJobPatch(repo, patch); applied["applied"] != true {
		t.Fatalf("adopted patch did not apply: %#v", applied)
	}
}

func adoptTestWrite(t *testing.T, root, path, content string) {
	t.Helper()
	file := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func adoptTestFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			result[rel] = []byte("symlink:" + link)
			return err
		}
		result[rel], err = os.ReadFile(path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func adoptTestEqualFiles(t *testing.T, want, got map[string][]byte) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("file count changed: got %v want %v", got, want)
	}
	for path, data := range want {
		actual, ok := got[path]
		if !ok || !bytes.Equal(actual, data) {
			t.Fatalf("bytes changed at %s: got %q want %q", path, actual, data)
		}
	}
}
