package enforce

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestShellEditTargets(t *testing.T) {
	tests := []struct {
		name, tool, command string
		targets             []string
		trivial             bool
	}{
		{"sed one line", "Bash", `sed -i '134s/\x08/\\b/g' stop_flow.go`, nil, true},
		{"sed write", "Bash", `sed -i 's/a/b/g' stop_flow.go`, []string{"stop_flow.go"}, false},
		{"sed long regexp option", "Bash", `sed --regexp-extended 's/a/b/' stop_flow.go`, nil, false},
		{"sed long posix option", "Bash", `sed --posix 's/a/b/' stop_flow.go`, nil, false},
		{"sed long in-place option", "Bash", `sed --in-place=~ 's/a/b/' stop_flow.go`, []string{"stop_flow.go"}, false},
		{"perl include option", "Bash", `perl -Ilib -e 'print 1' stop_flow.go`, nil, false},
		{"perl module option", "Bash", `perl -MFile::Spec -e 'print 1' stop_flow.go`, nil, false},
		{"perl lowercase module option", "Bash", `perl -mX -e 'print 1' stop_flow.go`, nil, false},
		{"perl backup in place", "Bash", `perl -pi.bak -e 's/a/b/' stop_flow.go`, []string{"stop_flow.go"}, false},
		{"perl lpi in place", "Bash", `perl -lpi -e 's/a/b/' stop_flow.go`, []string{"stop_flow.go"}, false},
		{"unknown cd variable drops relative", "Bash", `cd "$x"; sed -i 's/a/b/g' stop_flow.go`, nil, false},
		{"unknown cd dash drops relative", "Bash", `cd -; sed -i 's/a/b/g' stop_flow.go`, nil, false},
		{"unknown bare cd drops relative", "Bash", `cd; sed -i 's/a/b/g' stop_flow.go`, nil, false},
		{"unknown parenthesized cd drops relative", "Bash", `(cd x; sed -i 's/a/b/g' stop_flow.go`, nil, false},
		{"unknown pushd drops relative", "Bash", `pushd "$x"; sed -i 's/a/b/g' stop_flow.go`, nil, false},
		{"unknown Set-Location drops relative", "PowerShell", `Set-Location $x; Set-Content -Path a.go -Value x`, nil, false},
		{"absolute cd restores relative", "Bash", `cd /tmp; sed -i 's/a/b/g' stop_flow.go`, []string{filepath.Join(string(filepath.Separator), "tmp", "stop_flow.go")}, false},
		{"comment segment ignored", "Bash", `# sed -i 's/a/b/g' stop_flow.go`, nil, false},
		{"multiple line seds are not trivial", "Bash", `sed -i '134s/a/b/' one.go && sed -i '135s/c/d/' two.go`, []string{"one.go", "two.go"}, false},
		{"python heredoc in cd", "Bash", "cd go/internal/release && python3 - <<'EOF'\nopen('x.go','w').write('x')\nEOF", []string{filepath.Join("go", "internal", "release", "x.go")}, false},
		{"python variable and keyword arguments", "Bash", "python3 - <<'EOF'\np='stop_flow.go'\ns=open(p,encoding='utf-8').read()\nopen(p,'w',encoding='utf-8',newline='').write(s)\nEOF", []string{"stop_flow.go"}, false},
		{"heredoc marker before shell operators", "Bash", "python3 - <<'EOF' 2>/dev/null || echo nopython\nopen('heredoc.go','w').write('x')\nEOF", []string{"heredoc.go"}, false},
		{"python fs variable write", "Bash", `python3 -c "p='fs.go'; fs.writeFileSync(p, 'x')"`, []string{"fs.go"}, false},
		{"python Path variable write", "Bash", `python3 -c "p = Path('path.go'); p.write_text('x'); p.write_bytes(b'x')"`, []string{"path.go"}, false},
		{"read only sed", "Bash", `cd go/internal && sed -n 75,100p review/readiness.go`, nil, false},
		{"grep", "Bash", `grep -c $'\r' host.go`, nil, false},
		{"git redirect", "Bash", `git show HEAD:./stop.go > /dev/null`, nil, false},
		{"test redirect", "Bash", `go test ./... 2>&1 | tail > .tmp/out.txt`, nil, false},
		{"tee after pipeline", "Bash", `go test ./... | tee result.go`, []string{"result.go"}, false},
		{"quoted redirect", "Bash", `echo "a > b.go"`, nil, false},
		{"heredoc cat", "Bash", "cat > a.go <<'EOF'\ncontents\nEOF", []string{"a.go"}, false},
		{"powershell set content", "PowerShell", `Set-Content -Path a.go -Value x`, []string{"a.go"}, false},
		{"powershell set location case insensitive", "PowerShell", `sEt-LoCaTiOn sub; Set-Content -Path a.go -Value x`, []string{filepath.Join("sub", "a.go")}, false},
		{"powershell sl literal", "PowerShell", `sl sub; Set-Content -Path a.go -Value x`, []string{filepath.Join("sub", "a.go")}, false},
		{"powershell pushd literal", "PowerShell", `pushd sub; Set-Content -Path a.go -Value x`, []string{filepath.Join("sub", "a.go")}, false},
		{"powershell replace pipeline", "PowerShell", `(Get-Content a.go) -replace 'a','b' | Set-Content a.go`, []string{"a.go"}, false},
		{"powershell Out-File pipeline variable", "PowerShell", `$x | Out-File -FilePath a.go`, []string{"a.go"}, false},
		{"powershell get content", "PowerShell", `Get-Content a.go`, nil, false},
		{"powershell io", "PowerShell", `[IO.File]::WriteAllText('a.go', 'x')`, []string{"a.go"}, false},
		{"powershell redirect", "PowerShell", `Write-Output x > a.go`, []string{"a.go"}, false},
		{"descriptor redirect", "Bash", `ls -la 2>/dev/null`, nil, false},
		{"perl combined in-place", "Bash", `perl -pi -e 's/a/b/' file.go`, []string{"file.go"}, false},
		{"quoted redirect path", "Bash", `cat > 'a file.go'`, []string{"a file.go"}, false},
		{"arithmetic comparison", "Bash", `echo $((1 > 2))`, nil, false},
		{"conditional comparison", "Bash", `[[ a > b.go ]]`, nil, false},
		{"read interpreter", "Bash", `python3 -c "print(open('a.go').read())"`, nil, false},
		{"encoding argument is not mode", "Bash", `python3 -c "open('a.go', encoding='w').read()"`, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, trivial := shellEditTargets(tt.tool, tt.command)
			if !reflect.DeepEqual(got, tt.targets) || trivial != tt.trivial {
				t.Fatalf("shellEditTargets() = %#v, %v; want %#v, %v", got, trivial, tt.targets, tt.trivial)
			}
		})
	}
}

func TestShellPreEditPanicFailsOpen(t *testing.T) {
	old := shellAssignment
	shellAssignment = nil
	defer func() { shellAssignment = old }()
	event := map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": t.TempDir(), "tool_input": map[string]any{"command": `python3 -c "p='a.go'; open(p,'w').write('x')"`}}
	response, err := Pre(event, t.TempDir())
	if response != nil || err != nil {
		t.Fatalf("panic must fail open: response=%v error=%v", response, err)
	}
}
