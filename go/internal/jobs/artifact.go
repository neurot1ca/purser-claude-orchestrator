package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"purser-hook/internal/contextio"
)

// Artifact returns a persisted job artifact without entering the result/finalization path.
func Artifact(stateRoot, jobID, artifact string) map[string]any {
	job := readJob(filepath.Join(stateRoot, "codex_exec_mcp", "jobs"), jobID)
	if job == nil {
		return artifactError(jobID, "Job record is unavailable")
	}
	var index map[string]any
	indexData, err := os.ReadFile(filepath.Join(stateRoot, "async_jobs", jobID+".json"))
	if err == nil {
		err = json.Unmarshal(indexData, &index)
	}
	callKey, _ := index["call_key"].(string)
	if err != nil || callKey == "" || filepath.Base(callKey) != callKey {
		return artifactError(jobID, "Plan artifact is unavailable")
	}
	if index["stage"] != "CONSUMED" && index["stage"] != "FAILED" {
		return artifactError(jobID, "Call codex_result without artifact first to finalize this job, then request the artifact.")
	}
	var bundle map[string]any
	bundleData, err := os.ReadFile(filepath.Join(stateRoot, "bundle_artifacts", callKey+".json"))
	if err == nil {
		err = json.Unmarshal(bundleData, &bundle)
	}
	if err != nil || bundle == nil {
		return artifactError(jobID, "Full bundle artifact is unavailable")
	}
	if artifact == "bundle" {
		bundlePath, _ := bundle["full_bundle_path"].(string)
		if bundlePath == "" || !insideBundleArtifactsDirectory(stateRoot, bundlePath) {
			return artifactError(jobID, "Full bundle artifact path is outside the state root bundle_artifacts directory")
		}
		data, err := readArtifact(bundlePath)
		if err != nil {
			return artifactError(jobID, "Full bundle artifact is unavailable")
		}
		return artifactSuccess(jobID, string(data))
	}
	planPath, _ := bundle["plan_path"].(string)
	if planPath == "" {
		return artifactError(jobID, "This job has no plan artifact; valid values: bundle, none")
	}
	if !insidePlansDirectory(stateRoot, planPath) {
		return artifactError(jobID, "Plan artifact path is outside the state root plans directory")
	}
	data, err := readArtifact(planPath)
	if err != nil {
		return artifactError(jobID, "Plan artifact is unavailable")
	}
	if artifact == "plan" {
		return artifactSuccess(jobID, string(data))
	}
	section, valid, err := contextio.PlanMilestoneSection(data, artifact)
	if err != nil {
		return artifactError(jobID, "Plan milestones are unavailable")
	}
	if section == "" {
		values := "bundle, plan"
		if len(valid) > 0 {
			values += ", " + strings.Join(valid, ", ")
		} else {
			values += ", none"
		}
		return artifactError(jobID, "Unknown artifact; valid values: "+values)
	}
	return artifactSuccess(jobID, section)
}

func artifactSuccess(jobID, content string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": content}},
		"structuredContent": map[string]any{"jobId": jobID, "artifact": content}, "isError": false}
}

func artifactError(jobID, message string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": message}},
		"structuredContent": map[string]any{"jobId": jobID, "errorCode": "PLAN_ARTIFACT_UNAVAILABLE", "message": message}, "isError": true}
}

func insidePlansDirectory(stateRoot, candidate string) bool {
	return insideArtifactDirectory(stateRoot, "plans", candidate)
}

func insideBundleArtifactsDirectory(stateRoot, candidate string) bool {
	return insideArtifactDirectory(stateRoot, "bundle_artifacts", candidate)
}

func insideArtifactDirectory(stateRoot, directory, candidate string) bool {
	base, err := filepath.Abs(filepath.Join(stateRoot, directory))
	if err != nil {
		return false
	}
	path, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	base, path = filepath.Clean(base), filepath.Clean(path)
	if resolved, e := filepath.EvalSymlinks(base); e == nil {
		base = resolved
	}
	if resolved, e := filepath.EvalSymlinks(path); e == nil {
		path = resolved
	}
	if runtime.GOOS == "windows" {
		base, path = strings.ToLower(base), strings.ToLower(path)
	}
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readArtifact(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if runtime.GOOS == "windows" && (errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))) {
		time.Sleep(50 * time.Millisecond)
		return os.ReadFile(path)
	}
	return data, err
}
