package waiter

import (
	"runtime"
	"testing"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name, value, want string
	}{
		{name: "empty", value: "", want: "''"},
		{name: "spaces", value: "two words", want: "'two words'"},
		{name: "single quote", value: "it's", want: "'it'\\''s'"},
		{name: "double quote", value: `say "hi"`, want: `'say "hi"'`},
		{name: "backslashes", value: `a\\b`, want: `'a\\b'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellQuote(tt.value); got != tt.want {
				t.Fatalf("shellQuote(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestCurrentShellQuote(t *testing.T) {
	value := `a b'c"d\e`
	got, want := shellQuote(value), `'a b'\''c"d\e'`
	if runtime.GOOS == "windows" {
		got, want = powerShellQuote(value), `'a b''c"d\e'`
	}
	if got != want {
		t.Fatalf("current shell quote = %q, want %q", got, want)
	}
}

func TestCommandForPlatformPreservesShellQuotingWithBound(t *testing.T) {
	for _, tc := range []struct {
		name    string
		windows bool
		want    string
	}{
		{name: "posix", want: `"/path with space/purser-hook" wait job --session-owner token --max-seconds 150`},
		{name: "powershell", windows: true, want: `& '/path with space/purser-hook' wait job --session-owner token --max-seconds 150`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandForPlatform("/path with space/purser-hook", "job", "token", 150, tc.windows); got != tc.want {
				t.Fatalf("command = %q, want %q", got, tc.want)
			}
		})
	}
}
