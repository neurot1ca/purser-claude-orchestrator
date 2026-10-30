package errcheck

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
)

type live map[types.Object]bool

func copyLive(in live) live {
	out := live{}
	for o := range in {
		out[o] = true
	}
	return out
}
func union(a, b live) live {
	for o := range b {
		a[o] = true
	}
	return a
}

// Backward may-liveness: a read on any reachable successor protects a write.
// Loop variables are excluded altogether rather than guessing at a fixed point.
func ineffassign(root string, fs *token.FileSet, nodes []*ast.File, info *types.Info, found map[string]Finding) {
	for _, node := range nodes {
		for _, decl := range node.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			skip := map[types.Object]bool{}
			locals := map[types.Object]bool{}
			badFlow := false
			object := func(id *ast.Ident) types.Object {
				if o := info.Defs[id]; o != nil {
					return o
				}
				return info.Uses[id]
			}
			protect := func(n ast.Node) {
				ast.Inspect(n, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok {
						skip[object(id)] = true
					}
					return true
				})
			}
			if fn.Type.Results != nil {
				protect(fn.Type.Results)
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SliceExpr:
					if t := info.TypeOf(x.X); t != nil {
						if _, ok := t.Underlying().(*types.Array); ok {
							protect(x.X)
						}
					}
				case *ast.SelectorExpr:
					if sel := info.Selections[x]; sel != nil && sel.Kind() == types.MethodVal {
						if fn, ok := sel.Obj().(*types.Func); ok {
							if _, ok := fn.Type().(*types.Signature).Recv().Type().(*types.Pointer); ok {
								protect(x.X)
							}
						}
					}
				case *ast.AssignStmt:
					seen := map[types.Object]bool{}
					for _, e := range x.Lhs {
						if id, ok := ast.Unparen(e).(*ast.Ident); ok {
							o := object(id)
							if seen[o] {
								skip[o] = true
							}
							seen[o] = true
						}
					}
				case *ast.LabeledStmt:
					badFlow = true
				case *ast.BranchStmt:
					if x.Tok == token.GOTO || x.Tok == token.FALLTHROUGH {
						badFlow = true
					}
				case *ast.FuncLit:
					protect(x)
					return false
				case *ast.UnaryExpr:
					if x.Op == token.AND {
						protect(x.X)
					}
				case *ast.ForStmt:
					protect(x)
				case *ast.RangeStmt:
					protect(x)
				case *ast.SelectStmt:
					protect(x)
				case *ast.Ident:
					if v, ok := info.Defs[x].(*types.Var); ok && !v.IsField() && v.Pkg() != nil && v.Parent() != v.Pkg().Scope() && x.Name != "_" {
						locals[v] = true
					}
				}
				return true
			})
			if badFlow {
				continue
			}
			read := func(n ast.Node, l live) {
				if n == nil {
					return
				}
				ast.Inspect(n, func(n ast.Node) bool {
					if _, ok := n.(*ast.FuncLit); ok {
						return false
					}
					if id, ok := n.(*ast.Ident); ok {
						if o := info.Uses[id]; o != nil {
							l[o] = true
						}
					}
					return true
				})
			}
			write := func(id *ast.Ident, l live, zero bool) {
				o := object(id)
				if locals[o] && !skip[o] && !l[o] && !zero {
					k, f := finding(root, fs, id.Pos(), "ineffassign", id.Name)
					if within(root, fs.PositionFor(id.Pos(), false).Filename) {
						found[k] = f
					}
				}
				delete(l, o)
			}
			var stmt func(ast.Stmt, live, live) live
			var block func([]ast.Stmt, live, live) live
			block = func(stmts []ast.Stmt, l, br live) live {
				for i := len(stmts) - 1; i >= 0; i-- {
					l = stmt(stmts[i], l, br)
				}
				return l
			}
			stmt = func(s ast.Stmt, l, br live) live {
				switch x := s.(type) {
				case *ast.BlockStmt:
					return block(x.List, l, br)
				case *ast.ReturnStmt:
					l = live{}
					for _, e := range x.Results {
						read(e, l)
					}
					return l
				case *ast.BranchStmt:
					if x.Tok == token.BREAK {
						return copyLive(br)
					}
					return l
				case *ast.IfStmt:
					a := stmt(x.Body, copyLive(l), br)
					b := copyLive(l)
					if x.Else != nil {
						b = stmt(x.Else, b, br)
					}
					l = union(a, b)
					read(x.Cond, l)
					if x.Init != nil {
						l = stmt(x.Init, l, br)
					}
					return l
				case *ast.SwitchStmt, *ast.TypeSwitchStmt:
					var body *ast.BlockStmt
					var init, guard ast.Stmt
					var tag ast.Expr
					switch x := s.(type) {
					case *ast.SwitchStmt:
						body, init, tag = x.Body, x.Init, x.Tag
					case *ast.TypeSwitchStmt:
						body, init, guard = x.Body, x.Init, x.Assign
					}
					after := copyLive(l)
					paths := live{}
					hasDefault := false
					for _, c := range body.List {
						c := c.(*ast.CaseClause)
						if c.List == nil {
							hasDefault = true
						}
						branch := block(c.Body, copyLive(after), after)
						for _, e := range c.List {
							read(e, branch)
						}
						union(paths, branch)
					}
					if !hasDefault {
						union(paths, after)
					}
					read(tag, paths)
					// Guard := symbols are implicit per-clause objects, not local Defs.
					if guard != nil {
						paths = stmt(guard, paths, br)
					}
					if init != nil {
						paths = stmt(init, paths, br)
					}
					return paths
				case *ast.AssignStmt:
					// Parallel assignments evaluate all addresses and RHS before any stores.
					for i, e := range x.Lhs {
						if id, ok := ast.Unparen(e).(*ast.Ident); ok {
							zero := false
							if (x.Tok == token.ASSIGN || x.Tok == token.DEFINE) && len(x.Rhs) == len(x.Lhs) {
								zero = zeroValue(x.Rhs[i], info.TypeOf(id), info)
							}
							write(id, l, zero)
						}
					}
					for _, e := range x.Lhs {
						if _, ok := ast.Unparen(e).(*ast.Ident); !ok || (x.Tok != token.ASSIGN && x.Tok != token.DEFINE) {
							read(e, l)
						}
					}
					for _, e := range x.Rhs {
						read(e, l)
					}
				case *ast.IncDecStmt:
					if id, ok := ast.Unparen(x.X).(*ast.Ident); ok {
						write(id, l, false)
					}
					read(x.X, l)
				case *ast.DeclStmt:
					d, ok := x.Decl.(*ast.GenDecl)
					if !ok {
						return l
					}
					for i := len(d.Specs) - 1; i >= 0; i-- {
						v, ok := d.Specs[i].(*ast.ValueSpec)
						if !ok {
							continue
						}
						if len(v.Values) > 0 {
							for i, id := range v.Names {
								zero := len(v.Values) == len(v.Names) && zeroValue(v.Values[i], info.TypeOf(id), info)
								write(id, l, zero)
							}
						}
						for _, e := range v.Values {
							read(e, l)
						}
					}
				default:
					read(s, l)
				}
				return l
			}
			block(fn.Body.List, live{}, live{})
		}
	}
}

// Ignore explicit zero initialization, matching ineffassign's noise policy.
// Empty slices/maps and interfaces holding a typed zero are not zero values.
func zeroValue(e ast.Expr, target types.Type, info *types.Info) bool {
	if target == nil {
		return false
	}
	e = ast.Unparen(e)
	tv := info.Types[e]
	if tv.IsNil() {
		return true
	}
	if tv.Value != nil {
		if basic, ok := target.Underlying().(*types.Basic); ok {
			switch {
			case basic.Info()&types.IsBoolean != 0:
				return !constant.BoolVal(tv.Value)
			case basic.Info()&types.IsString != 0:
				return constant.StringVal(tv.Value) == ""
			case basic.Info()&types.IsNumeric != 0:
				return constant.Compare(tv.Value, token.EQL, constant.MakeInt64(0))
			}
		}
	}
	if lit, ok := e.(*ast.CompositeLit); ok && len(lit.Elts) == 0 {
		switch target.Underlying().(type) {
		case *types.Struct, *types.Array:
			return true
		}
	}
	if call, ok := e.(*ast.CallExpr); ok && len(call.Args) == 1 && info.Types[call.Fun].IsType() {
		arg := call.Args[0]
		if _, iface := target.Underlying().(*types.Interface); iface && !info.Types[arg].IsNil() {
			if t := info.TypeOf(arg); t != nil {
				if _, ok := t.Underlying().(*types.Interface); !ok {
					return false
				}
			}
		}
		return zeroValue(arg, target, info)
	}
	return false
}
