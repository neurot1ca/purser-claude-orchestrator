package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type agentFixture struct {
	Oracle string      `json:"oracle"`
	Cases  []agentCase `json:"cases"`
}

type agentCase struct {
	Agent string   `json:"agent"`
	Kind  string   `json:"kind"`
	Argv  []string `json:"argv"`
	Error string   `json:"error"`
}

func TestAgentCommandPythonParity(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture agentFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Oracle != "codex_exec_mcp._agent_command and Server._run_tool_once resume identity" {
		t.Fatalf("unexpected oracle: %q", fixture.Oracle)
	}
	for _, test := range fixture.Cases {
		t.Run(test.Agent+"/"+test.Kind, func(t *testing.T) {
			if test.Kind == "resume_identity_mismatch" {
				otherAgent := "opencode"
				if test.Agent == "opencode" {
					otherAgent = "agy"
				}
				err := ValidateAgentResume(test.Agent, otherAgent, "thread-123", "")
				assertAgentError(t, err, test.Error)
				return
			}
			input := AgentCommandInput{
				Agent: test.Agent, Prefix: []string{"agent-bin", "--prefix"},
				Tool: "codex", CWD: "C:/repo", Model: "google/gemini-test",
				ConfigArgs: []string{`model_reasoning_effort="high"`}, Prompt: "prompt text",
			}
			if test.Agent == "agy" {
				input.Model = "gemini-test"
			}
			if test.Kind == "codex-reply" {
				if err := ValidateAgentResume(test.Agent, test.Agent, "thread-123", "thread-123"); err != nil {
					t.Fatal(err)
				}
				input.Tool, input.ThreadID = "codex-reply", "thread-123"
			}
			if test.Kind == "unsupported_sandbox" {
				input.Sandbox = stringPointer("read-only")
			}
			if test.Kind == "read_only" {
				input.Sandbox = stringPointer("read-only")
			}
			if test.Kind == "unsupported_config" {
				input.ConfigArgs = []string{"unsupported_option=true"}
			}
			got, err := BuildAgentCommand(input)
			if test.Error != "" {
				assertAgentError(t, err, test.Error)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := test.Argv
			if test.Agent == "opencode" {
				absolute, absErr := filepath.Abs("C:/repo")
				if absErr != nil {
					t.Fatal(absErr)
				}
				want = append([]string(nil), test.Argv...)
				for index := range want {
					want[index] = strings.ReplaceAll(want[index], "__CWD__", absolute)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("argv mismatch\n got: %#v\nwant: %#v", got, want)
			}
		})
	}
}

func TestOpenCodeReadOnlyEnvironmentKeepsReportStream(t *testing.T) {
	configHome, userProfile, appData, cwd := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("USERPROFILE", userProfile)
	t.Setenv("APPDATA", appData)
	for path, content := range map[string]string{
		filepath.Join(configHome, "opencode", "opencode.jsonc"):            `{"mcp":{"global-server":{"type":"local"},},}`,
		filepath.Join(userProfile, ".config", "opencode", "opencode.json"): `{"mcp":{"servers":{"type":"remote"}}}`,
		filepath.Join(appData, "opencode", "opencode.json"):                `{"mcp":{"roaming":{"type":"local"}}}`,
		filepath.Join(cwd, "opencode.jsonc"): `{
			// Project MCP is disabled even with JSONC comments.
			"mcp": {"project-server": {"type": "local"},},
		}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	input := AgentCommandInput{Agent: "opencode", Prefix: []string{"opencode"}, Tool: "codex", Model: "google/gemini-test",
		ConfigArgs: []string{`model_reasoning_effort="high"`}, Sandbox: stringPointer("read-only"), Prompt: "plan", CWD: cwd}
	command, err := BuildAgentCommand(input)
	if err != nil {
		t.Fatal(err)
	}
	wantAgent := false
	for i := range command {
		if command[i] == "--agent" && i+1 < len(command) && command[i+1] == openCodeReadOnlyAgent {
			wantAgent = true
		}
	}
	if !wantAgent {
		t.Fatalf("read-only OpenCode argv missing named agent: %#v", command)
	}
	environment := AgentCommandEnvironment(input)
	if len(environment) != 1 || !strings.HasPrefix(environment[0], "OPENCODE_CONFIG_CONTENT=") {
		t.Fatalf("read-only OpenCode environment = %#v", environment)
	}
	var config struct {
		Permission map[string]string         `json:"permission"`
		MCP        map[string]map[string]any `json:"mcp"`
		Agent      map[string]struct {
			Permission map[string]string `json:"permission"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(environment[0], "OPENCODE_CONFIG_CONTENT=")), &config); err != nil {
		t.Fatal(err)
	}
	for _, permission := range []string{"edit", "write", "bash", "webfetch", "task", "external_directory"} {
		if config.Permission[permission] != "deny" || config.Agent[openCodeReadOnlyAgent].Permission[permission] != "deny" {
			t.Errorf("permission %q is not denied in global and named agent config: %#v", permission, config)
		}
	}
	for _, name := range []string{"global-server", "servers", "roaming", "project-server"} {
		if enabled, ok := config.MCP[name]["enabled"].(bool); !ok || enabled {
			t.Errorf("MCP server %q is not disabled: %#v", name, config.MCP)
		}
	}
	if config.Permission["*_*"] != "deny" || config.Agent[openCodeReadOnlyAgent].Permission["*_*"] != "deny" {
		t.Fatalf("MCP wildcard permission is not denied globally and for the read-only agent: %#v", config)
	}
	stream := NewEventStream("opencode")
	stream.Write([]byte("{\"type\":\"text\",\"part\":{\"text\":\"plan report\"}}\n{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\"}}\n"))
	result := stream.Result(false, 0, "")
	if result.Status != "success" || result.Final != "plan report" {
		t.Fatalf("read-only report stream result = %#v", result)
	}
}

func TestOpenCodeMCPDisabledWithCommentAfterTrailingComma(t *testing.T) {
	for name, content := range map[string]string{
		"line":  "{\"mcp\": {\"a\": {}, // note\n}}",
		"block": `{"mcp": {"a": {}, /* note */ }}`,
	} {
		t.Run(name, func(t *testing.T) {
			cwd := t.TempDir()
			if err := os.WriteFile(filepath.Join(cwd, "opencode.jsonc"), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			server, _ := openCodeDisabledMCPServers(cwd)["a"].(map[string]any)
			if enabled, ok := server["enabled"].(bool); !ok || enabled {
				t.Fatalf("MCP server is not disabled: %#v", server)
			}
		})
	}
}

func TestAgyPlanKeepsJSONReportChannel(t *testing.T) {
	stream := NewEventStream("agy")
	stream.Write([]byte("{\"status\":\"success\",\"response\":\"plan report\",\"conversation_id\":\"plan-session\"}\n"))
	result := stream.Result(false, 0, "")
	if result.Status != "success" || result.ThreadID != "plan-session" || result.Final != "plan report" {
		t.Fatalf("agy plan report result = %#v", result)
	}
}

func TestClaudeWorkerCarriesEffortAndSuppressesNestedHooks(t *testing.T) {
	command, err := BuildAgentCommand(AgentCommandInput{Agent: "claude", Prefix: []string{"claude"}, Tool: "codex", CWD: t.TempDir(),
		Model: "claude-sonnet-4-5", ConfigArgs: []string{`model_reasoning_effort="high"`}, Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--setting-sources", "", "--strict-mcp-config", "--permission-mode", "bypassPermissions", "--print", "--output-format", "json", "--model", "claude-sonnet-4-5", "--effort", "high", "--tools", "Read,Edit,Write,Glob,Grep,Bash", "--", "work"}
	if !reflect.DeepEqual(command, want) {
		t.Fatalf("Claude command argv mismatch\n got: %#v\nwant: %#v", command, want)
	}
	withoutEffort, err := BuildAgentCommand(AgentCommandInput{Agent: "claude", Prefix: []string{"claude"}, Tool: "codex", Model: "claude-sonnet-4-5", Prompt: "work"})
	if err != nil {
		t.Fatalf("Claude launch rejected omitted effort: %v", err)
	}
	for _, arg := range withoutEffort {
		if arg == "--effort" {
			t.Fatalf("omitted Claude effort emitted --effort: %#v", withoutEffort)
		}
	}
	if _, err := BuildAgentCommand(AgentCommandInput{Agent: "claude", Prefix: []string{"claude"}, Tool: "codex", Model: "unsupported", ConfigArgs: []string{`model_reasoning_effort="high"`}, Prompt: "work"}); err == nil {
		t.Fatal("Claude launch accepted an unsupported model")
	}
}

func TestAgyWorkerPromptRequestsForegroundCommands(t *testing.T) {
	command, err := BuildAgentCommand(AgentCommandInput{Agent: "agy", Prefix: []string{"agy"}, Tool: "codex", CWD: t.TempDir(),
		Model: "gemini-test", ConfigArgs: []string{`model_reasoning_effort="high"`}, Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	const foregroundInstruction = "Run every command in the foreground and wait for it; never start background tasks."
	if command[2] != "work\n"+foregroundInstruction {
		t.Fatalf("agy prompt = %q, want foreground instruction appended", command[2])
	}

	nonAgy, err := BuildAgentCommand(AgentCommandInput{Agent: "claude", Prefix: []string{"claude"}, Tool: "codex", Model: "haiku", Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if got := nonAgy[len(nonAgy)-1]; got != "work" {
		t.Fatalf("non-agy prompt changed: %q", got)
	}
}

func TestClaudeResearchIsReadOnlyAndAliasNoneOmitsEffort(t *testing.T) {
	command, err := BuildAgentCommand(AgentCommandInput{Agent: "claude", Prefix: []string{"claude"}, Tool: "codex", Model: "opus",
		Sandbox: stringPointer("read-only"), Prompt: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--setting-sources", "", "--restricted", "--safe-mode", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-chrome", "--print", "--output-format", "json", "--model", "opus", "--tools", "Read,Glob,Grep", "--", "plan"}
	if !reflect.DeepEqual(command, want) {
		t.Fatalf("Claude research argv mismatch\n got: %#v\nwant: %#v", command, want)
	}
	command, err = BuildAgentCommand(AgentCommandInput{Agent: "claude", Prefix: []string{"claude"}, Tool: "codex", Model: "haiku",
		ConfigArgs: []string{`model_reasoning_effort="none"`}, Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range command {
		if arg == "--effort" {
			t.Fatalf("Claude none effort must omit --effort: %#v", command)
		}
	}
}

func TestValidateStartAcceptsClaudePlanJob(t *testing.T) {
	t.Setenv("PURSER_AGENT", "")
	args := map[string]any{"call": "00000000-0000-0000-0000-000000000001", "prompt": "plan", "cwd": t.TempDir(), "model": "opus", "sandbox": "read-only"}
	request, failure := ValidateStart(args, StartValidationOptions{Agent: "claude", FixedAgent: true})
	if failure != nil || request.Agent != "claude" {
		t.Fatalf("Claude plan start failed validation: request=%#v failure=%#v", request, failure)
	}
}

func TestValidateStartSandboxDoesNotDropAlternateAgentReadOnly(t *testing.T) {
	for _, tc := range []struct {
		agent string
		model string
	}{
		{agent: "opencode", model: "google/gemini-test"},
		{agent: "agy", model: "gemini-test"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			args := map[string]any{"call": "00000000-0000-0000-0000-000000000001", "prompt": "plan", "cwd": t.TempDir(),
				"model": tc.model, "sandbox": "read-only", "config": map[string]any{"model_reasoning_effort": "high"}}
			request, failure := ValidateStart(args, StartValidationOptions{Agent: tc.agent, FixedAgent: true})
			if failure != nil || request.Agent != tc.agent {
				t.Fatalf("read-only start failed validation: request=%#v failure=%#v", request, failure)
			}
		})
	}
}

func assertAgentError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func stringPointer(value string) *string { return &value }

func TestWorkspaceWriteSandboxIsTheBridgedCodersDefault(t *testing.T) {
	in := AgentCommandInput{Agent: "opencode", Tool: "codex", CWD: t.TempDir(), Model: "opencode/big-pickle", Prompt: "work"}
	want, err := BuildAgentCommand(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Sandbox = stringPointer("workspace-write")
	got, err := BuildAgentCommand(in)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace-write command = %q, %v; want %q", got, err, want)
	}
}
