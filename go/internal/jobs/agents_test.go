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
			if !reflect.DeepEqual(got, test.Argv) {
				t.Fatalf("argv mismatch\n got: %#v\nwant: %#v", got, test.Argv)
			}
		})
	}
}

func TestOpenCodeReadOnlyEnvironmentKeepsReportStream(t *testing.T) {
	input := AgentCommandInput{Agent: "opencode", Prefix: []string{"opencode"}, Tool: "codex", Model: "google/gemini-test",
		ConfigArgs: []string{`model_reasoning_effort="high"`}, Sandbox: stringPointer("read-only"), Prompt: "plan"}
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
		Permission map[string]string `json:"permission"`
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
	stream := NewEventStream("opencode")
	stream.Write([]byte("{\"type\":\"text\",\"part\":{\"text\":\"plan report\"}}\n{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\"}}\n"))
	result := stream.Result(false, 0, "")
	if result.Status != "success" || result.Final != "plan report" {
		t.Fatalf("read-only report stream result = %#v", result)
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
	want := []string{"claude", "--setting-sources", "", "--restricted", "--permission-mode", "bypassPermissions", "--print", "--output-format", "json", "--model", "claude-sonnet-4-5", "--effort", "high", "--tools", "Read,Edit,Write,Glob,Grep,Bash", "work"}
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
	want := []string{"claude", "--setting-sources", "", "--restricted", "--safe-mode", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-chrome", "--print", "--output-format", "json", "--model", "opus", "--tools", "Read,Glob,Grep", "plan"}
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
