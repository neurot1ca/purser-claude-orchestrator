package testevidence

import "testing"

func TestLimitRetainsUnknown(t *testing.T) {
	runs := []map[string]any{{"command": "go test ./unknown", "exit_code": nil}}
	for i := 0; i < 6; i++ {
		runs = append(runs, map[string]any{"exit_code": 0})
	}
	evidence := Limit(runs)
	if len(evidence) != 6 || evidence[0]["command"] != "go test ./unknown" || evidence[0]["exit_code"] != nil {
		t.Fatalf("unknown status disappeared at the evidence cap: %#v", evidence)
	}
}

func TestExitCodeReflectsTestRunner(t *testing.T) {
	for _, tc := range []struct {
		command string
		code    any
		want    any
	}{
		{`go test ./...`, float64(0), 0},
		{`gofmt -w a.go; go test ./x`, float64(0), 0},
		{`pwsh -Command "gofmt -w a.go; go test ./x"`, float64(0), 0},
		{`gofmt -w a.go && go test ./x`, float64(0), 0},
		{`command exit 0; go test ./x`, float64(0), nil},
		{`builtin exit 0; go test ./x`, float64(0), nil},
		{`eval exit; go test ./x`, float64(0), nil},
		{`. ./x.sh; go test ./x`, float64(0), nil},
		{`echo hi; go test ./x`, float64(0), nil},
		{`echo setup #; go test ./x`, float64(0), nil},
		{`pwsh -Command "echo a #; go test ./x"`, float64(0), nil},
		{`exit 0; go test ./x`, float64(0), nil},
		{`pwsh -Command "Exit 0; go test ./x"`, float64(0), nil},
		{`exec true; go test ./x`, float64(0), nil},
		{`return; go test ./x`, float64(0), nil},
		{`break; go test ./x`, float64(0), nil},
		{`continue; go test ./x`, float64(0), nil},
		{`throw; go test ./x`, float64(0), nil},
		{`logout; go test ./x`, float64(0), nil},
		{`go test ./a; go test ./b`, float64(0), nil},
		{`go test ./a && go test ./b`, float64(0), 0},
		{`pwsh -Command 'gofmt -w a.go b.go; if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }; go test ./go/internal/testevidence'`, float64(0), 0},
		{"pwsh -Command 'gofmt -w a.go; IF\t(\n$lastexitcode -NE 0\n) {\nEXIT\t$lastexitcode\n}; go test ./x'", float64(0), 0},
		{`go test ./a; if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }; go test ./b`, float64(0), nil},
		{`go test ./a && if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE } && go test ./b`, float64(0), nil},
		{`gofmt -w a.go; if ($LASTEXITCODE -ne 0) { exit 1 }; go test ./x`, float64(0), nil},
		{`pwsh -Command "go test ./a && go test ./b"`, float64(0), 0},
		{`"C:\Program Files\PowerShell\7\pwsh.exe" -Command "go test ./internal/jobs -run 'TestApply(A|B)"'$'"' -count=1 -timeout=90s"`, float64(0), 0},
		{`"C:\Program Files\PowerShell\7\pwsh.exe" -Command "go test ./internal/jobs -run 'TestApply(A|B)"'$'"' -count=1 -timeout=90s"`, float64(1), 1},
		{`cmd /c go test ./...`, float64(0), 0},
		{`/bin/sh -c 'go test ./...'`, float64(0), 0},
		{`bash -lc 'go test ./...'`, float64(0), 0},
		{`go test ./... -run 'TestOne|TestTwo'`, float64(0), 0},
		{`"C:\Program Files\PowerShell\7\pwsh.exe" -Command "go test ./...; echo done"`, float64(0), nil},
		{`cmd /c "go test ./... || echo done"`, float64(0), nil},
		{`sh -c 'go test ./... | cat'`, float64(0), nil},
		{"bash -lc 'go test ./...\necho done'", float64(0), nil},
		{`pwsh -Command "go test ./...; if ($LASTEXITCODE -eq 0) { echo done }"`, float64(0), nil},
		{`pwsh -Command "go test ./... $LASTEXITCODE"`, float64(0), nil},
		{`go test ./... && echo done`, float64(0), nil},
		{`go test ./x; gofmt -w a.go`, float64(0), nil},
		{`gofmt -l . | go test ./x`, float64(0), nil},
		{`false || go test ./x`, float64(0), nil},
		{`sh -c 'go test ./...' ; echo done`, float64(0), nil},
		{`sh -c 'go test ./...' ; echo done`, float64(1), 1},
		{`sh -c 'go test -run "Test\"" ; echo done #'`, float64(0), nil},
		{`go test ./...; echo done`, float64(1), 1},
		{`cmd /c "go test ./... || echo done"`, float64(1), 1},
		{`sh -c 'go test ./... | cat'`, float64(1), 1},
		{`go test ./...`, nil, nil},
		{`go test ./...`, "0", nil},
		{`go test ./...`, float64(0.5), nil},
	} {
		t.Run(tc.command, func(t *testing.T) {
			target, got := ExitCode(tc.command, tc.code)
			if target == "" || got != tc.want {
				t.Fatalf("ExitCode(%q, %v) = %q, %v; want recognized runner, %v", tc.command, tc.code, target, got, tc.want)
			}
		})
	}
	command := `"C:\Program Files\PowerShell\7\pwsh.exe" -Command "go test ./internal/jobs -run 'TestApply(A|B)"'$'"' -count=1 -timeout=90s"`
	if target := Target(command); target != "go test ./internal/jobs -run TestApply(A|B)$ -count=1 -timeout=90s" {
		t.Fatalf("Target(%q) = %q", command, target)
	}
	if target, code := ExitCode(`echo "note; go test ./..."`, float64(0)); target != "" || code != nil {
		t.Fatalf("quoted test text became evidence: %q, %v", target, code)
	}
}
