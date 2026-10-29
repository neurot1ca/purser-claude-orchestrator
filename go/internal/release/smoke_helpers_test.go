package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func fixtureExecutable(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "main.go")
	out := filepath.Join(filepath.Dir(src), "fixture")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	program := `package main
import ("fmt"; "io"; "os")
func main() { if os.Getenv("FIXTURE_FAIL") != "" { fmt.Fprintln(os.Stderr,"fixture failure"); os.Exit(2) }; if len(os.Args)>1 && os.Args[1]=="mcp" { if os.Getenv("FIXTURE_BAD")=="init" { fmt.Println("{}") } else { fmt.Println("{\"id\":1,\"result\":{\"serverInfo\":{\"name\":\"codex-exec-mcp\"}}}"); if os.Getenv("FIXTURE_BAD")!="ping" { fmt.Println("{\"id\":2,\"result\":{}}") } }; return }; _,_=io.Copy(io.Discard,os.Stdin) }
`
	if err := os.WriteFile(src, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", out, src)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture executable: %v: %s", err, b)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(out, 0755); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
