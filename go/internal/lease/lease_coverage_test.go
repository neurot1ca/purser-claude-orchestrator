package lease

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScopesOverlap(t *testing.T) {
	cases := []struct {
		name        string
		left, right []any
		want        bool
	}{
		{"same", []any{"src/a.go"}, []any{"src/a.go"}, true},
		{"directory covers file", []any{"src"}, []any{"src/a.go"}, true},
		{"disjoint siblings", []any{"src/a"}, []any{"src/b"}, false},
		{"prefix requires slash boundary", []any{"src"}, []any{"src-old/a"}, false},
		{"trailing slash is literal", []any{"src/"}, []any{"src/a.go"}, false},
		{"case follows platform", []any{"Src"}, []any{"src/a"}, runtime.GOOS == "windows"},
		{"backslash is not repo separator", []any{`src\\nested`}, []any{"src/nested/file"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScopesOverlap(tc.left, tc.right); got != tc.want {
				t.Fatalf("ScopesOverlap(%v, %v) = %v, want %v", tc.left, tc.right, got, tc.want)
			}
		})
	}
}

func TestSameScope(t *testing.T) {
	for _, tc := range []struct {
		left  []any
		right []string
		want  bool
	}{{[]any{"a", "b"}, []string{"a", "b"}, true}, {[]any{"b", "a"}, []string{"a", "b"}, false}, {[]any{"a"}, []string{"a", "a"}, false}, {[]any{1}, []string{"1"}, false}} {
		if got := sameScope(tc.left, tc.right); got != tc.want {
			t.Errorf("sameScope(%v, %v) = %v, want %v", tc.left, tc.right, got, tc.want)
		}
	}
}

func TestHotAdmissionConflict(t *testing.T) {
	base := func(state string, job any, acquired float64) map[string]any {
		return map[string]any{"owners": map[string]any{"other": map[string]any{"state": state, "job_id": job, "scope": []any{"src/"}, "acquired_at": acquired}},
			"conflicts": []any{map[string]any{"path": "src/file.go", "recorded_at": 100.0}}}
	}
	for _, tc := range []struct {
		name  string
		value map[string]any
		job   string
		now   float64
		want  bool
	}{
		{"live foreign conflict", base("ACTIVE", "foreign", 100), "mine", 100, true},
		{"own job ignored", base("ACTIVE", "mine", 100), "mine", 100, false},
		{"released owner ignored", base("RELEASED", "foreign", 100), "mine", 100, false},
		{"expired conflict ignored", base("ACTIVE", "foreign", 100), "mine", 100 + conflictRetentionSeconds + 1, false},
		{"queued foreign conflicts", base("QUEUED", "foreign", 100), "mine", 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := New(t.TempDir()).hotAdmissionConflict(tc.value, []string{"src"}, tc.job, tc.now); got != tc.want {
				t.Fatalf("hotAdmissionConflict() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtendRetention(t *testing.T) {
	store := New(t.TempDir())
	if got := store.Acquire("active", "task", []string{"src"}, AcquireOptions{JobID: ptr("job")}, 10); got.Code != "LEASE_ACQUIRED" {
		t.Fatal(got)
	}
	value, err := store.read()
	if err != nil {
		t.Fatal(err)
	}
	value["owners"].(map[string]any)["active"].(map[string]any)["retention_after"] = 25.0
	if err := store.write(value, 10); err != nil {
		t.Fatal(err)
	}
	if got := store.ExtendRetention("active", 30, 20); got != (Result{"OK", "LEASE_EXTENDED"}) {
		t.Fatal(got)
	}
	if got := store.ExtendRetention("active", 25, 21); got != (Result{"OK", "LEASE_EXTENDED"}) {
		t.Fatal(got)
	}
	value, err = store.read()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := pythonFloat(value["owners"].(map[string]any)["active"].(map[string]any)["retention_after"]); !ok || got != 30 {
		t.Fatalf("retention_after = %v, want max horizon 30", value["owners"].(map[string]any)["active"].(map[string]any)["retention_after"])
	}
	if got := store.ExtendRetention("missing", 50, 22); got != (Result{"UNKNOWN", "LEASE_OWNER_MISSING"}) {
		t.Fatal(got)
	}
	if got := store.ExtendRetention("active", 50, 22); got != (Result{"OK", "LEASE_EXTENDED"}) {
		t.Fatal(got)
	}
}

func TestOverlappingOwnersUsesConcurrentForeignOwners(t *testing.T) {
	store := New(t.TempDir())
	owners := map[string]any{
		"self":    map[string]any{"state": "ACTIVE", "task_id": "self", "scope": []any{"a"}, "acquired_at": 10.0},
		"overlap": map[string]any{"state": "RELEASED", "task_id": "foreign", "scope": []any{"b"}, "acquired_at": 12.0, "released_at": 18.0},
		"before":  map[string]any{"state": "RELEASED", "task_id": "before", "scope": []any{"c"}, "acquired_at": 1.0, "released_at": 9.0},
		"after":   map[string]any{"state": "ACTIVE", "task_id": "after", "scope": []any{"d"}, "acquired_at": 20.0},
	}
	if err := store.write(map[string]any{"version": 1, "owners": owners, ownerOrderKey: []string{"self", "overlap", "before", "after"}}, 20); err != nil {
		t.Fatal(err)
	}
	got, err := store.OverlappingOwners("self", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["task_id"] != "foreign" {
		t.Fatalf("overlapping owners = %#v", got)
	}
	if _, err := store.OverlappingOwners("missing", 20); err == nil {
		t.Fatal("missing own owner should fail")
	}
}

func TestReleaseJobScopeLease(t *testing.T) {
	root := t.TempDir()
	project, owner, job := strings.Repeat("a", 16), strings.Repeat("b", 64), strings.Repeat("c", 32)
	if got := New(filepath.Join(root, "projects", project)).Acquire(owner, "task", []string{"src"}, AcquireOptions{}, 10); got.Code != "LEASE_ACQUIRED" {
		t.Fatal(got)
	}
	jobs := filepath.Join(root, "async_jobs")
	if err := os.MkdirAll(jobs, 0700); err != nil {
		t.Fatal(err)
	}
	writeIndex := func(jobID string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jobs, jobID+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeIndex(job, map[string]string{"jobId": job, "project_key": project, "call_key": owner})
	if !ReleaseJobScopeLease(root, job) {
		t.Fatal("valid indexed lease should release")
	}
	state, err := New(filepath.Join(root, "projects", project)).OwnerState(owner, 20)
	if err != nil || state != "RELEASED" {
		t.Fatalf("owner state = %q, err=%v", state, err)
	}
	missing := strings.Repeat("d", 32)
	writeIndex(missing, map[string]string{"jobId": missing, "project_key": project, "call_key": strings.Repeat("e", 64)})
	if !ReleaseJobScopeLease(root, missing) {
		t.Fatal("missing lease owner is an idempotent successful release")
	}
	if ReleaseJobScopeLease(root, "bad") {
		t.Fatal("invalid job id should fail")
	}
}

func ptr(value string) *string { return &value }
