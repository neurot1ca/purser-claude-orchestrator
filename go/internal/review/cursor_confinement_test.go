package review

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"purser-hook/internal/testutil"
)

const fakeCursorSource = `package main
import("encoding/json"; "fmt"; "os"; "os/exec"; "runtime"; "time"; "io"; "strings")
func main() {
 if len(os.Args)>1 && os.Args[1]=="child" { time.Sleep(2*time.Second); os.WriteFile(os.Getenv("CURSOR_CHILD_EFFECT"),[]byte("alive"),0600); return }
 if len(os.Args)==2 && os.Args[1]=="--version" { fmt.Println(os.Getenv("CURSOR_VERSION")); return }
 if len(os.Args)==3 && os.Args[1]=="mcp" && os.Args[2]=="list" {
  switch os.Getenv("CURSOR_MODE") {
  case "mcp-list-error": os.Exit(7)
  case "mcp-list-empty": return
  case "mcp-list-timeout": c:=exec.Command(os.Args[0],"child"); c.Start(); for {time.Sleep(time.Minute)}
  case "mcp-project": os.WriteFile(".cursor/mcp.json",[]byte("{}"),0600)
  case "mcp-listed","mcp-missing","mcp-denied","mcp-success": fmt.Println("demo: not loaded (needs approval)"); return
  }
  fmt.Println("No MCP servers configured (expected in .cursor/mcp.json or ~/.cursor/mcp.json)"); return
 }
 io.ReadAll(os.Stdin)
 f,_:=os.OpenFile(os.Getenv("CURSOR_CALLS"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); if f!=nil { f.WriteString("probe\n"); f.Close() }
 if !strings.Contains(strings.Join(os.Args," "),"--output-format stream-json") { os.Exit(9) }
 for _,arg:=range os.Args[1:] { if arg=="--mode" { os.Exit(10) } }
 switch os.Getenv("CURSOR_MODE") {
 case "write": os.WriteFile("canary.txt",[]byte("changed"),0600)
 case "new": os.WriteFile("new-file.txt",[]byte("changed"),0600)
 case "shell":
  var c *exec.Cmd
  if runtime.GOOS=="windows" { c=exec.Command("cmd.exe","/d","/c","echo changed>shell-file.txt") } else { c=exec.Command("sh","-c","echo changed > shell-file.txt") }; c.Run()
 case "timeout":
  c:=exec.Command(os.Args[0],"child"); c.Start(); for { time.Sleep(time.Minute) }
 case "error": os.Exit(1)
 case "auth": fmt.Fprintln(os.Stderr,"Authentication required"); os.Exit(1)
 case "model": fmt.Fprintln(os.Stderr,"Model unavailable on Free"); os.Exit(1)
 case "ambiguous": fmt.Println("{}"); return
 }
 enc:=json.NewEncoder(os.Stdout)
 mode:=os.Getenv("CURSOR_MODE")
 // Real schema lookup is not an MCP server call.
 for _,subtype:=range []string{"started","completed"} {
  call:=map[string]any{"args":map[string]any{"server":"cursor","toolName":"WebFetch"}}
  if subtype=="completed" {call["result"]=map[string]any{"success":map[string]any{"content":"WebFetch schema"}}}
  enc.Encode(map[string]any{"type":"tool_call","subtype":subtype,"call_id":"schema-lookup","tool_call":map[string]any{"getMcpToolsToolCall":call}})
 }
 if strings.HasPrefix(mode,"mcp-inherited-") {
  for _,subtype:=range []string{"started","completed"} {
   call:=map[string]any{"args":map[string]any{"server":"plugin-purser-codex-main"}}
   if subtype=="completed" {call["result"]=map[string]any{"success":map[string]any{"content":` + "`" + `{"mode":"namespace","namespace":"plugin-purser-codex-main","namespaceStatus":"ready","tools":[{"name":"codex_result"}]}` + "`" + `}}}
   enc.Encode(map[string]any{"type":"tool_call","subtype":subtype,"call_id":"inherited-schema","tool_call":map[string]any{"getMcpToolsToolCall":call}})
  }
 }
 if mode!="obey" {
  cwd,_:=os.Getwd()
  for i,tool:=range []string{"editToolCall","editToolCall","shellToolCall","webFetchToolCall","mcpToolCall"} {
   args:=map[string]any{"path":cwd+string(os.PathSeparator)+"canary.txt","streamContent":"changed\n"}
   if i==1 {args["path"]=cwd+string(os.PathSeparator)+"new-file.txt"}
   if i==2 {command:="printf changed > shell-file.txt"; if runtime.GOOS=="windows" {command="echo changed>shell-file.txt"}; args=map[string]any{"command":command}}
   if i==3 {args=map[string]any{"url":"https://example.com"}; if mode=="no-fetch" {continue}}
   if i==4 {
    args=map[string]any{"providerIdentifier":"demo","toolName":"read","name":"demo_read","args":map[string]any{}}
    if strings.HasPrefix(mode,"mcp-inherited-") {args=map[string]any{"name":"plugin-purser-codex-main-codex_result","args":map[string]any{},"providerIdentifier":"plugin-purser-codex-main","toolName":"codex_result","serverIdentifier":"plugin-purser-codex-main"}}
    if mode!="mcp-denied" && mode!="mcp-success" && mode!="mcp-inherited-rejected" && mode!="mcp-inherited-success" {continue}
   }
   if mode=="partial" && i==2 {continue}
   for _,subtype:=range []string{"started","completed"} {
    if mode=="unpaired" && subtype=="started" {continue}
    call:=map[string]any{"args":args}
    if subtype=="completed" {
     result:=map[string]any{"writePermissionDenied":map[string]any{"path":args["path"],"error":"Write permission denied: "+fmt.Sprint(args["path"])}}
     if i==2 { result=map[string]any{"permissionDenied":map[string]any{"command":args["command"],"workingDirectory":cwd,"error":"Command blocked by permissions configuration","isReadonly":false}} }
     if i==3 {result=map[string]any{"rejected":map[string]any{"reason":"Blocked by permissions configuration"}}; if mode=="fetch-user-rejected" {result=map[string]any{"rejected":map[string]any{"reason":"User Rejected"}}}}
     if i==3 && mode=="fetch-permission-denied" {result=map[string]any{"permissionDenied":map[string]any{"error":"permission denied"}}}
     if i==3 && mode=="fetch-success" {result=map[string]any{"success":map[string]any{"url":"https://example.com","markdown":"Example Domain"}}}
     if i==2 && mode=="fetch-success" {result=map[string]any{"error":map[string]any{"error":"shell error before the successful fetch"}}}
     if i==4 {
      result=map[string]any{"permissionDenied":map[string]any{"error":"Blocked by permissions configuration","isReadonly":false}}
      if mode=="mcp-inherited-rejected" {result=map[string]any{"rejected":map[string]any{"reason":"User rejected MCP: plugin-purser-codex-main-codex_result","isReadonly":false}}}
      if mode=="mcp-success" || mode=="mcp-inherited-success" {result=map[string]any{"success":map[string]any{}}}
     }
     if mode=="shell-spawn" && i==2 {result=map[string]any{"spawnError":map[string]any{"error":"Can't find Bash"}}}
     if mode=="generic-error" {result=map[string]any{"error":map[string]any{"error":"permission denied"}}}
     call["result"]=result
    }
    event:=map[string]any{"type":"tool_call","subtype":subtype,"call_id":fmt.Sprint(i),"tool_call":map[string]any{tool:call,"hookAdditionalContexts":[]any{},"toolCallId":fmt.Sprint(i),"startedAtMs":"1793648696221"}}
    if mode=="assistant-claim" {event=map[string]any{"type":"assistant","message":event}}
    enc.Encode(event)
   }
  }
 }
 enc.Encode(map[string]any{"type":"result","subtype":"success","is_error":false,"result":"all tools denied"})
}`

type changedCursorConfinement struct{ Adapter }

func (a changedCursorConfinement) ConfinementConfig(command []string) (string, []byte) {
	name, data := a.Adapter.ConfinementConfig(command)
	return name, append(data, '\n')
}

func TestCursorConfinement(t *testing.T) {
	root := reviewScratchDir(t)
	t.Setenv("PURSER_STATE_DIR", filepath.Join(root, "state"))
	source := filepath.Join(root, "main.go")
	if err := os.WriteFile(source, []byte(fakeCursorSource), 0600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "cursor-agent")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	build := testutil.Hide(exec.Command("go", "build", "-o", executable, source))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake cursor: %s: %v", output, err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	config := map[string]any{"reviewer1_executable": executable, "reviewer1_model_requested": "claude-sonnet-4-5"}
	t.Setenv("CURSOR_VERSION", "fake-v1")
	t.Setenv("CURSOR_CALLS", filepath.Join(root, "calls"))
	t.Run("shims", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("Windows shims")
		}
		t.Setenv("CURSOR_MODE", "mcp-denied")
		for ext, script := range map[string]string{
			".cmd": "@echo off\r\n\"" + executable + "\" %*\r\nexit /b %errorlevel%\r\n",
			".ps1": "& '" + strings.ReplaceAll(executable, "'", "''") + "' @args\nexit $LASTEXITCODE\n",
		} {
			path := filepath.Join(root, "cursor-agent"+ext)
			if err := os.WriteFile(path, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			shimConfig := map[string]any{"reviewer1_executable": path, "reviewer1_model_requested": "claude-sonnet-4-5"}
			if !cursorConfinementVerified(context.Background(), shimConfig) {
				t.Fatalf("%s shim confinement not verified", ext)
			}
		}
	})
	for mode, want := range map[string]string{
		"denied": "verified", "obey": "no_attempt", "write": "escape", "new": "escape", "shell": "escape",
		"timeout": "timeout", "error": "exit_1", "auth": "auth", "model": "model_unavailable", "ambiguous": "parse",
		"partial": "inconclusive", "unpaired": "inconclusive", "generic-error": "no_denial", "assistant-claim": "no_attempt",
		"shell-spawn": "shell_spawn",
		"no-fetch":    "inconclusive", "fetch-user-rejected": "verified", "fetch-permission-denied": "verified", "fetch-success": "escape", "mcp-missing": "mcp_inconclusive",
		"mcp-listed": "mcp_inconclusive", "mcp-denied": "verified", "mcp-success": "escape",
		"mcp-inherited-rejected": "verified", "mcp-inherited-success": "escape", "mcp-inherited-listed": "mcp_inconclusive",
		"mcp-list-error": "mcp_list_failed", "mcp-list-empty": "mcp_list_invalid", "mcp-list-timeout": "mcp_list_timeout", "mcp-project": "mcp_config",
	} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CURSOR_MODE", mode)
			effect := filepath.Join(root, "child-effect")
			t.Setenv("CURSOR_CHILD_EFFECT", effect)
			timeout := 10 * time.Second
			if mode == "timeout" || mode == "mcp-list-timeout" {
				timeout = time.Second
			}
			checks := map[string]string{}
			if got := probeCursorConfinement(context.Background(), config, timeout, checks); got != want {
				t.Fatalf("reason = %s, want %s", got, want)
			}
			if mode == "denied" || mode == "mcp-denied" || mode == "mcp-inherited-rejected" {
				for _, name := range []string{"edit", "create", "shell", "fetch"} {
					if checks[name] != "denied" {
						t.Fatalf("%s sub-check = %s", name, checks[name])
					}
				}
				mcpWant := "no_servers"
				if mode != "denied" {
					mcpWant = "denied"
				}
				if checks["mcp"] != mcpWant {
					t.Fatalf("MCP sub-check = %s", checks["mcp"])
				}
			}
			if mode == "timeout" || mode == "mcp-list-timeout" {
				time.Sleep(2200 * time.Millisecond)
				if _, err := os.Stat(effect); !os.IsNotExist(err) {
					t.Fatalf("timeout descendant survived: %v", err)
				}
			}
		})
	}
	t.Run("cache", func(t *testing.T) {
		t.Setenv("CURSOR_MODE", "mcp-denied")
		if err := os.Remove(os.Getenv("CURSOR_CALLS")); err != nil {
			t.Fatal(err)
		}
		check := func(want bool, calls int) {
			t.Helper()
			if got := cursorConfinementVerified(context.Background(), config); got != want {
				t.Fatalf("verified = %v, want %v", got, want)
			}
			data, _ := os.ReadFile(os.Getenv("CURSOR_CALLS"))
			if strings.Count(string(data), "probe\n") != calls {
				t.Fatalf("unexpected probe calls: %q, want %d", data, calls)
			}
		}
		check(true, 1)
		check(true, 1)
		file, err := os.OpenFile(executable, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write([]byte{0}) // PE/ELF trailers leave the fake's version unchanged.
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("mutate executable: %v, %v", writeErr, closeErr)
		}
		check(true, 2)
		// Both gates admit the cached verdict without the operator override.
		adapter := reviewerAdapters["cursor"]
		if _, err := adapter.BuildCommand("review", reviewSchema, config, CommandOptions{WorkingDirectory: root}); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CURSOR_VERSION", "fake-v2")
		check(true, 3)
		reviewerAdapters["cursor"] = changedCursorConfinement{adapter}
		t.Cleanup(func() { reviewerAdapters["cursor"] = adapter })
		check(true, 4)
		userPath := filepath.Join(home, ".cursor", "cli-config.json")
		if err := os.MkdirAll(filepath.Dir(userPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(userPath, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		check(true, 4) // Missing CLI config and an empty object have the same hash.
		if err := os.WriteFile(userPath, []byte(`{"permissions":{"allow":["Shell(**)"]}}`), 0600); err != nil {
			t.Fatal(err)
		}
		check(true, 5)
		check(true, 5)
		mcpPath := filepath.Join(home, ".cursor", "mcp.json")
		if err := os.WriteFile(mcpPath, nil, 0600); err != nil {
			t.Fatal(err)
		}
		check(true, 5) // Missing and empty MCP files have the same hash.
		if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"demo":{"command":"demo"}}}`), 0600); err != nil {
			t.Fatal(err)
		}
		check(true, 6)
		if err := os.Remove(mcpPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(mcpPath, 0700); err != nil {
			t.Fatal(err)
		}
		check(false, 6) // Unreadable MCP configuration cannot reuse a verdict.
		if err := os.Remove(mcpPath); err != nil {
			t.Fatal(err)
		}
		check(true, 6)
		t.Setenv("CURSOR_VERSION", "fake-v3")
		// Every inconclusive outcome retries, and can recover on the same key.
		calls := 6
		for _, mode := range []string{"mcp-list-error", "mcp-list-empty", "mcp-project"} {
			t.Setenv("CURSOR_MODE", mode)
			check(false, calls)
		}
		for _, mode := range []string{"obey", "error", "auth", "model", "ambiguous", "partial", "shell-spawn", "no-fetch", "mcp-missing", "mcp-inherited-listed"} {
			t.Setenv("CURSOR_MODE", mode)
			calls++
			check(false, calls)
		}
		t.Setenv("CURSOR_MODE", "timeout")
		t.Setenv("CURSOR_CHILD_EFFECT", filepath.Join(root, "cached-timeout-child-effect"))
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		reason := cursorConfinementStatus(ctx, config, nil)
		cancel()
		if reason != "timeout" {
			t.Fatalf("timeout reason = %s", reason)
		}
		calls++
		cachePattern := filepath.Join(reviewerDiagnosticStateRoot(), "cursor-confinement", "*.json")
		before, err := filepath.Glob(cachePattern)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("CURSOR_MODE", "denied")
		calls++
		check(true, calls)
		calls++
		check(true, calls) // No-server verdicts must re-probe on every selection.
		after, err := filepath.Glob(cachePattern)
		if err != nil || len(after) != len(before) {
			t.Fatalf("no_servers wrote a cache file: %v", err)
		}
		t.Setenv("CURSOR_MODE", "mcp-denied")
		calls++
		check(true, calls)
		after, err = filepath.Glob(cachePattern)
		if err != nil || len(after) != len(before)+1 {
			t.Fatalf("denied MCP verdict was not cached: %v", err)
		}
		check(true, calls)
		t.Setenv("CURSOR_VERSION", "fake-v4")
		t.Setenv("CURSOR_MODE", "write")
		calls++
		check(false, calls)
		t.Setenv("CURSOR_MODE", "mcp-denied")
		check(false, calls)
		paths, _ := filepath.Glob(filepath.Join(reviewerDiagnosticStateRoot(), "cursor-confinement", "*.json"))
		for _, path := range paths {
			if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		check(false, calls)
	})
}

func TestCursorExecutableIdentity(t *testing.T) {
	root := reviewScratchDir(t)
	path := filepath.Join(root, "cursor-agent")
	if err := os.WriteFile(path, []byte("binary-v1"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := cursorExecutableIdentity(path, "same-version")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("binary-v2"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := cursorExecutableIdentity(path, "same-version")
	if err != nil || first == second {
		t.Fatalf("executable mutation retained identity: %v", err)
	}
	if runtime.GOOS != "windows" {
		return
	}
	// Model the installed cmd -> ps1 -> versioned node/index launcher chain.
	shim := filepath.Join(root, "cursor-agent.cmd")
	script := filepath.Join(root, "cursor-agent.ps1")
	files := map[string]string{
		shim:   `powershell.exe -File "%SCRIPT_DIR%\cursor-agent.ps1"`,
		script: "& \"$scriptPath\\node.exe\" \"$scriptPath\\index.js\" $args\n& \"$nodePath\" \"$scriptPath\\versions\\$versionName\\index.js\" $args",
		filepath.Join(root, "versions", "same-version", "node.exe"): "node",
		filepath.Join(root, "versions", "same-version", "index.js"): "entrypoint",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := cursorExecutableIdentity(shim, "same-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{shim, script, filepath.Join(root, "versions", "same-version", "node.exe"), filepath.Join(root, "versions", "same-version", "index.js")} {
		data, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		after, err := cursorExecutableIdentity(shim, "same-version")
		if err != nil || before == after {
			t.Fatalf("target %s mutation retained identity: %v", target, err)
		}
		before = after
	}
	if err := os.Remove(filepath.Join(root, "versions", "same-version", "index.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := cursorExecutableIdentity(shim, "same-version"); err == nil {
		t.Fatal("missing launcher target was accepted")
	}
	if err := os.WriteFile(script, []byte("& $unresolvable @args"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := cursorExecutableIdentity(script, "same-version"); err == nil {
		t.Fatal("unresolvable shim was accepted")
	}
}

func TestLiveCursorConfinement(t *testing.T) {
	if os.Getenv("PURSER_LIVE_CURSOR") != "1" {
		t.Skip("PURSER_LIVE_CURSOR=1 required")
	}
	t.Setenv("PURSER_STATE_DIR", reviewScratchDir(t))
	config := map[string]any{"reviewer1_model_requested": "auto"}
	checks := map[string]string{}
	reason := cursorConfinementStatus(context.Background(), config, checks)
	for _, name := range []string{"edit", "create", "shell", "fetch", "mcp"} {
		t.Logf("Cursor confinement %s: %s", name, checks[name])
	}
	t.Logf("Cursor live confinement verified: %v; reason: %s", reason == "verified", reason)
	if reason != "verified" {
		t.Fatal("Cursor confinement remains unverified; reviewer unavailable")
	}
}

func TestCursorUnixLauncherIdentity(t *testing.T) {
	root := reviewScratchDir(t)
	dir := filepath.Join(root, "version")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "cursor-agent")
	files := map[string]string{
		launcher:                       "#!/bin/sh\nSCRIPT_DIR=\"$(cd \"$(dirname \"$0\")\" && pwd)\"\nexec \"$SCRIPT_DIR/node\" \"$SCRIPT_DIR/index.js\" \"$@\"\n",
		filepath.Join(dir, "node"):     "bundled-node",
		filepath.Join(dir, "index.js"): "entrypoint",
	}
	for path, text := range files {
		if err := os.WriteFile(path, []byte(text), 0700); err != nil {
			t.Fatal(err)
		}
	}
	entry := filepath.Join(root, "cursor-agent")
	if err := os.Symlink(launcher, entry); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		t.Log("Symlink unavailable on Windows; checking the resolved launcher directly")
		entry = launcher
	}
	before, err := cursorExecutableIdentity(entry, "same-version")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := cursorExecutableIdentity(launcher, "same-version")
	if err != nil || before != resolved {
		t.Fatalf("symlink identity differs: %v", err)
	}
	for _, name := range []string{"node", "index.js"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("changed-"+name), 0700); err != nil {
			t.Fatal(err)
		}
		after, err := cursorExecutableIdentity(entry, "same-version")
		if err != nil || after == before {
			t.Fatalf("%s mutation retained identity: %v", name, err)
		}
		before = after
	}
	if err := os.Remove(filepath.Join(dir, "index.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := cursorExecutableIdentity(entry, "same-version"); err == nil {
		t.Fatal("Missing Unix entrypoint accepted")
	}
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec node /unknown/entry.js \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := cursorExecutableIdentity(entry, "same-version"); err == nil {
		t.Fatal("Unresolvable Unix launcher accepted")
	}
}

func TestCursorUserConfigHash(t *testing.T) {
	home := reviewScratchDir(t)
	dir := filepath.Join(home, ".cursor")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cli-config.json")
	base := map[string]any{
		"permissions":  map[string]any{"allow": []string{"Shell(ls)"}},
		"approvalMode": "default", "sandbox": "enabled", "autoAcceptWebSearch": false,
		"mCpSettings":  map[string]any{"enabled": false},
		"privacyCache": map[string]any{"updatedAt": 1}, "model": "old-model", "authInfo": map[string]any{"account": "old-account"},
	}
	write := func(value any) {
		t.Helper()
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(base)
	want, err := cursorUserConfigHash(home)
	if err != nil {
		t.Fatal(err)
	}
	copyConfig := func() map[string]any {
		copy := map[string]any{}
		for key, value := range base {
			copy[key] = value
		}
		return copy
	}
	t.Run("run_state_churn", func(t *testing.T) {
		changed := copyConfig()
		changed["privacyCache"] = map[string]any{"updatedAt": 2}
		changed["model"] = "new-model"
		changed["authInfo"] = map[string]any{"account": "new-account"}
		write(changed)
		if got, err := cursorUserConfigHash(home); err != nil || got != want {
			t.Fatalf("run state changed security hash: %v", err)
		}
	})
	for key, value := range map[string]any{
		"permissions":  map[string]any{"allow": []string{"Shell(**)"}},
		"approvalMode": "always", "sandbox": "disabled", "autoAcceptWebSearch": true,
		"mCpSettings": map[string]any{"enabled": true},
	} {
		t.Run(key, func(t *testing.T) {
			changed := copyConfig()
			changed[key] = value
			write(changed)
			if got, err := cursorUserConfigHash(home); err != nil || got == want {
				t.Fatalf("%s change retained security hash: %v", key, err)
			}
		})
	}
	t.Run("missing_key", func(t *testing.T) {
		changed := copyConfig()
		delete(changed, "sandbox")
		write(changed)
		if got, err := cursorUserConfigHash(home); err != nil || got == want {
			t.Fatalf("missing key retained security hash: %v", err)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		for _, data := range []string{"invalid JSON", "[]", ""} {
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := cursorUserConfigHash(home); err == nil {
				t.Fatalf("invalid config %q was accepted", data)
			}
		}
	})
	t.Run("concurrent_truncation", func(t *testing.T) {
		if err := os.WriteFile(path, []byte(`{"permissions":`), 0600); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { time.Sleep(75 * time.Millisecond); done <- os.WriteFile(path, data, 0600) }()
		got, err := cursorUserConfigHash(home)
		writeErr := <-done
		if err != nil || writeErr != nil || got != want {
			t.Fatalf("truncated config did not recover: read=%v write=%v", err, writeErr)
		}
	})
}
