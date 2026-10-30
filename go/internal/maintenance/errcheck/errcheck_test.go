package errcheck

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
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
			want := []Finding{{File: "good.go", Line: 10, Check: "errcheck", Detail: "f.Close"}, {File: "good.go", Line: 13, Check: "errcheck", Detail: "<-ch"}}
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
		{File: "b.go", Line: 2, Check: "errcheck", Detail: "Close"},
		{File: "b.go", Line: 3, Check: "errcheck", Detail: "writer.WriteString"},
		{File: "z.go", Line: 1, Check: "errcheck", Detail: "os.RemoveAll"},
		{File: "a.go", Line: 1, Check: "errcheck", Detail: "Read"},
		{File: "a.go", Line: 2, Check: "ineffassign", Detail: "x"},
		{File: "a.go", Line: 3, Check: "unused", Detail: "func dead"},
		{File: "a_test.go", Line: 1, Check: "errcheck", Detail: "f.Sync"},
		{File: "testdata/example.go", Line: 1, Check: "errcheck", Detail: "os.MkdirAll"},
		{File: "a_test.go", Line: 2, Check: "errcheck", Detail: "Read"},
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
	want := []Finding{{File: "good.go", Line: 10, Check: "errcheck", Detail: "f.Close"}, {File: "nested/nested.go", Line: 5, Check: "errcheck", Detail: "f.Close"}, {File: "good.go", Line: 13, Check: "errcheck", Detail: "<-ch"}}
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

func TestFlowAndUnused(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("testdata", "flow"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), root)
	if err != nil || result.Status != "ok" {
		t.Fatalf("%+v: %v", result, err)
	}
	var got []string
	for _, f := range result.Findings {
		got = append(got, fmt.Sprintf("%s:%d:%s", f.Check, f.Line, f.Detail))
	}
	want := []string{"ineffassign:4:x", "ineffassign:46:x", "ineffassign:93:x", "ineffassign:112:x", "ineffassign:149:q", "ineffassign:160:x", "unused:50:func dead", "unused:52:const deadConst", "unused:104:type deadType", "unused:106:var deadVar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v; want %v", got, want)
	}
}

func TestIsolatedFailure(t *testing.T) {
	statuses := map[string]string{}
	runCheck("ineffassign", statuses, func() { panic("analysis failed") })
	ran := false
	runCheck("unused", statuses, func() { ran = true })
	r := Result{Status: "errcheck=ok\nineffassign=" + statuses["ineffassign"] + "\nunused=ok\n"}
	if !ran || !strings.HasPrefix(r.CheckStatus("ineffassign"), "failed:") || r.CheckStatus("unused") != "ok" {
		t.Fatalf("failure leaked: %+v", r)
	}
}

func TestUnusedSkipsAssemblyAndCgo(t *testing.T) {
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	fs := token.NewFileSet()
	node, err := parser.ParseFile(fs, filepath.Join(root, "native.go"), "package native; var assemblyOnly = 1", 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{}
	if _, err := conf.Check("native", fs, []*ast.File{node}, info); err != nil {
		t.Fatal(err)
	}
	for _, cgo := range []bool{false, true} {
		u := newUnused()
		p := pkg{Dir: root, ImportPath: "native"}
		if u.skip(p) {
			t.Fatal("ordinary package skipped")
		}
		if cgo {
			p.CgoFiles = []string{"native.go"}
		} else {
			p.SFiles = []string{"native.s"}
		}
		u.scan(root, fs, []*ast.File{node}, info, u.skip(p))
		p.ImportPath += " [native.test]"
		if !u.skip(p) || len(u.skippedAsm)+len(u.skippedCgo) != 1 {
			t.Fatal("package variants not deduplicated")
		}
		found := map[string]Finding{}
		u.finish(found)
		if len(found) != 0 {
			t.Fatalf("native reference reported unused: %+v", found)
		}
	}
}
