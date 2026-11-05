package profile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type securityFixtureCase struct {
	Input struct {
		Tier      string `json:"tier"`
		Section   string `json:"section"`
		Key       string `json:"key"`
		Value     any    `json:"value"`
		Operation string `json:"operation"`
	} `json:"input"`
	Valid  bool   `json:"valid"`
	Reason string `json:"reason"`
}

func TestDelegationRoleSetting(t *testing.T) {
	if ok, _ := ValidateSecurityKey("user", "roles", "enforce_delegation"); !ok {
		t.Fatal("key rejected")
	}
	for _, value := range []any{false, true, "false"} {
		_, boolean := value.(bool)
		if ok, _ := ValidateSecurityValue("roles", "enforce_delegation", value); ok != boolean {
			t.Fatalf("value %v: valid=%v", value, ok)
		}
	}
}

func TestSecurityPythonFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", ".claude", "hooks", "orchestrator", "tests", "fixtures", "go_profile", "security.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture []securityFixtureCase
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	for _, test := range fixture {
		var valid bool
		var reason string
		if test.Input.Operation == "key" {
			valid, reason = ValidateSecurityKey(test.Input.Tier, test.Input.Section, test.Input.Key)
		} else {
			valid, reason = ValidateSecurityValue(test.Input.Section, test.Input.Key, test.Input.Value)
		}
		if valid != test.Valid || reason != test.Reason {
			t.Errorf("%s.%s (%#v): got (%v, %q), want (%v, %q)", test.Input.Section, test.Input.Key, test.Input.Value, valid, reason, test.Valid, test.Reason)
		}
	}
}
