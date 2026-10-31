package maintenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/contextio"
	"purser-hook/internal/maintenance/errcheck"
)

func TestUncheckedErrorSection(t *testing.T) {
	result := errcheck.Result{Status: "ok"}
	var want strings.Builder
	for i := 1; i <= 41; i++ {
		result.Findings = append(result.Findings, errcheck.Finding{File: "a.go", Line: i, Detail: "f.Close"})
		fmt.Fprintf(&want, "errcheck a.go:%d f.Close\n", i)
	}
	path := filepath.Join(maintenanceScratch(t), "reports", "cleanup", "errcheck-run.txt")
	section, version := uncheckedErrorSection(result, path)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want.String() {
		t.Fatalf("incomplete findings file: %s, %v", data, err)
	}
	if !strings.Contains(section, "count=41") || !strings.Contains(version, "file="+path) || strings.Contains(section, "f.Close") || !strings.Contains(section, "evidence `errcheck(builtin)`") {
		t.Fatalf("incorrect candidate summary: %s", section)
	}
	mixed := errcheck.Result{Status: "errcheck=ok\nineffassign=failed:analyze\nunused=ok; skipped_asm_packages=2; skipped_cgo_packages=1\n", Findings: []errcheck.Finding{{Check: "unused", File: "dead.go", Line: 1, Detail: "func dead"}}}
	section, version = uncheckedErrorSection(mixed, path)
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "unused dead.go:1 func dead\n" || !strings.Contains(version, "unused(builtin): ok; skipped_asm_packages=2; skipped_cgo_packages=1; count=1") || !strings.Contains(section, "ineffassign(builtin): failed:analyze") {
		t.Fatalf("check failure affected siblings: %s %s %v", section, data, err)
	}

	for _, status := range []string{"skipped:not-go", "failed:load/type-check"} {
		section, _ := uncheckedErrorSection(errcheck.Result{Status: status}, path)
		if !strings.Contains(section, status) || !strings.Contains(section, "(unavailable)") {
			t.Fatalf("incorrect unavailable summary: %s", section)
		}
	}
}

func TestUncheckedErrorWriteFailure(t *testing.T) {
	blocked := filepath.Join(maintenanceScratch(t), "blocked")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	section, version := uncheckedErrorSection(errcheck.Result{Status: "ok"}, filepath.Join(blocked, "errcheck.txt"))
	if !strings.Contains(section, "failed:write") || !strings.Contains(version, "(unavailable)") {
		t.Fatalf("write failure not recorded: %s, %s", section, version)
	}
}

func TestReportPromptBytes(t *testing.T) {
	repo := "<REPO>"
	cleanup, err := BuildReportPrompt(repo, "cleanup", 90)
	if err != nil {
		t.Fatal(err)
	}
	wantCleanup := "Produce a cleanup report for the repository at <REPO>.\n" +
		"This is a strictly read-only inspection. Do not edit files, create repository files,\n" +
		"install tools, or run package managers. Detect languages from `git ls-files -co --exclude-standard`\n" +
		"extensions, go.mod, package.json, Cargo.toml, pyproject.toml/setup.cfg, and shebangs.\n" +
		"Use only locally resolved, already-installed tools; never\n" +
		"download tools, dependencies, or toolchains (including npx fetches, `go run\n" +
		"pkg@version`, or `cargo install`). Report only evidence-backed candidates.\n" +
		"For Go use deadcode, else staticcheck -checks U1000; TS/JS use project-local knip\n" +
		"or ts-prune; Rust use compiler dead_code warnings (`cargo check --offline` only if\n" +
		"it needs no download); Python use vulture. Other languages use an existing configured\n" +
		"analyzer. If none is usable, use search evidence and state its limits for that language.\n" +
		"Include a compact per-language scope table: language, file count, tool+version or\n" +
		"search-only/not-scanned, command, and limitation. For each item include exact tool\n" +
		"output or search evidence, repository path and symbol, and checks against entry points,\n" +
		"hooks, and dynamic/reflection use before calling it unused. Include commands/searches\n" +
		"and distinguish uncertain candidates from confirmed facts.\n" +
		"For each finding include why removing it benefits the repository or reduces a risk,\n" +
		"in one short sentence of at most 80 characters.\n" +
		"Order findings from most to least important. Every finding must include severity (high,\n" +
		"medium, or low), meaning its impact if left, separately from confidence.\n" +
		"If user-initiated work starts, it has priority: do not delay it, cancel it, or\n" +
		"continue consuming resources once it needs the machine. End your report with this\n" +
		"machine-readable list, using one object per finding and an empty list for no findings:\n" +
		"```purser-findings-json\n" +
		"[{\"path_line\":\"relative/path.py:12\",\"symbol\":\"name\",\"why\":\"short reason to remove it\",\"evidence\":\"short evidence\",\"severity\":\"high|medium|low\",\"confidence\":\"high|medium|low\"}]\n```\n"
	if cleanup != wantCleanup {
		t.Fatalf("cleanup prompt mismatch\n got: %q\nwant: %q", cleanup, wantCleanup)
	}
	testGaps, err := BuildReportPrompt(repo, "test-gaps", 90)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Change test files only", "safety, authority boundaries, persistence/recovery", "ordering rationale", "There is no fixed gap count", "Coverage is measured by the host", "line_coverage_pct is null"} {
		if !strings.Contains(testGaps, required) {
			t.Errorf("test-gaps prompt missing %q", required)
		}
	}
	if strings.Contains(testGaps, "strictly read-only") || strings.Contains(testGaps, "worker-estimated percentages") == false {
		t.Fatalf("test-gaps prompt retained read-only or worker-owned coverage: %s", testGaps)
	}
}

func TestReportPromptsRequireNoDownloadsAndLanguageScope(t *testing.T) {
	cleanup, _ := BuildReportPrompt("<REPO>", "cleanup", 90)
	if !strings.Contains(cleanup, "never\ndownload tools, dependencies") || !strings.Contains(cleanup, "per-language scope table") {
		t.Fatal("cleanup prompt contract changed")
	}
	testGaps, _ := BuildReportPrompt("<REPO>", "test-gaps", 90)
	if !strings.Contains(testGaps, "Change test files only") || !strings.Contains(testGaps, "line_coverage_pct is null") {
		t.Fatal("test-gaps writing/coverage contract missing")
	}
}

func TestFindingsParsingOracle(t *testing.T) {
	cases := []struct {
		name, body string
		count      int
		coverage   any
	}{
		{"worker-coverage-ignored", "```purser-findings-json\n{\"findings\":[{\"path_line\":\"a.py:1\",\"symbol\":\"a\",\"evidence\":\"e\",\"confidence\":\"low\"},{\"path_line\":\"b.py:2\",\"symbol\":\"b\",\"evidence\":\"e\",\"confidence\":\"high\"}],\"line_coverage_pct\":73.5}\n```", 2, nil},
		{"empty", "```purser-findings-json\n[]\n```", 0, nil},
		{"malformed", "```purser-findings-json\n{bad}\n```", -1, nil},
		{"partial-coverage", "```purser-findings-json\n{\"findings\":[{\"path_line\":\"a.py:1\",\"symbol\":\"a\",\"evidence\":\"e\",\"confidence\":\"high\"}]}\n```", -1, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseFindings(tc.body)
			if tc.count < 0 {
				if got != nil {
					t.Fatalf("findings = %#v, want unknown", got)
				}
			} else if got == nil || len(got) != tc.count {
				t.Fatalf("findings = %#v", got)
			}
			if !reflect.DeepEqual(LineCoverage(tc.body), tc.coverage) {
				t.Fatalf("coverage = %#v, want %#v", LineCoverage(tc.body), tc.coverage)
			}
			if tc.name == "well-formed" && got[0]["confidence"] != "high" {
				t.Fatalf("confidence order = %#v", got)
			}
		})
	}
	body := "```purser-findings-json\n{\"findings\":[{\"path_line\":\"a.py:1\",\"symbol\":\"a\",\"evidence\":\"e\",\"confidence\":\"low\"},{\"path_line\":\"b.py:2\",\"symbol\":\"b\",\"evidence\":\"e\",\"confidence\":\"high\"}],\"line_coverage_pct\":80}\n```"
	if got := ParseFindings(body, "test-gaps"); got[0]["symbol"] != "b" {
		t.Fatalf("test-gaps order = %#v", got)
	}
	if got := ParseFindings(body, "cleanup"); got[0]["symbol"] != "b" {
		t.Fatalf("cleanup confidence order = %#v", got)
	}
}

func TestFindingsSortBySeverityThenConfidence(t *testing.T) {
	body := "```purser-findings-json\n[" +
		`{"path_line":"a:1","symbol":"medium-low","evidence":"e","severity":"medium","confidence":"low"},` +
		`{"path_line":"b:1","symbol":"high-low","evidence":"e","severity":"high","confidence":"low"},` +
		`{"path_line":"c:1","symbol":"medium-high","evidence":"e","severity":"medium","confidence":"high"},` +
		`{"path_line":"d:1","symbol":"high-high","evidence":"e","severity":"high","confidence":"high"},` +
		`{"path_line":"e:1","symbol":"high-high-later","evidence":"e","severity":"high","confidence":"high"}]` + "\n```"
	got := ParseFindings(body)
	want := []string{"high-high", "high-high-later", "high-low", "medium-high", "medium-low"}
	if len(got) != len(want) {
		t.Fatalf("findings=%#v", got)
	}
	for i, symbol := range want {
		if got[i]["symbol"] != symbol {
			t.Fatalf("finding[%d]=%v, want %s", i, got[i]["symbol"], symbol)
		}
	}
}

func TestFindingSeverityFallsBackToConfidence(t *testing.T) {
	body := "```purser-findings-json\n[{\"path_line\":\"a:1\",\"symbol\":\"a\",\"evidence\":\"e\",\"confidence\":\"medium\"}]\n```"
	got := ParseFindings(body)
	if len(got) != 1 || got[0]["severity"] != "medium" {
		t.Fatalf("findings=%#v, want confidence-derived severity medium", got)
	}
}

func TestFindingsWhyMustBeString(t *testing.T) {
	body := "```purser-findings-json\n[{\"path_line\":\"a.py:1\",\"symbol\":\"a\",\"evidence\":\"e\",\"confidence\":\"high\",\"why\":7}]\n```"
	if got := ParseFindings(body); got != nil {
		t.Fatalf("non-string why parsed as %#v", got)
	}
}

func TestRunAndPersistReportSuccessAndFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			caseDir := maintenanceScratch(t)
			codexHome := filepath.Join(caseDir, "codex-home")
			if err := os.MkdirAll(codexHome, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CODEX_HOME", codexHome)
			state, repo := filepath.Join(caseDir, "state"), filepath.Join(caseDir, "repo")
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(repo, 0700); err != nil {
				t.Fatal(err)
			}
			writeCoverageSuite(t, repo)
			if !failed {
				profileDir := filepath.Join(repo, ".orchestrator")
				if err := os.MkdirAll(profileDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(profileDir, "profile.toml"), []byte("schema_version = 1\n[maintenance]\ncoverage_target_pct = 80\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			commitMaintenanceFixture(t, repo)
			result := map[string]any{"state": "SUCCEEDED", "content": "Body.\n```purser-findings-json\n{\"findings\":[{\"path_line\":\"a.py:1\",\"symbol\":\"f\",\"evidence\":\"source\",\"confidence\":\"low\"}],\"line_coverage_pct\":80}\n```", "usage": map[string]any{"total_tokens": 21}}
			if failed {
				result = map[string]any{"state": "FAILED", "errorCode": "CODEX_FAILED", "message": "worker failed"}
			}
			fake := &fakeJobs{jobID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", results: []map[string]any{result}}
			if !failed {
				fake.onStart = func(state string) { stageMaintenancePatch(t, state, repo, fake) }
				fake.finalize = reviewMaintenancePatch
			}
			runner := Runner{Jobs: fake, Sleep: func(_ time.Duration) {}}
			path, err := RunAndPersistReport(runner, state, repo, "test-gaps")
			key := filepath.Join(state, "projects", contextProjectKey(repo), "maintenance", "reports", "test-gaps")
			if failed {
				if err == nil || path != "" {
					t.Fatalf("path=%q error=%v", path, err)
				}
				matches, _ := filepath.Glob(filepath.Join(key, "failures", "*.json"))
				if len(matches) != 1 {
					t.Fatalf("failure files: %v", matches)
				}
				var record map[string]any
				data, _ := os.ReadFile(matches[0])
				if json.Unmarshal(data, &record) != nil || record["state"] != "FAILED" || record["job_id"] != fake.jobID {
					t.Fatalf("failure record: %s", data)
				}
				reports, _ := filepath.Glob(filepath.Join(key, "*.md"))
				if len(reports) != 0 {
					t.Fatalf("failure created report: %v", reports)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			prompt, _ := BuildReportPrompt(repo, "test-gaps", 80)
			if !strings.HasPrefix(fake.started["prompt"].(string), prompt) {
				t.Fatal("runner prompt differs from builder")
			}
			if !strings.Contains(prompt, "coverage target is 80%") {
				t.Fatalf("override missing from prompt: %s", prompt)
			}
			content, _ := os.ReadFile(path)
			if !strings.Contains(string(content), "- Findings: 1") || !strings.Contains(string(content), `"total_tokens":21`) {
				t.Fatalf("report content: %s", content)
			}
			lastRun := filepath.Join(state, "projects", contextProjectKey(repo), "maintenance", "reports", "test-gaps", "last-run.json")
			if _, err := os.Stat(lastRun); err != nil {
				t.Fatal(err)
			}
			notice, _ := os.ReadFile(strings.TrimSuffix(path, ".md") + ".notice.json")
			if !strings.Contains(string(notice), `"line_coverage_pct":null`) || !strings.Contains(string(notice), `"coverage_target_pct":80`) {
				t.Fatalf("notice: %s", notice)
			}
			ready, err := os.ReadFile(filepath.Join(key, "ready.json"))
			if err != nil {
				t.Fatal(err)
			}
			var proposal map[string]any
			if json.Unmarshal(ready, &proposal) != nil {
				t.Fatal("invalid ready evidence")
			}
			after := proposal["after"].(map[string]any)["Counts"].(map[string]any)
			if after["Covered"] != float64(3) || after["Executable"] != float64(4) || proposal["new_test_count"] != float64(1) {
				t.Fatalf("worker percentage replaced host evidence: %s", ready)
			}
			events, _ := filepath.Glob(filepath.Join(state, "telemetry", "events", "*", "session_unknown.jsonl"))
			if len(events) != 1 {
				t.Fatalf("completion telemetry files: %v", events)
			}
			lines, _ := os.ReadFile(events[0])
			var event map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(string(lines))), &event) != nil || event["event_type"] != "MAINTENANCE_REPORT_FINISHED" {
				t.Fatalf("completion telemetry: %s", lines)
			}
			payload := event["payload"].(map[string]any)
			if payload["findings_high_count"] != float64(0) || payload["line_coverage_pct"] != nil || payload["quota_change_active_window_pct"] != nil {
				t.Fatalf("completion telemetry payload: %#v", payload)
			}
		})
	}
}

func TestTestGapCoverageUnknownPersistsLimitations(t *testing.T) {
	repo, state := maintenancePatchRepo(t), maintenanceScratch(t)
	fake := &fakeJobs{results: []map[string]any{{"state": "FAILED", "errorCode": "COVERAGE_UNKNOWN", "message": "maintenance baseline coverage is unknown"}}}
	runner := Runner{Jobs: fake, CoderAgent: "codex", CoderModel: "fixture"}
	path, err := RunAndPersistReport(runner, state, repo, "test-gaps")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "Coverage unknown: unavailable tooling, unsupported suite, incomplete scope, or incompatible measurements; test writing skipped.") {
		t.Fatalf("missing coverage limitations: %s, error=%v", body, err)
	}
	reports := filepath.Join(MaintenanceDirectory(state, repo), "reports", "test-gaps")
	if failures, err := filepath.Glob(filepath.Join(reports, "failures", "*.json")); err != nil || len(failures) != 0 {
		t.Fatalf("coverage unknown recorded a failure: %v, error=%v", failures, err)
	}
	if _, err := os.Stat(filepath.Join(reports, "last-run.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(MaintenanceDirectory(state, repo), "reports", "test-gaps", "ready.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host coverage failure left a ready proposal: %v", err)
	}
}

func writeCoverageSuite(t *testing.T, root string) {
	t.Helper()
	for name, content := range map[string]string{
		"go.mod":        "module coveragefixture\n\ngo 1.20\n",
		"go.work":       "go 1.20\n\nuse .\n",
		"shape.go":      "package shape\n\nfunc Choose(ok bool) int {\n if ok { return 1 }; return 0\n}\n",
		"shape_test.go": "package shape\nimport \"testing\"\nfunc TestChoose(t *testing.T) { if Choose(true) != 1 { t.Fatal(\"unexpected choice\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func contextProjectKey(repository string) string {
	return contextio.ProjectKey(repository)
}

func maintenanceScratch(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "..", ".tmp", "maintenance-report-tests")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path, err := os.MkdirTemp(root, "case-")
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}
