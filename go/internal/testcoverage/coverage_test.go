package testcoverage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGoCoverDeduplicatesOverlappingBlockSpans(t *testing.T) {
	data := []byte("mode: set\npkg/a.go:1.1,3.2 2 1\npkg/a.go:2.1,4.2 1 0\n")
	got, err := ParseGoCover(data, ".")
	if err != nil {
		t.Fatal(err)
	}
	if got != (Counts{Covered: 3, Executable: 4}) {
		t.Fatalf("counts = %+v", got)
	}
}

func TestCoverageFixtureParsersNormalizeCounts(t *testing.T) {
	// The Go fixture is actual go test -coverprofile output: three covered
	// span lines (4-6) and one missing line (7), despite 2/3 statement coverage.
	for name, parse := range map[string]func([]byte) (Counts, error){
		"go.coverprofile": func(data []byte) (Counts, error) { return ParseGoCover(data, ".") },
		"python.json":     ParsePythonJSON,
		"javascript.lcov": ParseLCOV,
	} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := parse(data)
		if err != nil || got != (Counts{Covered: 3, Executable: 4}) {
			t.Fatalf("%s counts=%+v error=%v", name, got, err)
		}
	}
}

func TestGoCoverRejectsMalformedColumn(t *testing.T) {
	if _, err := ParseGoCover([]byte("mode: set\npkg/a.go:1.bad,2.2 1 1\n"), "."); err == nil {
		t.Fatal("malformed coverprofile column accepted")
	}
}

func TestLCOVRejectsTruncatedTotals(t *testing.T) {
	for _, totals := range []string{"LF:2\nLH:1\n", "LF:1\nLH:2\n"} {
		if _, err := ParseLCOV([]byte("SF:a.js\nDA:1,1\n" + totals + "end_of_record\n")); err == nil {
			t.Fatal("DA records disagreeing with LF/LH accepted")
		}
	}
}

func TestMalformedOrIncompleteCoverageIsRejected(t *testing.T) {
	for _, parse := range []func() error{
		func() error { _, err := ParseGoCover([]byte("mode: set\npkg/a.go:bad\n"), "."); return err },
		func() error { _, err := ParseGoCover([]byte("mode: set\n"), "."); return err },
		func() error {
			_, err := ParsePythonJSON([]byte(`{"files":{"a.py":{"executed_lines":[1]}}}`))
			return err
		},
		func() error { _, err := ParseLCOV([]byte("SF:a.js\nDA:1,1\n")); return err },
	} {
		if parse() == nil {
			t.Fatal("malformed/incomplete evidence accepted")
		}
	}
}

func TestComparisonRejectsDenominatorOrScopeMismatch(t *testing.T) {
	before := Evidence{Ecosystem: "go", Scope: "./pkg", Flags: GoFlags, Counts: Counts{Covered: 3, Executable: 5}, Status: "measured"}
	after := before
	after.Counts = Counts{Covered: 4, Executable: 5}
	if !Compare(before, after) {
		t.Fatal("compatible improvement rejected")
	}
	after.Counts.Executable++
	if Compare(before, after) {
		t.Fatal("denominator mismatch accepted")
	}
	after = before
	after.Counts.Covered++
	after.Scope = "./other"
	if Compare(before, after) {
		t.Fatal("scope mismatch accepted")
	}
}

func TestAggregateRequiresEveryProductionEcosystem(t *testing.T) {
	goEvidence := Evidence{Ecosystem: "go", Scope: "./...", Flags: GoFlags, Counts: Counts{Covered: 2, Executable: 3}, Status: "measured"}
	if _, ok := Aggregate([]string{"go", "python"}, []Evidence{goEvidence}); ok {
		t.Fatal("incomplete ecosystem scope aggregated")
	}
	pythonEvidence := Evidence{Ecosystem: "python", Scope: "configured-suite", Flags: "coverage-json", Counts: Counts{Covered: 1, Executable: 2}, Status: "measured"}
	got, ok := Aggregate([]string{"go", "python"}, []Evidence{goEvidence, pythonEvidence})
	if !ok || got != (Counts{Covered: 3, Executable: 5}) {
		t.Fatalf("aggregate=%+v complete=%t", got, ok)
	}
}

func TestRunGoUnavailableToolReturnsUnknown(t *testing.T) {
	t.Setenv("PATH", "")
	got := RunGo(context.Background(), ".", []string{"./..."}, time.Second)
	if got.Status != unknown {
		t.Fatalf("status=%q, want unknown", got.Status)
	}
}
