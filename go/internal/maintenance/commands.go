package maintenance

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"purser-hook/internal/contextio"
	"purser-hook/internal/fsretry"
	"purser-hook/internal/jobs"
	"purser-hook/internal/proc"
)

var checkSleep = time.Sleep
var maintenanceClock = time.Now

func LatestQuota(now time.Time) map[string]any { return latestMaintenanceQuota(now, "codex") }

func CanonicalRepository(repository string) (string, error) {
	root, err := filepath.Abs(repository)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	command := proc.Hide(exec.Command("git", "-C", root, "rev-parse", "--show-toplevel"))
	proc.ConfigureNoWindow(command)
	data, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("not a project repository")
	}
	return filepath.Clean(strings.TrimSpace(string(data))), nil
}

func MaintenanceDirectory(state, root string) string {
	return filepath.Join(state, "projects", contextio.ProjectKey(root), "maintenance")
}

func RequireOptIn(state, root string) error {
	data, err := os.ReadFile(filepath.Join(MaintenanceDirectory(state, root), "opt-in.json"))
	if err != nil {
		return fmt.Errorf("Maintenance is not opted in for %s; run opt-in explicitly first.", root)
	}
	var marker struct {
		Enabled     bool   `json:"enabled"`
		ProjectRoot string `json:"project_root"`
	}
	if json.Unmarshal(data, &marker) != nil || !marker.Enabled || marker.ProjectRoot != root {
		return fmt.Errorf("Maintenance is not opted in for %s; run opt-in explicitly first.", root)
	}
	return nil
}

func writeCommandJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".maintenance-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return fsretry.Replace(name, path)
}

func Control(state, command, repository, decision, report string, acted []string) (string, error) {
	root, err := CanonicalRepository(repository)
	if err != nil {
		return "", err
	}
	dir := MaintenanceDirectory(state, root)
	marker := filepath.Join(dir, "opt-in.json")
	switch command {
	case "opt-in":
		if err := writeCommandJSON(marker, map[string]any{"project_root": root, "enabled": true}); err != nil {
			return "", err
		}
		consentPath := filepath.Join(dir, "consent.json")
		value := map[string]any{}
		if data, readErr := os.ReadFile(consentPath); readErr == nil {
			_ = json.Unmarshal(data, &value)
		}
		value["project_root"], value["decision"], value["source"] = root, "yes", "manual-opt-in"
		if err := writeCommandJSON(consentPath, value); err != nil {
			return "", err
		}
		return marker, nil
	case "opt-out":
		value := map[string]any{"project_root": root}
		if data, readErr := os.ReadFile(marker); readErr == nil {
			_ = json.Unmarshal(data, &value)
		}
		value["project_root"], value["enabled"] = root, false
		if err := writeCommandJSON(marker, value); err != nil {
			return "", err
		}
		return marker, nil
	case "consent":
		if decision != "yes" && decision != "no" {
			return "", errors.New("consent decision must be yes or no")
		}
		path := filepath.Join(dir, "consent.json")
		if err := writeCommandJSON(path, map[string]any{"project_root": root, "decision": decision}); err != nil {
			return "", err
		}
		if err := writeCommandJSON(marker, map[string]any{"project_root": root, "enabled": decision == "yes"}); err != nil {
			return "", err
		}
		return path, nil
	case "skip-second-show", "mark-done":
		path, err := filepath.Abs(report)
		if err != nil {
			return "", err
		}
		path, _ = filepath.EvalSymlinks(path)
		reportType := filepath.Base(filepath.Dir(path))
		expected := filepath.Join(dir, "reports", reportType)
		expected, _ = filepath.Abs(expected)
		if (reportType != "cleanup" && reportType != "test-gaps") || filepath.Clean(filepath.Dir(path)) != filepath.Clean(expected) || !strings.HasSuffix(filepath.Base(path), ".notice.json") {
			return "", errors.New("report notice must belong to this project")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		var notice map[string]any
		if err := json.Unmarshal(data, &notice); err != nil {
			return "", err
		}
		if command == "skip-second-show" {
			cycle, _ := notice["pending_cycle"].(string)
			if cycle == "" {
				cycle, _ = notice["last_shown_cycle"].(string)
			}
			if cycle == "" || cycle == "no-window" {
				return "", errors.New("no quota cycle is pending")
			}
			cycles, _ := notice["cycles"].(map[string]any)
			if cycles == nil {
				cycles = map[string]any{}
				notice["cycles"] = cycles
			}
			entry, _ := cycles[cycle].(map[string]any)
			if entry == nil {
				entry = map[string]any{}
				cycles[cycle] = entry
			}
			entry["skip_second"] = true
			notice["pending"] = false
			delete(notice, "pending_cycle")
			delete(notice, "pending_show")
		} else {
			findings, _ := notice["findings"].([]any)
			allowed := map[string]bool{}
			for _, raw := range findings {
				if item, ok := raw.(map[string]any); ok {
					if line, ok := item["path_line"].(string); ok {
						allowed[line] = true
					}
				}
			}
			for _, value := range acted {
				if !allowed[value] {
					return "", fmt.Errorf("acted path_line not found in report: %q", value)
				}
			}
			if reportType == "test-gaps" {
				if err := completeTestProposal(state, root, dir, notice); err != nil {
					return "", err
				}
			}
			notice["done"], notice["pending"], notice["shown"], notice["acted_findings"] = true, false, true, acted
		}
		return "", writeCommandJSON(path, notice)
	default:
		return "", fmt.Errorf("unsupported maintenance command: %s", command)
	}
}

var removeCompletedTestProposal = removeTestProposal

func completeTestProposal(state, root, dir string, notice map[string]any) error {
	readyPath := filepath.Join(dir, "reports", "test-gaps", "ready.json")
	completionPath := filepath.Join(filepath.Dir(readyPath), "completed.json")
	head, err := maintenanceCommandGit(root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return errors.New("test-gaps proposal is pending: HEAD is unavailable")
	}
	head = strings.TrimSpace(head)
	finish := func(completion map[string]any) error {
		if err := removeCompletedTestProposal(readyPath); err != nil {
			return fmt.Errorf("test-gaps proposal is pending: ready proposal could not be removed: %w", err)
		}
		notice["completion_job_id"], notice["completion_revision"] = completion["job_id"], completion["revision"]
		return nil
	}
	if data, readErr := os.ReadFile(completionPath); readErr == nil {
		var completion map[string]any
		if json.Unmarshal(data, &completion) != nil {
			return errors.New("test-gaps proposal is pending: completion record is invalid")
		}
		id, _ := completion["job_id"].(string)
		revision, _ := completion["revision"].(string)
		at, _ := completion["completed_at"].(string)
		_, timeErr := time.Parse(time.RFC3339Nano, at)
		if completion["commit"] == head && id != "" && revision != "" && timeErr == nil {
			data, readyErr := os.ReadFile(readyPath)
			var ready map[string]any
			if errors.Is(readyErr, os.ErrNotExist) || (readyErr == nil && json.Unmarshal(data, &ready) == nil && ready["job_id"] == id && ready["changed_paths_sha256"] == revision) {
				return finish(completion)
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("test-gaps proposal is pending: completion record is unavailable: %w", readErr)
	}
	data, err := os.ReadFile(readyPath)
	if err != nil {
		return fmt.Errorf("test-gaps proposal is pending: ready proposal is unavailable: %w", err)
	}
	var proposal map[string]any
	if json.Unmarshal(data, &proposal) != nil || proposal["status"] != "ready" {
		return errors.New("test-gaps proposal is pending: ready proposal is invalid")
	}
	id, _ := proposal["job_id"].(string)
	digest, _ := proposal["changed_paths_sha256"].(string)
	job, err := jobs.Read(filepath.Join(state, "codex_exec_mcp", "jobs", id+".json"))
	if err != nil {
		return fmt.Errorf("test-gaps proposal is pending: applied job record is unavailable: %w", err)
	}
	var actualID, recordedID, identitySource, applyState, appliedRevision, worktree, repository string
	var maintenance bool
	job.Get("jobId", &actualID)
	job.Get("applyState", &applyState)
	job.Get("appliedRevision", &appliedRevision)
	job.Get("worktreePath", &worktree)
	job.Get("repoRoot", &repository)
	job.Get("maintenance_test_job", &maintenance)
	job.Get("maintenance_job_id", &recordedID)
	job.Get("maintenance_identity_source", &identitySource)
	if id == "" || digest == "" || actualID != id || recordedID != id || identitySource != "host" || !maintenance || applyState != "applied" || appliedRevision != digest || !sameProjectPath(repository, root) || worktree == "" || sameProjectPath(worktree, root) {
		return errors.New("test-gaps proposal is pending: its job has not been applied at the proposed revision")
	}
	status, revision, err := jobs.MaintenanceApplyValidation(state, repository, id)
	if err != nil {
		return fmt.Errorf("test-gaps proposal is pending: terminal host validation is unavailable: %w", err)
	}
	if status != "passed" {
		return fmt.Errorf("test-gaps proposal is pending: terminal host validation status is %q", status)
	}
	if revision != digest {
		return errors.New("test-gaps proposal is pending: terminal host validation revision does not match the proposal")
	}
	if err := verifyTestProposalCommit(root, proposal, head); err != nil {
		return fmt.Errorf("test-gaps proposal is pending: %w", err)
	}
	current, err := maintenanceCommandGit(root, "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(current) != head {
		return errors.New("test-gaps proposal is pending: HEAD changed during completion")
	}
	completion := map[string]any{"job_id": id, "commit": head, "revision": digest, "completed_at": time.Now().UTC().Format(time.RFC3339Nano)}
	if err := writeCommandJSON(completionPath, completion); err != nil {
		return err
	}
	return finish(completion)
}

func verifyTestProposalCommit(root string, proposal map[string]any, head string) error {
	baseline, _ := proposal["baseline_head"].(string)
	projectRoot, _ := proposal["project_root"].(string)
	if baseline == "" || !sameProjectPath(projectRoot, root) {
		return errors.New("proposal baseline or project root is invalid")
	}
	parents, err := maintenanceCommandGit(root, "show", "-s", "--format=%P", head)
	if err != nil || strings.TrimSpace(parents) != baseline {
		return errors.New("HEAD must be one commit directly on the proposal baseline")
	}
	changed, err := maintenanceCommandGit(root, "diff-tree", "--no-commit-id", "--name-only", "-z", "--no-renames", baseline, head)
	if err != nil {
		return errors.New("HEAD commit paths could not be verified")
	}
	want, ok := proposal["changed_test_paths"].([]any)
	if !ok || len(want) == 0 {
		return errors.New("proposal test paths are invalid")
	}
	expected := make(map[string]bool, len(want))
	for _, raw := range want {
		path, ok := raw.(string)
		if !ok || path == "" {
			return errors.New("proposal test paths are invalid")
		}
		if filepath.IsAbs(path) {
			rel, relErr := filepath.Rel(projectRoot, path)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return errors.New("proposal test path is outside its project")
			}
			path = rel
		}
		path = filepath.ToSlash(filepath.Clean(path))
		key := path
		if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if expected[key] {
			return errors.New("proposal contains duplicate test paths")
		}
		expected[key] = true
	}
	got := strings.Split(strings.TrimSuffix(changed, "\x00"), "\x00")
	if len(got) != len(expected) {
		return errors.New("HEAD commit does not change exactly the proposed test paths")
	}
	for _, path := range got {
		key := filepath.ToSlash(filepath.Clean(path))
		if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if !expected[key] {
			return errors.New("HEAD commit does not change exactly the proposed test paths")
		}
		if _, err := maintenanceCommandGit(root, "cat-file", "blob", head+":"+path); err != nil {
			return fmt.Errorf("HEAD test content could not be read: %q", path)
		}
	}
	// Match JobChangedPathsSHA256's binary patch format, reading only committed
	// versions so later checkout edits cannot substitute for the validated patch.
	args := []string{"diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-renames", baseline, head, "--"}
	for _, path := range got {
		args = append(args, ":(literal)"+path)
	}
	patch, err := maintenanceCommandGit(root, args...)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256([]byte(patch))) != proposal["changed_paths_sha256"] {
		return errors.New("HEAD commit content does not match the validated proposal revision")
	}
	return nil
}

func maintenanceCommandGit(root string, args ...string) (string, error) {
	command := proc.Hide(exec.Command("git", append([]string{"-C", root}, args...)...))
	proc.ConfigureNoWindow(command)
	output, err := command.Output()
	return string(output), err
}

// DelayedCheck is the detached SessionStart worker. It scans enabled projects,
// honors the requested project, then chooses its oldest due report type.
func DelayedCheck(repository, state string, delay time.Duration, runner Runner) (string, error) {
	checkSleep(minDuration(maxDuration(delay, 0), 5*time.Minute))
	now := maintenanceClock()
	if err := runner.resolveCoder(repository); err != nil {
		return "", err
	}
	quota := latestMaintenanceQuota(now, runner.CoderAgent)
	root, err := CanonicalRepository(repository)
	if err != nil {
		return "", err
	}
	if err := ScanOptedInProjects(root, state); err != nil {
		return "", nil
	}
	dir := MaintenanceDirectory(state, root)
	var due []string
	for _, kind := range []string{"cleanup", "test-gaps"} {
		if contextio.MaintenanceEligible(root, state, now, quota, kind, runner.CoderAgent == "codex").Eligible {
			due = append(due, kind)
		}
	}
	if len(due) == 0 {
		return "", nil
	}
	kind := contextio.SelectMaintenanceReportType(dir, due)
	return RunAndPersistReport(runner, state, root, kind)
}

func ScanOptedInProjects(candidate, state string) error {
	projects := filepath.Join(state, "projects")
	entries, err := os.ReadDir(projects)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	found := false
	for _, entry := range entries {
		path := filepath.Join(projects, entry.Name(), "maintenance", "opt-in.json")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var marker struct {
			Enabled     bool   `json:"enabled"`
			ProjectRoot string `json:"project_root"`
		}
		if json.Unmarshal(data, &marker) != nil || !marker.Enabled {
			continue
		}
		root, err := CanonicalRepository(marker.ProjectRoot)
		if err == nil && root == marker.ProjectRoot && MaintenanceDirectory(state, root) == filepath.Dir(path) && root == candidate {
			found = true
		}
	}
	if !found {
		return errors.New("project not opted in")
	}
	return nil
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
