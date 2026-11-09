package jobs

import (
	"context"
	"testing"
)

func TestAbnormalExitOverride(t *testing.T) {
	const pkg = "example.test/internal/contextio"
	selected := []string{pkg}

	tests := []struct {
		name     string
		err      error
		stream   []string
		want     string
		selected []string
	}{
		{
			name: "minus one with every selected package passed",
			err:  workerExitError{code: -1},
			stream: []string{
				`{"Action":"pass","Test":"TestOne","Package":"` + pkg + `"}`,
				`{"Action":"pass","Package":"` + pkg + `"}`,
			},
			want: "go_test_root_exit_minus_one_all_packages_passed",
		},
		{
			name: "minus one with failed test",
			err:  workerExitError{code: -1},
			stream: []string{
				`{"Action":"fail","Test":"TestOne","Package":"` + pkg + `"}`,
				`{"Action":"fail","Package":"` + pkg + `"}`,
			},
		},
		{
			name: "minus one with selected package missing",
			err:  workerExitError{code: -1},
			stream: []string{
				`{"Action":"pass","Package":"example.test/other"}`,
			},
		},
		{
			name: "minus one with build failure",
			err:  workerExitError{code: -1},
			stream: []string{
				`{"Action":"output","Package":"` + pkg + `","Output":"# ` + pkg + `\n"}`,
				`{"Action":"pass","Package":"` + pkg + `"}`,
			},
		},
		{
			name: "exit one despite all packages passed",
			err:  workerExitError{code: 1},
			stream: []string{
				`{"Action":"pass","Package":"` + pkg + `"}`,
			},
		},
		{
			name:     "captured package cannot satisfy two selected paths",
			err:      workerExitError{code: -1},
			selected: []string{"./foo", "./bar/foo"},
			stream: []string{
				`{"Action":"pass","Package":"module/bar/foo"}`,
				`{"Action":"pass","Package":"module/unrelated"}`,
			},
		},
		{
			name: "timeout remains unchanged",
			err:  context.DeadlineExceeded,
			stream: []string{
				`{"Action":"pass","Package":"` + pkg + `"}`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := &applyValidation{Counts: map[string]int{}}
			capture := &testJSONCapture{record: record}
			for _, line := range tt.stream {
				parseTestJSONLine([]byte(line), capture)
			}
			packages := tt.selected
			if packages == nil {
				packages = selected
			}
			got := abnormalGoTestExitOverride(tt.err, capture, packages, "windows")
			if got != tt.want {
				t.Fatalf("override note = %q, want %q", got, tt.want)
			}
			if got != "" {
				record.ValidationNote = got
			}
			if record.ValidationNote != tt.want {
				t.Fatalf("validation note = %q, want %q", record.ValidationNote, tt.want)
			}
		})
	}
}

func TestValidationFinalJSONLine(t *testing.T) {
	const pkg = "example.test/internal/contextio"
	const pass = `{"Action":"pass","Package":"` + pkg + `"}`
	for _, tt := range []struct {
		name    string
		stream  string
		invalid bool
	}{
		{name: "valid final line without newline", stream: pass},
		{name: "invalid final line blocks override", stream: pass + "\n" + `{"Action":`, invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			record := &applyValidation{}
			capture := &testJSONCapture{record: record}
			if _, err := capture.Write([]byte(tt.stream)); err != nil {
				t.Fatal(err)
			}
			note := abnormalGoTestExitOverride(workerExitError{code: -1}, capture, []string{pkg}, "windows")
			if record.Counts["packages_passed"] != 1 || capture.invalidStream != tt.invalid {
				t.Fatalf("counts = %v, invalidStream = %v", record.Counts, capture.invalidStream)
			}
			if (note == "") != tt.invalid {
				t.Fatalf("override note = %q, invalid final line = %v", note, tt.invalid)
			}
			capture.Finish()
			if record.Counts["packages_passed"] != 1 || len(capture.pending) != 0 {
				t.Fatalf("repeated Finish changed counts or retained pending data: %#v", capture)
			}
		})
	}
}
