// Package testcoverage measures executable source lines from local coverage
// artifacts. It never interprets Go's statement percentage as line coverage.
package testcoverage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"purser-hook/internal/proc"
)

const unknown = "unknown"

var coverPosition = regexp.MustCompile(`^([0-9]+)\.([0-9]+),([0-9]+)\.([0-9]+)$`)

type Counts struct {
	Covered    int
	Executable int
}

// Evidence binds counts to the exact measured scope and invocation contract.
type Evidence struct {
	Ecosystem string
	Scope     string
	Flags     string
	Counts    Counts
	Status    string
	Raw       string `json:"-"`
}

func Unknown(ecosystem, scope, flags string) Evidence {
	return Evidence{Ecosystem: ecosystem, Scope: scope, Flags: flags, Status: unknown}
}

// ParseGoCover counts each source line intersected by a statement block once.
// A line is covered when any intersecting block has a positive execution count.
// This is a block-span line convention, not Go's statement coverage metric.
func ParseGoCover(data []byte, root string) (Counts, error) {
	lines := map[string]map[int]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	first := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if first {
			first = false
			if line != "mode: set" && line != "mode: count" && line != "mode: atomic" {
				return Counts{}, fmt.Errorf("invalid coverprofile mode")
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return Counts{}, fmt.Errorf("malformed coverprofile block")
		}
		separator := strings.LastIndex(fields[0], ":")
		if separator <= 0 {
			return Counts{}, fmt.Errorf("malformed coverprofile span")
		}
		fileName, positions := fields[0][:separator], fields[0][separator+1:]
		match := coverPosition.FindStringSubmatch(positions)
		if match == nil {
			return Counts{}, fmt.Errorf("malformed coverprofile position")
		}
		var position [4]int
		for i := range position {
			n, err := strconv.Atoi(match[i+1])
			if err != nil || n < 1 {
				return Counts{}, fmt.Errorf("invalid coverprofile position")
			}
			position[i] = n
		}
		start, end := position[0], position[2]
		count, err3 := strconv.Atoi(fields[2])
		if end < start || (end == start && position[3] < position[1]) || count < 0 || err3 != nil {
			return Counts{}, fmt.Errorf("invalid coverprofile values")
		}
		statements, statementErr := strconv.Atoi(fields[1])
		if statementErr != nil || statements < 1 {
			return Counts{}, fmt.Errorf("invalid coverprofile statement count")
		}
		file := filepath.Clean(strings.ReplaceAll(fileName, "\\", "/"))
		if filepath.IsAbs(file) {
			rel, err := filepath.Rel(root, file)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return Counts{}, fmt.Errorf("coverprofile file outside scope")
			}
			file = rel
		}
		file = filepath.ToSlash(file)
		if lines[file] == nil {
			lines[file] = map[int]bool{}
		}
		for n := start; n <= end; n++ {
			if count > 0 {
				lines[file][n] = true
			} else if _, exists := lines[file][n]; !exists {
				lines[file][n] = false
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Counts{}, err
	}
	if first || len(lines) == 0 {
		return Counts{}, fmt.Errorf("incomplete coverprofile")
	}
	var result Counts
	for _, fileLines := range lines {
		for _, covered := range fileLines {
			result.Executable++
			if covered {
				result.Covered++
			}
		}
	}
	if result.Executable == 0 {
		return Counts{}, fmt.Errorf("empty coverprofile scope")
	}
	return result, nil
}

func ParseLCOV(data []byte) (Counts, error) {
	seen := map[string]int{}
	file := ""
	var record map[int]bool
	lf, lh := -1, -1
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "SF:"):
			if file != "" {
				return Counts{}, fmt.Errorf("unterminated LCOV source")
			}
			file = strings.TrimPrefix(line, "SF:")
			if file == "" {
				return Counts{}, fmt.Errorf("empty LCOV source")
			}
			record = map[int]bool{}
			lf, lh = -1, -1
		case strings.HasPrefix(line, "DA:"):
			if file == "" {
				return Counts{}, fmt.Errorf("LCOV line outside source")
			}
			parts := strings.Split(strings.TrimPrefix(line, "DA:"), ",")
			if len(parts) < 2 {
				return Counts{}, fmt.Errorf("malformed LCOV line")
			}
			n, e1 := strconv.Atoi(parts[0])
			count, e2 := strconv.Atoi(parts[1])
			if e1 != nil || e2 != nil || n < 1 || count < 0 {
				return Counts{}, fmt.Errorf("invalid LCOV line values")
			}
			key := filepath.ToSlash(filepath.Clean(file)) + ":" + strconv.Itoa(n)
			if old, ok := seen[key]; ok && old != count {
				return Counts{}, fmt.Errorf("conflicting LCOV duplicate")
			}
			seen[key] = count
			record[n] = count > 0
		case strings.HasPrefix(line, "LF:") || strings.HasPrefix(line, "LH:"):
			n, err := strconv.Atoi(line[3:])
			if file == "" || err != nil || n < 0 {
				return Counts{}, fmt.Errorf("invalid LCOV totals")
			}
			declared := &lf
			if strings.HasPrefix(line, "LH:") {
				declared = &lh
			}
			if *declared != -1 {
				return Counts{}, fmt.Errorf("duplicate LCOV total")
			}
			*declared = n
		case line == "end_of_record":
			if file == "" {
				return Counts{}, fmt.Errorf("LCOV record without source")
			}
			hits := 0
			for _, covered := range record {
				if covered {
					hits++
				}
			}
			if lf != len(record) || lh != hits {
				return Counts{}, fmt.Errorf("incomplete LCOV record: DA disagrees with LF/LH")
			}
			file = ""
		case line == "" || strings.HasPrefix(line, "BRDA:") || strings.HasPrefix(line, "FN:") || strings.HasPrefix(line, "FNDA:") || strings.HasPrefix(line, "FNF:") || strings.HasPrefix(line, "FNH:") || strings.HasPrefix(line, "TN:"):
			// Other valid LCOV records do not contribute executable-line counts.
		default:
			return Counts{}, fmt.Errorf("malformed LCOV record")
		}
	}
	if file != "" || len(seen) == 0 {
		return Counts{}, fmt.Errorf("incomplete LCOV evidence")
	}
	var c Counts
	for _, n := range seen {
		c.Executable++
		if n > 0 {
			c.Covered++
		}
	}
	return c, nil
}

// ParsePythonJSON reads coverage.py's JSON report, counting each file's line
// universe from executed and missing lines and deduplicating source paths.
func ParsePythonJSON(data []byte) (Counts, error) {
	// Keep parsing dependency-free while accepting only the coverage.py shape.
	var report struct {
		Files map[string]struct {
			Executed []int `json:"executed_lines"`
			Missing  []int `json:"missing_lines"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &report); err != nil || len(report.Files) == 0 {
		return Counts{}, fmt.Errorf("malformed or incomplete coverage.py JSON")
	}
	seen := map[string]bool{}
	for file, f := range report.Files {
		if file == "" {
			return Counts{}, fmt.Errorf("empty coverage.py source")
		}
		if f.Executed == nil || f.Missing == nil {
			return Counts{}, fmt.Errorf("incomplete coverage.py line data")
		}
		for _, n := range f.Executed {
			if n < 1 {
				return Counts{}, fmt.Errorf("invalid executed line")
			}
			seen[filepath.ToSlash(filepath.Clean(file))+":"+strconv.Itoa(n)] = true
		}
		for _, n := range f.Missing {
			if n < 1 {
				return Counts{}, fmt.Errorf("invalid missing line")
			}
			key := filepath.ToSlash(filepath.Clean(file)) + ":" + strconv.Itoa(n)
			if _, ok := seen[key]; !ok {
				seen[key] = false
			}
		}
	}
	var c Counts
	for _, covered := range seen {
		c.Executable++
		if covered {
			c.Covered++
		}
	}
	if c.Executable == 0 {
		return Counts{}, fmt.Errorf("empty coverage.py scope")
	}
	return c, nil
}

const GoFlags = "go test -coverprofile -mod=readonly; GOTOOLCHAIN=local; GOPROXY=off; GOSUMDB=off"

// RunGo runs the caller's fixed package scope offline. The workdir and
// coverage output are resolved through absolute paths for Windows long paths.
func RunGo(ctx context.Context, root string, packages []string, timeout time.Duration) Evidence {
	packages = append([]string(nil), packages...)
	for i := range packages {
		packages[i] = strings.ReplaceAll(packages[i], "\\", "/")
	}
	scope := strings.Join(packages, " ")
	result := Unknown("go", scope, GoFlags)
	if len(packages) == 0 || timeout <= 0 {
		return result
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		return result
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return result
	}
	scratch := filepath.Join(abs, ".tmp", "testcoverage")
	if err = os.MkdirAll(scratch, 0700); err != nil {
		return result
	}
	profile, err := os.CreateTemp(scratch, "go-cover-*.out")
	if err != nil {
		return result
	}
	profilePath := profile.Name()
	if profile.Close() != nil {
		_ = os.Remove(profilePath)
		return result
	}
	defer os.Remove(profilePath)
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"test", "-coverprofile=" + profilePath, "-mod=readonly"}
	args = append(args, packages...)
	cmd := proc.Hide(exec.CommandContext(commandCtx, goPath, args...))
	cmd.Dir = abs
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	proc.ConfigureProcessTreeKill(cmd)
	if cmd.Run() != nil {
		return result
	}
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return result
	}
	counts, err := ParseGoCover(data, abs)
	if err != nil {
		return result
	}
	result.Counts, result.Status = counts, "measured"
	result.Raw = string(data)
	return result
}

func compatible(before, after Evidence) bool {
	return before.Status == "measured" && after.Status == "measured" && before.Ecosystem == after.Ecosystem && before.Scope != "" && before.Scope == after.Scope && before.Flags != "" && before.Flags == after.Flags && before.Counts.Executable > 0 && before.Counts.Executable == after.Counts.Executable && before.Counts.Covered >= 0 && before.Counts.Covered <= before.Counts.Executable && after.Counts.Covered >= 0 && after.Counts.Covered <= after.Counts.Executable
}

func Compare(before, after Evidence) bool {
	return compatible(before, after) && after.Counts.Covered > before.Counts.Covered
}

// CanWrite requires two successful host runs over an unchanged production scope
// and denominator. Unsupported ecosystems stay unknown: parsing a historical
// Python/JS artifact is insufficient evidence of a configured, runnable suite.
func CanWrite(ctx context.Context, root string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	before := MeasureProject(ctx, root, timeout)
	if before.Status != "measured" {
		return false
	}
	after := MeasureProject(ctx, root, timeout)
	return compatible(before, after) && before.Counts == after.Counts
}

// MeasureProject measures every production ecosystem, returning unknown for incomplete scope.
func MeasureProject(ctx context.Context, root string, timeout time.Duration) Evidence {
	result := Unknown("go", "", GoFlags)
	var files, modules []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".tmp", "testdata", "vendor", "node_modules", ".venv", "__pycache__":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unknown symlink scope")
		}
		switch entry.Name() {
		case "go.mod":
			modules = append(modules, filepath.Dir(path))
		case "package.json", "pyproject.toml", "setup.cfg", "Cargo.toml":
			return fmt.Errorf("no validated local suite for production ecosystem")
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".go":
			if !strings.HasSuffix(path, "_test.go") {
				files = append(files, path)
			}
		case ".py", ".pyw", ".pyi", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte", ".rs", ".rb", ".java", ".kt", ".c", ".cc", ".cpp", ".h", ".hpp", ".s", ".cs", ".php", ".swift", ".lua", ".sh", ".bash", ".zsh", ".ps1", ".bat", ".cmd":
			return fmt.Errorf("unmeasured production ecosystem")
		case "":
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			var prefix [2]byte
			n, readErr := file.Read(prefix[:])
			closeErr := file.Close()
			if readErr != nil && readErr != io.EOF {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n == 2 && string(prefix[:]) == "#!" {
				return fmt.Errorf("unmeasured script ecosystem")
			}
		}
		return nil
	})
	if err != nil || len(modules) != 1 || len(files) == 0 {
		return result
	}
	for i, file := range files {
		rel, err := filepath.Rel(modules[0], file)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return result
		}
		files[i] = filepath.ToSlash(rel)
	}
	result = RunGo(ctx, modules[0], []string{"./..."}, timeout)
	module, err := filepath.Rel(root, modules[0])
	if err != nil {
		return Unknown("go", "", GoFlags)
	}
	result.Scope = filepath.ToSlash(module) + "\n" + strings.Join(files, "\n")
	return result
}

// Aggregate requires evidence for every production ecosystem and compatible
// scope metadata within each before/after pair; percentages are never averaged.
func Aggregate(expected []string, evidence []Evidence) (Counts, bool) {
	byName := map[string]Evidence{}
	for _, e := range evidence {
		if e.Status != "measured" || e.Counts.Executable < 1 || e.Counts.Covered < 0 || e.Counts.Covered > e.Counts.Executable {
			return Counts{}, false
		}
		byName[e.Ecosystem] = e
	}
	if len(expected) == 0 {
		return Counts{}, false
	}
	var total Counts
	for _, name := range expected {
		e, ok := byName[name]
		if !ok {
			return Counts{}, false
		}
		total.Covered += e.Counts.Covered
		total.Executable += e.Counts.Executable
	}
	return total, total.Executable > 0
}
