package errcheck

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"purser-hook/internal/proc"
)

func TestRun(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	for _, fixture := range []string{"good", "bad"} {
		t.Run(fixture, func(t *testing.T) {
			root, err := filepath.Abs(filepath.Join("testdata", fixture))
			if err != nil {
				t.Fatal(err)
			}
			result, err := Run(context.Background(), root)
			if fixture == "bad" {
				if err == nil || !strings.HasPrefix(result.Status, "failed:") || len(result.Findings) != 0 {
					t.Fatalf("incomplete scan: %+v, %v", result, err)
				}
				return
			}
			want := []Finding{{File: "good.go", Line: 10, Callee: "f.Close"}, {File: "good.go", Line: 13, Callee: "<-ch"}}
			if err != nil || result.Status != "ok" || !reflect.DeepEqual(result.Findings, want) {
				t.Fatalf("got %+v, %v; want %+v", result, err, want)
			}
		})
	}
}

func TestDiscoveryFailure(t *testing.T) {
	for _, tool := range []string{"go", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	root, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(root, "missing-git-dir"))
	result, err := Run(context.Background(), root)
	if err == nil || !strings.HasPrefix(result.Status, "failed:detect modules:") || len(result.Findings) != 0 {
		t.Fatalf("discovery failure hidden: %+v, %v", result, err)
	}
}

func TestImportanceOrder(t *testing.T) {
	want := []Finding{
		{File: "b.go", Line: 2, Callee: "Close"},
		{File: "b.go", Line: 3, Callee: "writer.WriteString"},
		{File: "z.go", Line: 1, Callee: "os.RemoveAll"},
		{File: "a.go", Line: 1, Callee: "Read"},
		{File: "a_test.go", Line: 1, Callee: "f.Sync"},
		{File: "testdata/example.go", Line: 1, Callee: "os.MkdirAll"},
		{File: "a_test.go", Line: 2, Callee: "Read"},
	}
	got := append([]Finding(nil), want...)
	for i, j := 0, len(got)-1; i < j; i, j = i+1, j-1 {
		got[i], got[j] = got[j], got[i]
	}
	sortFindings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %+v, want %+v", got, want)
	}
}

func TestNestedModules(t *testing.T) {
	for _, tool := range []string{"go", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	scratch := filepath.Join("..", "..", "..", "..", ".tmp", "errcheck-tests")
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(scratch, "nested-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(root, os.DirFS(filepath.Join("testdata", "good"))); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(root, "nested"), os.DirFS(filepath.Join("testdata", "nested"))); err != nil {
		t.Fatal(err)
	}
	if out, err := proc.Hide(exec.Command("git", "init", "--quiet", root)).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s, %v", out, err)
	}
	result, err := Run(context.Background(), root)
	want := []Finding{{File: "good.go", Line: 10, Callee: "f.Close"}, {File: "nested/nested.go", Line: 5, Callee: "f.Close"}, {File: "good.go", Line: 13, Callee: "<-ch"}}
	if err != nil || result.Status != "ok" || !reflect.DeepEqual(result.Findings, want) {
		t.Fatalf("nested scan: %+v, %v; want %+v", result, err, want)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "nested.go"), []byte("package nested\nfunc broken() { missing() }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = Run(context.Background(), root)
	if err == nil || !strings.HasPrefix(result.Status, "failed:") || !strings.Contains(result.Status, "module nested:") || len(result.Findings) != 0 {
		t.Fatalf("partial scan accepted: %+v, %v", result, err)
	}
}
