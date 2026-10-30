package errcheck

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
)

var Checks = []string{"errcheck", "ineffassign", "unused"}

// CheckStatus preserves the aggregate Result shape while exposing isolated failures.
func (r Result) CheckStatus(check string) string {
	status, _, _ := strings.Cut(r.CheckSummary(check), ";")
	return status
}

// CheckSummary includes coverage limitations alongside the check outcome.
func (r Result) CheckSummary(check string) string {
	for _, line := range strings.Split(r.Status, "\n") {
		if value, ok := strings.CutPrefix(line, check+"="); ok {
			return value
		}
	}
	return r.Status
}

func runCheck(check string, statuses map[string]string, run func()) {
	if statuses[check] != "" {
		return
	}
	defer func() {
		if v := recover(); v != nil {
			statuses[check] = fmt.Sprintf("failed:analyze: %v", v)
		}
	}()
	run()
}

func testRank(f Finding) int {
	if strings.HasSuffix(f.File, "_test.go") || strings.Contains("/"+f.File, "/testdata/") {
		return 1
	}
	return 0
}
func checkRank(check string) int {
	for i, name := range Checks {
		if name == check {
			return i
		}
	}
	return 0
}
func finding(root string, fs *token.FileSet, pos token.Pos, check, detail string) (string, Finding) {
	p := fs.PositionFor(pos, false)
	rel, _ := filepath.Rel(root, p.Filename)
	f := Finding{Check: check, File: filepath.ToSlash(rel), Line: p.Line, Detail: detail}
	return fmt.Sprintf("%s:%s:%d:%d", check, f.File, p.Line, p.Column), f
}

type unused struct {
	defs       map[string]Finding
	uses       map[string]bool
	symbols    map[string]string
	protected  map[string]bool
	skippedAsm map[string]bool
	skippedCgo map[string]bool
}

func newUnused() *unused {
	return &unused{defs: map[string]Finding{}, uses: map[string]bool{}, symbols: map[string]string{}, protected: map[string]bool{}, skippedAsm: map[string]bool{}, skippedCgo: map[string]bool{}}
}

// Normal and augmented test variants count as one package.
func (u *unused) skip(p pkg) bool {
	key := p.Dir + "/" + strings.Split(p.ImportPath, " [")[0]
	if len(p.SFiles) > 0 {
		u.skippedAsm[key] = true
	}
	if len(p.CgoFiles) > 0 {
		u.skippedCgo[key] = true
	}
	return len(p.SFiles)+len(p.CgoFiles) > 0
}

func (u *unused) scan(root string, fs *token.FileSet, nodes []*ast.File, info *types.Info, skipped bool) {
	// Object identities differ between ordinary and augmented test packages. Source
	// positions join them, so a use in either variant protects the declaration.
	key := func(o types.Object) string {
		p := fs.PositionFor(o.Pos(), false)
		return fmt.Sprintf("%s:%d:%d", p.Filename, p.Line, p.Column)
	}
	for _, o := range info.Uses {
		u.uses[key(o)] = true
	}
	for _, node := range nodes {
		protected := skipped
		for _, imp := range node.Imports {
			if imp.Path.Value == `"C"` {
				protected = true
			}
		}
		for _, group := range node.Comments {
			for _, c := range group.List {
				if strings.HasPrefix(c.Text, "//go:linkname") || strings.HasPrefix(c.Text, "//go:embed") {
					protected = true
					if strings.HasPrefix(c.Text, "//go:linkname") {
						fields := strings.Fields(c.Text)
						if len(fields) > 1 {
							for _, o := range info.Defs {
								if o != nil && o.Pkg() != nil && o.Name() == fields[1] {
									u.protected[strings.Split(o.Pkg().Path(), " [")[0]+"."+o.Name()] = true
								}
							}
						}
						if len(fields) > 2 {
							u.protected[fields[2]] = true
						}
					}
				}
			}
		}
		for _, decl := range node.Decls {
			var names []*ast.Ident
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					names = append(names, d.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						names = append(names, s.Name)
					case *ast.ValueSpec:
						names = append(names, s.Names...)
					}
				}
			}
			for _, id := range names {
				o := info.Defs[id]
				if o == nil || o.Exported() || id.Name == "_" || id.Name == "init" || id.Name == "main" {
					continue
				}
				k := key(o)
				if protected {
					u.uses[k] = true
					continue
				}
				kind := ""
				switch o.(type) {
				case *types.Func:
					kind = "func"
				case *types.Var:
					kind = "var"
				case *types.Const:
					kind = "const"
				case *types.TypeName:
					kind = "type"
				}
				if kind == "" {
					continue
				}
				p := fs.PositionFor(id.Pos(), false)
				if !within(root, p.Filename) {
					continue
				}
				if kind == "func" && strings.HasSuffix(p.Filename, "_test.go") {
					// Testing entry points are invoked by the generated test main.
					if strings.HasPrefix(id.Name, "Test") || strings.HasPrefix(id.Name, "Benchmark") || strings.HasPrefix(id.Name, "Fuzz") || strings.HasPrefix(id.Name, "Example") {
						continue
					}
				}
				_, f := finding(root, fs, id.Pos(), "unused", kind+" "+id.Name)
				u.defs[k] = f
				u.symbols[k] = strings.Split(o.Pkg().Path(), " [")[0] + "." + o.Name()
			}
		}
	}
}
func (u *unused) finish(found map[string]Finding) {
	for k, f := range u.defs {
		if !u.uses[k] && !u.protected[u.symbols[k]] {
			found["unused:"+k] = f
		}
	}
}
