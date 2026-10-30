// Package errcheck finds discarded error results without external analyzers.
package errcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"purser-hook/internal/proc"
)

type Finding struct {
	Check  string
	File   string
	Line   int
	Detail string
}

type Result struct {
	Status   string
	Findings []Finding
}

type pkg struct {
	Dir, ImportPath, Export                    string
	GoFiles, CompiledGoFiles, CgoFiles, SFiles []string
	ImportMap                                  map[string]string
	Error                                      *struct{ Err string }
	DepsErrors                                 []struct{ Err string }
	Incomplete                                 bool
}

type importerMap struct {
	base    types.Importer
	mapping map[string]string
}

func (i importerMap) Import(path string) (*types.Package, error) {
	if p := i.mapping[path]; p != "" {
		path = p
	}
	return i.base.Import(path)
}

func isError(t types.Type) bool {
	return t != nil && types.AssignableTo(t, types.Universe.Lookup("error").Type())
}

func returnsError(t types.Type) bool {
	if tuple, ok := t.(*types.Tuple); ok {
		for j := 0; j < tuple.Len(); j++ {
			if isError(tuple.At(j).Type()) {
				return true
			}
		}
		return false
	}
	return isError(t)
}

var excluded = map[string]bool{
	"(*bytes.Buffer).Write":            true,
	"(*bytes.Buffer).WriteByte":        true,
	"(*bytes.Buffer).WriteRune":        true,
	"(*bytes.Buffer).WriteString":      true,
	"crypto/rand.Read":                 true,
	"errors.AsType":                    true,
	"fmt.Print":                        true,
	"fmt.Printf":                       true,
	"fmt.Println":                      true,
	"fmt.Fprint(*bytes.Buffer)":        true,
	"fmt.Fprintf(*bytes.Buffer)":       true,
	"fmt.Fprintln(*bytes.Buffer)":      true,
	"fmt.Fprint(*strings.Builder)":     true,
	"fmt.Fprintf(*strings.Builder)":    true,
	"fmt.Fprintln(*strings.Builder)":   true,
	"fmt.Fprint(os.Stderr)":            true,
	"fmt.Fprintf(os.Stderr)":           true,
	"fmt.Fprintln(os.Stderr)":          true,
	"(*io.PipeReader).CloseWithError":  true,
	"(*io.PipeWriter).CloseWithError":  true,
	"math/rand.Read":                   true,
	"(*math/rand.Rand).Read":           true,
	"(*strings.Builder).Write":         true,
	"(*strings.Builder).WriteByte":     true,
	"(*strings.Builder).WriteRune":     true,
	"(*strings.Builder).WriteString":   true,
	"(hash.Hash).Write":                true,
	"(*crypto/sha3.SHA3).Write":        true,
	"(*crypto/sha3.SHAKE).Read":        true,
	"(*crypto/sha3.SHAKE).Write":       true,
	"(*hash/maphash.Hash).Write":       true,
	"(*hash/maphash.Hash).WriteByte":   true,
	"(*hash/maphash.Hash).WriteString": true,
}

func exclude(call *ast.CallExpr, info *types.Info) string {
	fun := call.Fun
	for {
		switch x := fun.(type) {
		case *ast.IndexExpr:
			fun = x.X
		case *ast.IndexListExpr:
			fun = x.X
		case *ast.ParenExpr:
			fun = x.X
		default:
			goto ready
		}
	}
ready:
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok {
		return ""
	}
	names := []string{fn.FullName()}
	if selection := info.Selections[sel]; selection != nil {
		t := selection.Recv()
		if _, ok := t.Underlying().(*types.Interface); ok {
			var walk func(types.Type)
			seen := map[types.Type]bool{}
			walk = func(t types.Type) {
				if seen[t] {
					return
				}
				seen[t] = true
				if n, ok := t.(*types.Named); ok {
					names = append(names, "("+n.String()+")."+fn.Name())
				}
				if i, ok := t.Underlying().(*types.Interface); ok {
					for j := 0; j < i.NumEmbeddeds(); j++ {
						walk(i.EmbeddedType(j))
					}
				}
			}
			walk(t)
		}
	}
	arg := ""
	if len(call.Args) > 0 {
		a := call.Args[0]
		if t := info.TypeOf(a); t != nil {
			arg = t.String()
		}
		if x, ok := a.(*ast.SelectorExpr); ok {
			if v, ok := info.Uses[x.Sel].(*types.Var); ok && v.Pkg() != nil && v.Pkg().Name() == "os" && (v.Name() == "Stderr" || v.Name() == "Stdout") {
				arg = "os." + v.Name()
			}
		}
	}
	for _, n := range names {
		if excluded[n] || (arg != "" && excluded[n+"("+arg+")"]) {
			return "default exclusion"
		}
	}
	return ""
}

// Run is advisory: a failed check publishes no findings and never means clean.
// Shared load failures invalidate all checks. Analyzer failures are isolated.
// Dependencies and toolchains must already be available locally.
func Run(ctx context.Context, root string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	fail := func(reason string, err error) (Result, error) {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		detail := []rune(strings.Join(strings.Fields(err.Error()), " "))
		if len(detail) > 160 {
			detail = append(detail[:160], []rune("...")...)
		}
		return Result{Status: "failed:" + reason + ": " + string(detail)}, err
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		return Result{Status: "skipped:no-go-toolchain"}, nil
	}
	modules := map[string]bool{}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		modules[root] = true
	} else if !os.IsNotExist(err) {
		return fail("detect", err)
	}
	cmd := proc.Hide(exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-co", "--exclude-standard", "-z", "--", "*.go", "go.mod", "**/go.mod", ":(exclude)**/testdata/**", ":(exclude)**/vendor/**"))
	proc.ConfigureProcessTreeKill(cmd)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return fail("detect", ctx.Err())
	}
	if err != nil {
		return fail("detect modules", err)
	}
	hasGo := false
	for _, file := range strings.Split(string(out), "\x00") {
		if strings.HasSuffix(file, ".go") {
			hasGo = true
		}
		if filepath.Base(file) == "go.mod" {
			modules[filepath.Join(root, filepath.Dir(file))] = true
		}
	}
	if len(modules) == 0 {
		if !hasGo {
			return Result{Status: "skipped:not-go"}, nil
		}
		modules[root] = true
	}
	dirs := make([]string, 0, len(modules))
	for dir := range modules {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	found := map[string]Finding{}
	dead := newUnused()
	statuses := map[string]string{}
	for _, dir := range dirs {
		if err := scan(ctx, goPath, root, dir, found, dead, statuses); err != nil {
			module, _ := filepath.Rel(root, dir)
			return fail("load/type-check module "+filepath.ToSlash(module), err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail("timeout/canceled", err)
	}
	dead.finish(found)
	result := Result{Status: "ok"}
	for _, check := range Checks {
		if statuses[check] != "" || len(dead.skippedAsm)+len(dead.skippedCgo) > 0 {
			result.Status = ""
			break
		}
	}
	if result.Status == "" {
		for _, check := range Checks {
			status := statuses[check]
			if status == "" {
				status = "ok"
			}
			if check == "unused" {
				status += fmt.Sprintf("; skipped_asm_packages=%d; skipped_cgo_packages=%d", len(dead.skippedAsm), len(dead.skippedCgo))
			}
			result.Status += check + "=" + status + "\n"
		}
	}
	for _, finding := range found {
		if statuses[finding.Check] == "" {
			result.Findings = append(result.Findings, finding)
		}
	}
	sortFindings(result.Findings)
	return result, nil
}

func sortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if pa, pb := testRank(a), testRank(b); pa != pb {
			return pa < pb
		}
		if pa, pb := checkRank(a.Check), checkRank(b.Check); pa != pb {
			return pa < pb
		}
		if pa, pb := priority(a), priority(b); pa != pb {
			return pa < pb
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Detail < b.Detail
	})
}

func priority(f Finding) int {
	rank := 0
	if f.Check != "" && f.Check != "errcheck" {
		return 0
	}
	expr, _ := parser.ParseExpr(f.Detail)
	var name string
	for expr != nil {
		switch x := ast.Unparen(expr).(type) {
		case *ast.IndexExpr:
			expr = x.X
			continue
		case *ast.IndexListExpr:
			expr = x.X
			continue
		case *ast.SelectorExpr:
			name = x.Sel.Name
		case *ast.Ident:
			name = x.Name
		}
		break
	}
	switch name {
	case "Close", "Flush", "Sync", "Rename", "Chmod", "Chtimes", "Truncate", "Symlink", "Link", "Encode", "Commit", "Unlock", "Kill", "Wait", "Signal":
		return rank
	}
	if strings.HasPrefix(name, "Write") || strings.HasPrefix(name, "Remove") || strings.HasPrefix(name, "Mkdir") {
		return rank
	}
	return rank + 1
}

func scan(ctx context.Context, goPath, root, dir string, found map[string]Finding, dead *unused, statuses map[string]string) error {
	cmd := proc.Hide(exec.CommandContext(ctx, goPath, "list", "-mod=readonly", "-deps", "-export", "-compiled", "-test", "-json", "./..."))
	proc.ConfigureProcessTreeKill(cmd)
	cmd.WaitDelay = time.Second
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GONOPROXY=none", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	var packages []pkg
	exports := map[string]string{}
	for {
		var p pkg
		if err := decoder.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if p.Error != nil || p.Incomplete || len(p.DepsErrors) > 0 {
			return fmt.Errorf("incomplete package %s", p.ImportPath)
		}
		packages = append(packages, p)
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
	}
	fs := token.NewFileSet()
	imp := importer.ForCompiler(fs, "gc", func(path string) (io.ReadCloser, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path = exports[path]
		if path == "" {
			return nil, fmt.Errorf("missing export data")
		}
		return os.Open(path)
	})
	for _, p := range packages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.Dir == "" || !within(root, p.Dir) || strings.HasSuffix(p.ImportPath, ".test") {
			continue
		}
		files := p.CompiledGoFiles
		if len(files) == 0 {
			files = p.GoFiles
		}
		var nodes []*ast.File
		for _, file := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !filepath.IsAbs(file) {
				file = filepath.Join(p.Dir, file)
			}
			node, err := parser.ParseFile(fs, file, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			nodes = append(nodes, node)
		}
		if len(nodes) == 0 {
			continue
		}
		info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
		conf := types.Config{Importer: importerMap{imp, p.ImportMap}, FakeImportC: true}
		if _, err := conf.Check(p.ImportPath, fs, nodes, info); err != nil {
			return err
		}
		runCheck("unused", statuses, func() {
			dead.scan(root, fs, nodes, info, dead.skip(p))
		})
		runCheck("ineffassign", statuses, func() { ineffassign(root, fs, nodes, info, found) })
		runCheck("errcheck", statuses, func() {
			for _, node := range nodes {
				ast.Inspect(node, func(n ast.Node) bool {
					var expr ast.Expr
					switch x := n.(type) {
					case *ast.ExprStmt:
						expr = ast.Unparen(x.X)
					case *ast.DeferStmt:
						expr = x.Call
					case *ast.GoStmt:
						expr = x.Call
					default:
						return true
					}
					callee := expr
					switch x := expr.(type) {
					case *ast.CallExpr:
						if !returnsError(info.TypeOf(x)) || exclude(x, info) != "" {
							return true
						}
						callee = x.Fun
					case *ast.UnaryExpr:
						if x.Op != token.ARROW || !isError(info.TypeOf(x)) {
							return true
						}
					default:
						return true
					}
					pos := fs.PositionFor(expr.Pos(), false)
					if !within(root, pos.Filename) {
						return true
					}
					rel, _ := filepath.Rel(root, pos.Filename)
					var name bytes.Buffer
					if format.Node(&name, fs, callee) != nil {
						return true
					}
					key := fmt.Sprintf("errcheck:%s:%d:%d", rel, pos.Line, pos.Column)
					found[key] = Finding{Check: "errcheck", File: filepath.ToSlash(rel), Line: pos.Line, Detail: name.String()}
					return true
				})
			}
		})
	}
	return nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
