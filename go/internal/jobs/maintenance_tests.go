package jobs

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"purser-hook/internal/proc"
)

// MaintenanceTestIdentity is populated from the host's durable job record.
type MaintenanceTestIdentity struct {
	JobID      string
	RecordedID string
	RecordedBy string
}

func (i MaintenanceTestIdentity) Valid() bool {
	return i.JobID != "" && i.JobID == i.RecordedID && i.RecordedBy == "host"
}

// ValidateMaintenanceTestDelta rejects any delta that is not a safe, nonempty
// set of test-only regular files. Delta paths are canonical repository paths.
func ValidateMaintenanceTestDelta(root string, delta map[string]any, identity MaintenanceTestIdentity) error {
	if !identity.Valid() {
		return fmt.Errorf("maintenance job identity is not host-recorded")
	}
	if delta == nil || delta["status"] != "OK" {
		return fmt.Errorf("maintenance delta is unavailable")
	}
	baseline, ok := delta["baseline_head"].(string)
	if !ok || len(baseline) < 40 || len(baseline) > 64 {
		return fmt.Errorf("maintenance baseline identity is unavailable")
	}
	for _, char := range baseline {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return fmt.Errorf("maintenance baseline identity is invalid")
		}
	}
	paths, ok := maintenancePaths(delta["changed_paths"])
	if !ok || len(paths) == 0 {
		return fmt.Errorf("maintenance delta has no canonical changed paths")
	}
	seen := map[string]bool{}
	for _, path := range paths {
		norm := strings.ReplaceAll(path, `\`, "/")
		if norm == "" || strings.HasPrefix(norm, "/") || strings.Contains(norm, "../") || norm == ".." || seen[norm] {
			return fmt.Errorf("invalid or duplicate maintenance path %q", path)
		}
		key := norm
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			return fmt.Errorf("duplicate maintenance path %q", path)
		}
		seen[key] = true
		if !maintenanceTestPath(norm) {
			return fmt.Errorf("non-test maintenance path %q", path)
		}
		if !strings.HasSuffix(strings.ToLower(norm), "_test.go") {
			entries, err := proc.Hide(exec.Command("git", "-C", root, "ls-tree", "-r", "-z", baseline, "--", norm)).Output()
			if err != nil {
				return fmt.Errorf("cannot inspect baseline test source %q: %w", path, err)
			}
			if len(entries) != 0 {
				return fmt.Errorf("editing an existing non-Go test source or fixture is not mechanically verifiable: %q", path)
			}
		}
		full := filepath.Join(root, filepath.FromSlash(norm))
		if err := rejectMaintenanceSymlinkParents(root, norm); err != nil {
			return err
		}
		info, err := os.Lstat(full)
		if err != nil {
			return fmt.Errorf("cannot inspect maintenance path %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("maintenance path is not a regular file: %q", path)
		}
		if baseline, ok := delta["baseline_head"].(string); ok && baseline != "" {
			if err := checkMaintenanceMode(root, baseline, norm); err != nil {
				return err
			}
		}
		if strings.EqualFold(filepath.Ext(norm), ".go") {
			existing := map[string]bool{}
			if baseline, ok := delta["baseline_head"].(string); ok && baseline != "" {
				var err error
				existing, err = baselineMaintenanceTests(root, baseline, norm)
				if err != nil {
					return err
				}
			}
			if err := validateMaintenanceGoFile(full, existing); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			if baseline, ok := delta["baseline_head"].(string); ok && baseline != "" {
				if err := compareMaintenanceGoTests(root, baseline, norm, full); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func rejectMaintenanceSymlinkParents(root, path string) error {
	current := root
	parts := strings.Split(path, "/")
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("cannot inspect maintenance path parent %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("maintenance path traverses non-directory or symlink: %q", path)
		}
	}
	return nil
}

func checkMaintenanceMode(root, baseline, path string) error {
	cmd := proc.Hide(exec.Command("git", "-C", root, "diff", "--raw", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", baseline, "--", path))
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("cannot inspect baseline mode for %q: %w", path, err)
	}
	if len(output) == 0 {
		return nil
	}
	entries := strings.Split(string(output), "\x00")
	if len(entries)%2 != 1 || entries[len(entries)-1] != "" {
		return fmt.Errorf("invalid git mode delta for %q", path)
	}
	for i := 0; i < len(entries)-1; i += 2 {
		fields := strings.Fields(entries[i])
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") || entries[i+1] == "" {
			return fmt.Errorf("invalid git mode delta for %q", path)
		}
		oldMode, newMode := strings.TrimPrefix(fields[0], ":"), fields[1]
		for _, mode := range []string{oldMode, newMode} {
			if mode != "000000" && mode != "100644" && mode != "100755" {
				return fmt.Errorf("unsupported maintenance file mode %s for %q", mode, path)
			}
		}
		if oldMode != "000000" && oldMode != newMode {
			return fmt.Errorf("maintenance mode change is prohibited: %q", path)
		}
	}
	return nil
}

func compareMaintenanceGoTests(root, baseline, path, currentPath string) error {
	old, err := proc.Hide(exec.Command("git", "-C", root, "show", baseline+":"+path)).Output()
	if err != nil {
		listing, listErr := proc.Hide(exec.Command("git", "-C", root, "ls-tree", "-r", "-z", baseline, "--", path)).Output()
		if listErr != nil {
			return fmt.Errorf("cannot inspect baseline tests for %q: %w", path, listErr)
		}
		if len(listing) == 0 {
			return nil
		}
		return fmt.Errorf("cannot read existing baseline test %q: %w", path, err)
	}
	previous, err := parser.ParseFile(token.NewFileSet(), path, old, 0)
	if err != nil {
		return fmt.Errorf("cannot parse baseline test %q: %w", path, err)
	}
	current, err := parser.ParseFile(token.NewFileSet(), currentPath, nil, 0)
	if err != nil {
		return fmt.Errorf("cannot parse current test %q: %w", path, err)
	}
	oldTests, newTests := testFunctions(previous), testFunctions(current)
	for name, oldFn := range oldTests {
		newFn, ok := newTests[name]
		if !ok {
			return fmt.Errorf("existing test was removed: %s", name)
		}
		if recognizedFailureCount(newFn.Body) < recognizedFailureCount(oldFn.Body) {
			return fmt.Errorf("existing test outcomes were weakened: %s", name)
		}
	}
	return nil
}

func testFunctions(file *ast.File) map[string]*ast.FuncDecl {
	result := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") && fn.Body != nil {
			result[fn.Name.Name] = fn
		}
	}
	return result
}

func recognizedFailureCount(body *ast.BlockStmt) int {
	count := 0
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name := selector.Sel.Name
		if strings.HasPrefix(name, "Error") || strings.HasPrefix(name, "Fatal") || name == "Fail" || name == "FailNow" {
			count++
		}
		return true
	})
	return count
}

// CountExecutedMaintenanceTests counts unique added identities that the host
// reports as executed; repeated table cases or duplicate reports count once.
func CountExecutedMaintenanceTests(added, executed []string) int {
	known := map[string]bool{}
	for _, identity := range added {
		if identity != "" {
			known[identity] = true
		}
	}
	counted := map[string]bool{}
	for _, identity := range executed {
		if known[identity] {
			counted[identity] = true
		}
	}
	return len(counted)
}

func maintenancePaths(raw any) ([]string, bool) {
	switch paths := raw.(type) {
	case []string:
		return paths, true
	case []any:
		out := make([]string, len(paths))
		for i, item := range paths {
			value, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[i] = value
		}
		return out, true
	default:
		return nil, false
	}
}

func maintenanceTestPath(path string) bool {
	lower := strings.ToLower(path)
	parts := strings.Split(lower, "/")
	for _, part := range parts[:len(parts)-1] {
		if part == "testdata" {
			name := parts[len(parts)-1]
			return strings.Contains(name, "test") || strings.Contains(name, "fixture") || strings.HasSuffix(name, ".golden") || strings.HasSuffix(name, ".json")
		}
	}
	name := parts[len(parts)-1]
	if strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py") || strings.HasSuffix(name, "_test.py") {
		return true
	}
	for _, ext := range []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"} {
		if strings.HasSuffix(name, ".test"+ext) || strings.HasSuffix(name, ".spec"+ext) {
			return true
		}
	}
	return false
}

func validateMaintenanceGoFile(path string, existing map[string]bool) error {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return fmt.Errorf("parse failed: %w", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
			continue
		}
		if existing[fn.Name.Name] {
			continue
		}
		param := fn.Type.Params.List[0]
		star, ok := param.Type.(*ast.StarExpr)
		if !ok || !isTestingT(star.X) {
			continue
		}
		if fn.Body == nil || hasUnconditionalSkip(fn.Body) || !hasOutcomeCheck(fn, file) || constructorOnly(fn.Body) {
			return fmt.Errorf("test %s has no recognized behavioral outcome", fn.Name.Name)
		}
	}
	return nil
}

func baselineMaintenanceTests(root, baseline, path string) (map[string]bool, error) {
	old, err := proc.Hide(exec.Command("git", "-C", root, "show", baseline+":"+path)).Output()
	if err != nil {
		listing, listErr := proc.Hide(exec.Command("git", "-C", root, "ls-tree", "-r", "-z", baseline, "--", path)).Output()
		if listErr != nil {
			return nil, fmt.Errorf("cannot inspect baseline tests for %q: %w", path, listErr)
		}
		if len(listing) == 0 {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("cannot read existing baseline test %q: %w", path, err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, old, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot parse baseline tests for %q: %w", path, err)
	}
	result := map[string]bool{}
	for name := range testFunctions(file) {
		result[name] = true
	}
	return result, nil
}

func isTestingT(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "T" && (fmt.Sprint(sel.X) == "testing")
}

func hasUnconditionalSkip(body *ast.BlockStmt) bool {
	for _, stmt := range body.List {
		call, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		invoke, ok := call.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		selector, ok := invoke.Fun.(*ast.SelectorExpr)
		if ok && (selector.Sel.Name == "Skip" || selector.Sel.Name == "Skipf" || selector.Sel.Name == "SkipNow") {
			return true
		}
	}
	return false
}

func hasOutcomeCheck(test *ast.FuncDecl, file *ast.File) bool {
	found := false
	paramNames := map[string]bool{}
	if test.Type.Params != nil {
		for _, field := range test.Type.Params.List {
			for _, name := range field.Names {
				paramNames[name.Name] = true
			}
		}
	}
	helpers := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || fn.Type.Params == nil {
			continue
		}
		for _, field := range fn.Type.Params.List {
			star, ok := field.Type.(*ast.StarExpr)
			if !ok || !isTestingT(star.X) {
				continue
			}
			for _, name := range field.Names {
				if bodyHasTestingFailure(fn.Body, map[string]bool{name.Name: true}) {
					helpers[fn.Name.Name] = true
				}
			}
		}
	}
	if bodyHasTestingFailure(test.Body, paramNames) {
		return true
	}
	ast.Inspect(test.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name, ok := call.Fun.(*ast.Ident); ok && helpers[name.Name] && len(call.Args) > 0 {
			found = true
		}
		return true
	})
	return found
}

func bodyHasTestingFailure(body *ast.BlockStmt, receivers map[string]bool) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || !receivers[receiver.Name] {
			return true
		}
		name := selector.Sel.Name
		if strings.HasPrefix(name, "Error") || strings.HasPrefix(name, "Fatal") || name == "Fail" || name == "FailNow" {
			found = true
		}
		return true
	})
	return found
}

func constructorOnly(body *ast.BlockStmt) bool {
	constructors, otherBehavior := 0, 0
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
			if strings.HasPrefix(selector.Sel.Name, "New") {
				constructors++
			} else if selector.Sel.Name != "Error" && selector.Sel.Name != "Errorf" && selector.Sel.Name != "Fatal" && selector.Sel.Name != "Fatalf" && selector.Sel.Name != "Fail" && selector.Sel.Name != "FailNow" {
				otherBehavior++
			}
		}
		return true
	})
	return constructors > 0 && otherBehavior == 0
}
