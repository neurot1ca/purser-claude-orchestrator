package optimize

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"purser-hook/internal/contextio"
	"purser-hook/internal/learning"
)

const (
	telemetryMaxTasks = 50
	telemetryMaxJobs  = 200
)

type telemetryJobView struct {
	ID         string `json:"job_id"`
	Task       string `json:"task"`
	Status     string `json:"reviewer_status"`
	Verdict    string `json:"verdict"`
	Provider   string `json:"provider"`
	Model      string `json:"requested_model"`
	Served     string `json:"served_model_attestation"`
	Valid      *int64 `json:"valid"`
	FalseCount *int64 `json:"false"`
	at         float64
}

type telemetryTaskView struct {
	Task      string `json:"task"`
	Jobs      int    `json:"jobs"`
	SendBacks *int64 `json:"send_backs"`
	at        float64
}

type telemetryCoverage struct {
	RawJobsRead     int      `json:"raw_jobs_read"`
	TasksRead       int      `json:"task_snapshots_read"`
	ArchivedRecords int      `json:"archived_records_read"`
	DuplicateJobs   int      `json:"duplicate_archived_jobs"`
	Ambiguous       int      `json:"ambiguous_identities"`
	Legacy          int      `json:"unsupported_legacy_facts"`
	Changed         int      `json:"changed_during_read"`
	Unreadable      int      `json:"unreadable_inputs"`
	EvictedRecords  *int64   `json:"evicted_records,omitempty"`
	CoveredSources  *int64   `json:"archive_covered_sources,omitempty"`
	ArchiveCutoff   string   `json:"archive_cutoff,omitempty"`
	Problems        []string `json:"problems,omitempty"`
	OmittedTasks    int      `json:"omitted_tasks"`
	OmittedJobs     int      `json:"omitted_jobs"`
}

type telemetryHealthView struct {
	Scope       string `json:"scope"`
	Component   string `json:"component"`
	Status      string `json:"status"`
	Consecutive *int64 `json:"consecutive_failures"`
	Lifetime    *int64 `json:"lifetime_failures"`
	Historical  *int64 `json:"historical_unattributed_failures,omitempty"`
	LastError   string `json:"last_error"`
	CheckedAt   string `json:"last_checked_at"`
}

type telemetryReport struct {
	ProjectKey string                `json:"project_key"`
	Tasks      []telemetryTaskView   `json:"tasks"`
	Jobs       []telemetryJobView    `json:"jobs"`
	Coverage   telemetryCoverage     `json:"coverage"`
	Health     []telemetryHealthView `json:"health"`
}

// RunTelemetry prints a read-only reviewer telemetry report for one project.
func RunTelemetry(out io.Writer, stateRoot, repo string, asJSON bool) error {
	report := collectTelemetry(stateRoot, contextio.ProjectKey(repo))
	if asJSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "%s\n", data)
		return err
	}
	_, err := io.WriteString(out, renderTelemetry(report))
	return err
}

func collectTelemetry(stateRoot, project string) telemetryReport {
	report := telemetryReport{ProjectKey: project, Tasks: []telemetryTaskView{}, Jobs: []telemetryJobView{}, Health: []telemetryHealthView{}}
	cov := &report.Coverage
	jobs := map[string]*telemetryJobView{}
	tasks := map[string]*telemetryTaskView{}
	sendBacks := map[string]*int64{}
	taskAt := map[string]float64{}

	task := func(name string) *telemetryTaskView {
		if tasks[name] == nil {
			tasks[name] = &telemetryTaskView{Task: name}
		}
		return tasks[name]
	}
	add := func(view telemetryJobView) {
		if view.Status == "" {
			cov.Legacy++
		}
		jobs[view.ID] = &view
		t := task(view.Task)
		if view.at > t.at {
			t.at = view.at
		}
		if view.at > taskAt[view.Task] {
			taskAt[view.Task] = view.at
		}
	}

	jobsDir := filepath.Join(stateRoot, "codex_exec_mcp", "jobs")
	entries, err := os.ReadDir(jobsDir)
	if err != nil && !os.IsNotExist(err) {
		cov.Problems = append(cov.Problems, "raw job directory unavailable")
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(jobsDir, entry.Name())
		job, err := readTelemetryJSON(path)
		if err != nil {
			recordReadError(cov, err, "job")
			continue
		}
		cov.RawJobsRead++
		stem := strings.TrimSuffix(entry.Name(), ".json")
		facts := learning.ReadJobFacts(stateRoot, job)
		projectKey, _ := facts["project_key"].(string)
		id, _ := facts["source_job_id"].(string)
		if id == "" || id != stem || projectKey == "" {
			cov.Ambiguous++
		}
		if id == "" {
			id = stem
		}
		if projectKey != project {
			continue
		}
		taskID, _ := facts["task_id"].(string)
		if taskID == "" {
			taskID = "unknown"
		}
		view := telemetryJobView{
			ID:       id,
			Task:     taskID,
			Status:   stringValue(facts["reviewer_execution_status"]),
			Verdict:  stringValue(facts["reviewer_verdict"]),
			Provider: stringValue(facts["reviewer_provider"]),
			Model:    stringValue(facts["reviewer_requested_model"]),
			Served:   stringValue(facts["reviewer_served_model_attestation"]),
			at:       numberValue(job["finishedAt"]),
		}
		view.Valid = countValue(facts["reviewer1_valid_count"])
		if view.Valid == nil {
			view.Valid = countValue(job["reviewer1Valid"])
		}
		view.FalseCount = countValue(facts["reviewer1_false_count"])
		if view.FalseCount == nil {
			view.FalseCount = countValue(job["reviewer1False"])
		}
		add(view)
	}

	summary, err := readTelemetryJSON(filepath.Join(stateRoot, "telemetry", learning.SummaryName))
	if err == nil {
		_, sourcesOK := summary["sources"].([]any)
		_, recordsOK := summary["records"].([]any)
		_, cohortsOK := summary["cohorts"].(map[string]any)
		if numberValue(summary["schema_version"]) != 1 || !sourcesOK || !recordsOK || !cohortsOK {
			err = fmt.Errorf("invalid learning summary schema")
		}
	}
	if err != nil && !os.IsNotExist(err) {
		recordReadError(cov, err, "learning summary")
		summary = nil
	}
	records, _ := summary["records"].([]any)
	cov.ArchivedRecords = len(records)
	if archive, ok := summary["archive"].(map[string]any); ok {
		cov.EvictedRecords = countValue(archive["evicted_records"])
		cov.CoveredSources = countValue(archive["covered_sources"])
		for _, key := range []string{"cutoff", "cutoff_at", "archive_cutoff"} {
			if value := stringValue(archive[key]); value != "" {
				cov.ArchiveCutoff = value
			}
		}
	}
	for _, raw := range records {
		record, ok := raw.(map[string]any)
		if !ok {
			cov.Unreadable++
			continue
		}
		projectKey := stringValue(record["project_key"])
		id := stringValue(record["id"])
		if projectKey != project {
			continue
		}
		if id == "" {
			cov.Ambiguous++
			continue
		}
		if _, exists := jobs[id]; exists {
			cov.DuplicateJobs++
			continue
		}
		taskID := stringValue(record["task"])
		if taskID == "" {
			taskID = "unknown"
		}
		view := telemetryJobView{
			ID:       id,
			Task:     taskID,
			Status:   stringValue(record["review_execution_status"]),
			Verdict:  stringValue(record["review_verdict"]),
			Provider: stringValue(record["review_provider"]),
			Model:    stringValue(record["review_model"]),
			Served:   stringValue(record["review_served_model_attestation"]),
			at:       numberValue(record["at"]),
		}
		view.Valid = archivedCount(record, "reviewer1_valid")
		view.FalseCount = archivedCount(record, "reviewer1_false")
		add(view)
		if value, ok := intValue(record["send_backs"]); ok {
			if sendBacks[taskID] == nil || view.at >= taskAt[taskID] {
				v := value
				sendBacks[taskID] = &v
			}
		}
	}

	taskDir := filepath.Join(stateRoot, "projects", project, "telemetry", "tasks")
	entries, err = os.ReadDir(taskDir)
	if err != nil && !os.IsNotExist(err) {
		cov.Problems = append(cov.Problems, "task snapshot directory unavailable")
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		snapshot, err := readTelemetryJSON(filepath.Join(taskDir, entry.Name()))
		if err != nil {
			recordReadError(cov, err, "task")
			continue
		}
		cov.TasksRead++
		name := stringValue(snapshot["task_id"])
		if name == "" {
			name = strings.TrimSuffix(entry.Name(), ".json")
		}
		if key := stringValue(snapshot["project_key"]); key != "" && key != project {
			cov.Ambiguous++
			continue
		}
		acceptance, _ := snapshot["claude_acceptance"].(map[string]any)
		if value, ok := intValue(acceptance["send_backs"]); ok {
			sendBacks[name] = &value
		}
		task(name)
	}

	for _, view := range jobs {
		task(view.Task).Jobs++
	}
	for name, value := range sendBacks {
		task(name).SendBacks = value
	}
	for name, value := range taskAt {
		if tasks[name].at < value {
			tasks[name].at = value
		}
	}
	for _, view := range tasks {
		report.Tasks = append(report.Tasks, *view)
	}
	sort.Slice(report.Tasks, func(i, j int) bool {
		if report.Tasks[i].at != report.Tasks[j].at {
			return report.Tasks[i].at > report.Tasks[j].at
		}
		return report.Tasks[i].Task < report.Tasks[j].Task
	})
	if len(report.Tasks) > telemetryMaxTasks {
		cov.OmittedTasks = len(report.Tasks) - telemetryMaxTasks
		report.Tasks = report.Tasks[:telemetryMaxTasks]
	}
	for _, view := range jobs {
		report.Jobs = append(report.Jobs, *view)
	}
	sort.Slice(report.Jobs, func(i, j int) bool {
		if report.Jobs[i].at != report.Jobs[j].at {
			return report.Jobs[i].at > report.Jobs[j].at
		}
		return report.Jobs[i].ID < report.Jobs[j].ID
	})
	if len(report.Jobs) > telemetryMaxJobs {
		cov.OmittedJobs = len(report.Jobs) - telemetryMaxJobs
		report.Jobs = report.Jobs[:telemetryMaxJobs]
	}

	report.Health = append(report.Health, readTelemetryHealth(stateRoot, project, "project", cov)...)
	report.Health = append(report.Health, readTelemetryHealth(stateRoot, "", "root", cov)...)
	sort.Slice(report.Health, func(i, j int) bool {
		if report.Health[i].Scope != report.Health[j].Scope {
			return report.Health[i].Scope < report.Health[j].Scope
		}
		return report.Health[i].Component < report.Health[j].Component
	})
	sort.Strings(cov.Problems)
	return report
}

func readTelemetryJSON(path string) (map[string]any, error) {
	data, err := readTelemetryFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("invalid JSON trailer")
	}
	return value, nil
}

func readTelemetryFile(path string) ([]byte, error) {
	tries := 1
	if runtime.GOOS == "windows" {
		tries = 4
	}
	var data []byte
	var err error
	for i := 0; i < tries; i++ {
		data, err = os.ReadFile(path)
		if err == nil || os.IsNotExist(err) {
			return data, err
		}
		if i+1 < tries {
			time.Sleep(time.Duration(10*(i+1)) * time.Millisecond)
		}
	}
	return data, err
}

func recordReadError(cov *telemetryCoverage, err error, kind string) {
	if os.IsNotExist(err) {
		cov.Changed++
		return
	}
	cov.Unreadable++
	cov.Problems = append(cov.Problems, "unreadable "+kind+" input")
}

func readTelemetryHealth(stateRoot, project, scope string, cov *telemetryCoverage) []telemetryHealthView {
	dir := filepath.Join(stateRoot, "telemetry")
	if project != "" {
		dir = filepath.Join(stateRoot, "projects", project, "telemetry")
	}
	data, err := readTelemetryFile(filepath.Join(dir, "telemetry_health.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			cov.Problems = append(cov.Problems, "unreadable "+scope+" health")
		}
		return nil
	}
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil {
		cov.Problems = append(cov.Problems, "malformed "+scope+" health")
		return nil
	}
	components, _ := doc["components"].(map[string]any)
	out := []telemetryHealthView{}
	if len(components) == 0 {
		return []telemetryHealthView{{
			Scope: scope, Component: "legacy (unattributed)", Status: stringValue(doc["status"]),
			Historical: countValue(doc["failure_count"]), LastError: stringValue(doc["last_error_code"]),
			CheckedAt: stringValue(doc["updated_at"]),
		}}
	}
	for name, raw := range components {
		c, _ := raw.(map[string]any)
		view := telemetryHealthView{Scope: scope, Component: name, Status: stringValue(c["status"]), CheckedAt: stringValue(c["last_checked_at"])}
		view.Consecutive = countValue(c["consecutive_failures"])
		view.Lifetime = countValue(c["lifetime_failures"])
		view.LastError = stringValue(c["last_error_code"])
		if failure, ok := c["last_failure"].(map[string]any); ok && view.LastError == "" {
			view.LastError = stringValue(failure["reason"])
		}
		out = append(out, view)
	}
	return out
}

func archivedCount(record map[string]any, key string) *int64 {
	present, _ := record[key+"_present"].(bool)
	if !present {
		return nil
	}
	value, ok := intValue(record[key])
	if !ok {
		return nil
	}
	return &value
}

func countValue(value any) *int64 {
	if n, ok := intValue(value); ok {
		return &n
	}
	return nil
}

func intValue(value any) (int64, bool) {
	switch n := value.(type) {
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case float64:
		if n >= 0 && n <= 1<<53 && n == float64(int64(n)) {
			return int64(n), true
		}
	case int:
		if n >= 0 {
			return int64(n), true
		}
	case int64:
		if n >= 0 {
			return n, true
		}
	}
	return 0, false
}

func numberValue(value any) float64 {
	switch n := value.(type) {
	case json.Number:
		f, _ := n.Float64()
		return f
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		if v {
			return "true"
		}
		return "false"
	}
	return ""
}

func optionalCount(value *int64) string {
	if value == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *value)
}

func optionalNumber(value *int64) string {
	if value == nil {
		return "not recorded"
	}
	return fmt.Sprintf("%d", *value)
}

func cell(value string) string {
	if value == "" {
		return "—"
	}
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func renderTelemetry(report telemetryReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Purser telemetry\n\nProject key: %s\n\n", cell(report.ProjectKey))
	b.WriteString("## Tasks\n\n| Task | Jobs | Send-backs |\n| --- | --- | --- |\n")
	for _, task := range report.Tasks {
		fmt.Fprintf(&b, "| %s | %d | %s |\n", cell(task.Task), task.Jobs, optionalCount(task.SendBacks))
	}
	if len(report.Tasks) == 0 {
		b.WriteString("| — | 0 | — |\n")
	}
	if report.Coverage.OmittedTasks > 0 {
		fmt.Fprintf(&b, "\n_%d older tasks omitted._\n", report.Coverage.OmittedTasks)
	}
	b.WriteString("\n## Jobs\n\n| Job | Task | Reviewer status | Verdict | Provider | Model | Served | Valid | False |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, job := range report.Jobs {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", shortID(job.ID), cell(job.Task), cell(job.Status), cell(job.Verdict), cell(job.Provider), cell(job.Model), cell(job.Served), optionalCount(job.Valid), optionalCount(job.FalseCount))
	}
	if len(report.Jobs) == 0 {
		b.WriteString("| — | — | — | — | — | — | — | — | — |\n")
	}
	if report.Coverage.OmittedJobs > 0 {
		fmt.Fprintf(&b, "\n_%d older jobs omitted._\n", report.Coverage.OmittedJobs)
	}
	cov := report.Coverage
	b.WriteString("\n## Coverage\n\n")
	fmt.Fprintf(&b, "- Raw jobs read: %d\n- Task snapshots read: %d\n- Archived records read: %d\n", cov.RawJobsRead, cov.TasksRead, cov.ArchivedRecords)
	fmt.Fprintf(&b, "- Duplicate archived jobs skipped: %d\n", cov.DuplicateJobs)
	fmt.Fprintf(&b, "- Archive: covered sources %s; evicted records %s; cutoff %s\n", optionalNumber(cov.CoveredSources), optionalNumber(cov.EvictedRecords), first(cov.ArchiveCutoff, "not recorded"))
	fmt.Fprintf(&b, "- Ambiguous identities: %d\n- Unsupported legacy facts: %d\n", cov.Ambiguous, cov.Legacy)
	status := "COMPLETE"
	if cov.Unreadable > 0 || cov.Changed > 0 || len(cov.Problems) > 0 {
		status = "PARTIAL"
	}
	fmt.Fprintf(&b, "- Input status: %s", status)
	if cov.Unreadable > 0 {
		fmt.Fprintf(&b, "; %d unreadable input(s)", cov.Unreadable)
	}
	if cov.Changed > 0 {
		fmt.Fprintf(&b, "; %d file(s) changed during read", cov.Changed)
	}
	b.WriteString("\n")
	for _, problem := range cov.Problems {
		fmt.Fprintf(&b, "- Note: %s\n", problem)
	}
	b.WriteString("\n## Component health\n\n| Scope | Component | Status | Consecutive | Lifetime | Historical unattributed | Last error | Checked |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, health := range report.Health {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n", health.Scope, cell(health.Component), cell(health.Status), optionalCount(health.Consecutive), optionalCount(health.Lifetime), optionalCount(health.Historical), cell(health.LastError), cell(health.CheckedAt))
	}
	if len(report.Health) == 0 {
		b.WriteString("| — | — | no health records | — | — | — | — | — |\n")
	}
	return b.String()
}
