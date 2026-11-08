package enforce

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"purser-hook/internal/fsretry"
	"purser-hook/internal/ledger"
	"purser-hook/internal/proc"
)

var replaceAdoptFile = fsretry.Replace

// adoptMainCheckout is a best-effort transaction. Nothing in main is replaced
// until every destination has been copied and verified. The saved states also
// cover partial restores and delta/index computation failures.
func adoptMainCheckout(root string, pending, delta, inspection map[string]any) (adopted map[string]any, digest string) {
	worktree, _ := pending["worktree_path"].(string)
	files, fok := pythonInt(delta["changed_file_count"])
	lines, lok := pythonInt(delta["changed_line_count"])
	if pending["phase"] != "implementation" || worktree == "" || delta["status"] != ledger.OK || !fok || !lok || files != 0 || lines != 0 {
		return nil, ""
	}
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, ""
	}
	worktree, err = filepath.EvalSymlinks(worktree)
	if err != nil || samePath(root, worktree) {
		return nil, ""
	}
	findings, _ := inspection["findings"].([]any)
	var finding map[string]any
	for _, raw := range findings {
		f := mapValue(raw)
		if f["code"] == "MAIN_CHECKOUT_STATUS_UNKNOWN" {
			return nil, ""
		}
		if f["code"] == "MAIN_CHECKOUT_CHANGED_DURING_WORKTREE_JOB" {
			finding = f
		}
	}
	if finding == nil || finding["truncated"] != false {
		return nil, ""
	}
	paths, ok := postStringList(finding["paths"])
	if !ok || len(paths) == 0 || len(paths) > mainCheckoutPathCap {
		return nil, ""
	}
	baseline := mapValue(pending["baseline"])
	head, _ := baseline["head"].(string)
	if head == "" {
		return nil, ""
	}
	current, err := adoptGit(root, "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(string(current)) != head {
		return nil, ""
	}
	mainBaseline := mapValue(pending["main_checkout_baseline"])
	dirty, ok := postStatusEntries(mainBaseline["status_entries"])
	if !ok {
		return nil, ""
	}
	scope, _ := postStringList(pending["scope"])
	ignoreCase, _ := adoptGit(root, "config", "--bool", "core.ignorecase")
	caseFold := runtime.GOOS == "windows" || strings.TrimSpace(string(ignoreCase)) == "true"
	inScope := func(path string) bool {
		for _, item := range scope {
			item = strings.TrimSuffix(strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(item), `\`, "/"), "/"), "/**")
			candidate := path
			if caseFold {
				candidate, item = strings.ToLower(candidate), strings.ToLower(item)
			}
			if postPathInScope(candidate, []string{item}) {
				return true
			}
		}
		return false
	}
	type change struct {
		path                  string
		main, job             adoptFile
		tracked               bool
		mainBackup, jobBackup string
	}
	changes := make([]change, 0, len(paths))
	entries, err := ledger.StatusEntries(root)
	if err != nil {
		return nil, ""
	}
	for _, entry := range entries {
		// Porcelain collapses a rename into one entry. Its original path is
		// not part of the inspection's comparable list; do not move half of it.
		if entry["original_path"] != "" {
			return nil, ""
		}
	}
	for _, path := range paths {
		if !inScope(path) {
			return nil, ""
		}
		for prior := range dirty {
			if samePath(prior, path) || caseFold && strings.EqualFold(prior, path) {
				return nil, ""
			}
		}
		main, e := readAdoptFile(root, path)
		if e != nil {
			return nil, ""
		}
		job, e := readAdoptFile(worktree, path)
		if e != nil {
			return nil, ""
		}
		// Refuse non-file HEAD entries even for deletions. Git restores the
		// checkout bytes and mode later, including configured conversions.
		entry, e := adoptGit(root, "ls-tree", "-z", "HEAD", "--", ":(literal)"+path)
		if e != nil {
			return nil, ""
		}
		if len(entry) > 0 {
			fields := strings.SplitN(string(entry), " ", 3)
			if len(fields) != 3 || fields[0] != "100644" && fields[0] != "100755" {
				return nil, ""
			}
		}
		changes = append(changes, change{path: path, main: main, job: job, tracked: len(entry) > 0})
	}
	// A filtered empty delta can conceal cache/ignored work. Do not adopt over it.
	snapshot, _ := pending["worktree_snapshot"].(string)
	rawDelta := ledger.ComputeJobDelta(worktree, snapshot)
	if rawDelta["status"] != ledger.OK || rawDelta["changed_file_count"] != 0 {
		return nil, ""
	}
	mainIndex, err := adoptGit(root, "write-tree")
	if err != nil {
		return nil, ""
	}
	jobIndex, err := adoptGit(worktree, "write-tree")
	if err != nil {
		return nil, ""
	}
	// Keep synced originals outside the checked-out trees. Rollback can then
	// rename them back without allocating disk space after a failed write.
	backups := []string{}
	keepBackups := false
	defer func() {
		if !keepBackups {
			for _, path := range backups {
				removeAdoptTemp(path)
			}
		}
	}()
	for _, tree := range []string{root, worktree} {
		dir, e := adoptGit(tree, "rev-parse", "--absolute-git-dir")
		if e != nil {
			return nil, ""
		}
		for i := range changes {
			file, backup := changes[i].main, &changes[i].mainBackup
			if tree == worktree {
				file, backup = changes[i].job, &changes[i].jobBackup
			}
			if !file.exists {
				continue
			}
			*backup, e = stageAdoptFile(strings.TrimSpace(string(dir)), file)
			if e != nil {
				return nil, ""
			}
			backups = append(backups, *backup)
		}
	}
	jobTouched, mainTouched := 0, 0
	success := false
	defer func() {
		if success {
			return
		}
		// Retain the durable backups if an external lock also blocks rollback.
		for i := mainTouched - 1; i >= 0; i-- {
			if e := restoreAdoptFile(root, changes[i].path, changes[i].main, changes[i].mainBackup); e != nil {
				keepBackups = true
				log.Printf("main checkout adoption rollback: %s: %v", changes[i].path, e)
			}
		}
		for i := jobTouched - 1; i >= 0; i-- {
			if e := restoreAdoptFile(worktree, changes[i].path, changes[i].job, changes[i].jobBackup); e != nil {
				keepBackups = true
				log.Printf("worktree adoption rollback: %s: %v", changes[i].path, e)
			}
		}
		if mainTouched > 0 {
			if _, e := adoptGit(root, "read-tree", strings.TrimSpace(string(mainIndex))); e != nil {
				log.Printf("main checkout adoption index rollback: %v", e)
			}
		}
		if jobTouched > 0 {
			if _, e := adoptGit(worktree, "read-tree", strings.TrimSpace(string(jobIndex))); e != nil {
				log.Printf("worktree adoption index rollback: %v", e)
			}
		}
	}()
	for i, c := range changes {
		jobTouched = i + 1
		if err = writeAdoptFile(worktree, c.path, c.main); err != nil {
			return nil, ""
		}
		copied, e := readAdoptFile(worktree, c.path)
		if e != nil || !sameAdoptFile(copied, c.main) {
			return nil, ""
		}
	}
	// Recheck all originals before replacing any, including HEAD evidence.
	current, err = adoptGit(root, "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(string(current)) != head {
		return nil, ""
	}
	for _, c := range changes {
		now, e := readAdoptFile(root, c.path)
		if e != nil || !sameAdoptFile(now, c.main) {
			return nil, ""
		}
	}
	for i, c := range changes {
		mainTouched = i + 1
		// One literal path per invocation bounds argument size independently
		// of path count, including on Windows with long checkout paths.
		if c.tracked {
			if runtime.GOOS == "windows" && c.main.exists && c.main.mode&0200 == 0 {
				if err = os.Chmod(filepath.Join(root, filepath.FromSlash(c.path)), c.main.mode|0200); err != nil {
					return nil, ""
				}
			}
			err = fsretry.Retry(func() error {
				_, e := adoptGit(root, "restore", "--source=HEAD", "--staged", "--worktree", "--", ":(literal)"+c.path)
				return e
			}, 2*time.Second)
		} else {
			err = writeAdoptFile(root, c.path, adoptFile{})
			if err == nil {
				_, err = adoptGit(root, "reset", "-q", "HEAD", "--", ":(literal)"+c.path)
			}
		}
		if err != nil {
			return nil, ""
		}
	}
	for _, c := range changes {
		status, e := adoptGit(root, "status", "--porcelain=v1", "--untracked-files=all", "--", ":(literal)"+c.path)
		if e != nil || len(status) != 0 {
			return nil, ""
		}
	}
	adopted = ledger.ComputeJobDelta(worktree, snapshot)
	adopted, err = ledger.FilterJobDeltaChecked(worktree, adopted)
	if err != nil || adopted["status"] != ledger.OK || adopted["changed_file_count"] != len(paths) {
		return nil, ""
	}
	digest, err = ledger.JobChangedPathsSHA256(worktree, snapshot)
	if err != nil {
		return nil, ""
	}
	finding["code"] = "MAIN_CHECKOUT_CHANGES_ADOPTED"
	finding["message"] = "Moved these files from the main checkout into the job worktree; review them as the job's change: " + strings.Join(paths, ", ") + "."
	success = true
	return adopted, digest
}

type adoptFile struct {
	data   []byte
	mode   os.FileMode
	exists bool
}

func sameAdoptFile(a, b adoptFile) bool {
	return a.exists == b.exists && (!a.exists || sha256.Sum256(a.data) == sha256.Sum256(b.data) && (runtime.GOOS == "windows" || a.mode&0111 == b.mode&0111))
}

// Walk every component with Lstat: EvalSymlinks alone would allow links whose
// targets happen to remain inside the repository, including Windows junctions.
func readAdoptFile(root, path string) (adoptFile, error) {
	name := filepath.FromSlash(path)
	if !filepath.IsLocal(name) || filepath.Clean(name) == "." {
		return adoptFile{}, os.ErrInvalid
	}
	current := root
	parts := strings.Split(name, string(filepath.Separator))
	for i, part := range parts {
		if strings.EqualFold(part, ".git") {
			return adoptFile{}, os.ErrInvalid
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return adoptFile{}, nil
		}
		if err != nil {
			return adoptFile{}, err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return adoptFile{}, os.ErrInvalid
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil || !samePath(resolved, current) {
			return adoptFile{}, os.ErrInvalid
		}
		if i < len(parts)-1 {
			if !info.IsDir() {
				return adoptFile{}, os.ErrInvalid
			}
		} else {
			if !info.Mode().IsRegular() {
				return adoptFile{}, os.ErrInvalid
			}
			var data []byte
			err = fsretry.Retry(func() error { var e error; data, e = os.ReadFile(current); return e }, 2*time.Second)
			return adoptFile{data, info.Mode().Perm(), true}, err
		}
	}
	return adoptFile{}, os.ErrInvalid
}

func writeAdoptFile(root, path string, file adoptFile) error {
	prior, err := readAdoptFile(root, path)
	if err != nil {
		return err
	}
	target := filepath.Join(root, filepath.FromSlash(path))
	if !file.exists && !prior.exists {
		return nil
	}
	if file.exists {
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		temp, e := stageAdoptFile(filepath.Dir(target), file)
		if e != nil {
			return e
		}
		defer removeAdoptTemp(temp)
		if runtime.GOOS == "windows" && prior.exists && prior.mode&0200 == 0 {
			if err = os.Chmod(target, prior.mode|0200); err != nil {
				return err
			}
		}
		err = replaceAdoptFile(temp, target)
	} else {
		if runtime.GOOS == "windows" && prior.mode&0200 == 0 {
			if err = os.Chmod(target, prior.mode|0200); err != nil {
				return err
			}
		}
		err = fsretry.Retry(func() error { return os.Remove(target) }, 2*time.Second)
	}
	if err != nil && prior.exists {
		_ = os.Chmod(target, prior.mode)
	}
	return err
}

func stageAdoptFile(dir string, file adoptFile) (string, error) {
	temp, err := os.CreateTemp(dir, ".purser-adopt-*")
	if err != nil {
		return "", err
	}
	_, err = temp.Write(file.data)
	if err == nil {
		err = temp.Chmod(file.mode)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		removeAdoptTemp(temp.Name())
		return "", err
	}
	return temp.Name(), nil
}

func removeAdoptTemp(path string) {
	if runtime.GOOS == "windows" {
		_ = os.Chmod(path, 0600)
	}
	_ = fsretry.Retry(func() error { return os.Remove(path) }, 2*time.Second)
}

func restoreAdoptFile(root, path string, file adoptFile, backup string) error {
	if backup != "" {
		prior, err := readAdoptFile(root, path)
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(path))
		if runtime.GOOS == "windows" && prior.exists && prior.mode&0200 == 0 {
			if err := os.Chmod(target, prior.mode|0200); err != nil {
				return err
			}
		}
		if err := fsretry.Replace(backup, target); err == nil {
			return nil
		}
		// Linked worktrees can live on another volume from their Git directory.
		// In that case retain the backup until the ordinary replacement succeeds.
	}
	return writeAdoptFile(root, path, file)
}

func adoptGit(root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := proc.Hide(exec.CommandContext(ctx, "git", append([]string{"-c", "core.longpaths=true", "-C", root}, args...)...))
	output, err := command.Output()
	if err != nil {
		// Git reports filesystem locks via stderr rather than a Go errno.
		// Translate those failures so fsretry applies its bounded policy.
		if failure, ok := err.(*exec.ExitError); ok {
			message := strings.ToLower(string(failure.Stderr))
			if strings.Contains(message, "permission denied") || strings.Contains(message, "access is denied") ||
				strings.Contains(message, "being used by another process") || strings.Contains(message, "sharing violation") ||
				strings.Contains(message, "resource busy") || strings.Contains(message, "index.lock") && strings.Contains(message, "file exists") {
				err = fmt.Errorf("%w: %v", os.ErrPermission, err)
			}
		}
		return nil, fmt.Errorf("adoption git: %w", err)
	}
	return output, nil
}
