package provider

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoErrorDiagnosticsWhilePlanning is the provider-wide guard for the rule that
// planning must never raise an ERROR diagnostic a resource that ALREADY EXISTS can
// trip.
//
// Terraform's destroy plan still runs a refresh phase that computes an ordinary,
// NON-null plan, and both attribute plan modifiers and a resource's ModifyPlan run
// against it — the framework only skips them when the planned state is null, which
// that phase is not. So an error raised there aborts `terraform destroy` as well as
// the apply it was meant to stop, and a practitioner whose live resource has drifted
// from the config (a portal-side volume grow, a moved deploy archive) is left with no
// way to destroy but to edit the HCL or pass -refresh=false. Reported by a pilot
// customer on 2026-09-06 for storage_gb; found in three more places when looked for.
//
// The refusal belongs in Create/Update/Delete, which a destroy never reaches. Warn
// while planning, fail while applying.
//
// THE ONE EXEMPTION is an error raised only when the PRIOR STATE IS NULL: a destroy
// plan always has prior state, so such an error cannot block one, and Terraform plans
// the create half of a replacement as a separate call with no prior state — which is
// the only place a refusal can stop a replacement BEFORE it destroys the resource
// (kubernetes_cluster's retired public_ip_id). Guard the call with
// `req.State.Raw.IsNull()` and this scan allows it.
//
// A per-resource test cannot pin this: the trap is one call away in any of ~50
// resources, and it reads like ordinary defensive code. Hence the source scan.
func TestNoErrorDiagnosticsWhilePlanning(t *testing.T) {
	roots := []string{".."} // every package under internal/
	seen := map[string]bool{}

	for _, root := range roots {
		dirs := map[string][]string{}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			abs, aerr := filepath.Abs(p)
			if aerr != nil {
				return aerr
			}
			if seen[abs] {
				return nil
			}
			seen[abs] = true
			dirs[filepath.Dir(abs)] = append(dirs[filepath.Dir(abs)], abs)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
		for _, files := range dirs {
			checkPackage(t, files)
		}
	}
}

// checkPackage scans one directory: it first collects the names of same-package
// helpers that raise an error diagnostic (secret's addCreateTimeRefusals is one),
// then flags plan-time functions that reach a diagnostic error either directly or
// through one of those helpers — the indirection is otherwise a free way back into
// the bug.
func checkPackage(t *testing.T, files []string) {
	t.Helper()
	fset := token.NewFileSet()
	parsed := make([]*ast.File, 0, len(files))
	for _, p := range files {
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		parsed = append(parsed, f)
	}

	errorHelpers := map[string]bool{}
	for _, f := range parsed {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv != nil || isPlanTimeFunc(fn.Name.Name) {
				continue
			}
			if raisesDiagError(fn.Body) {
				errorHelpers[fn.Name.Name] = true
			}
		}
	}

	for _, f := range parsed {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !isPlanTimeFunc(fn.Name.Name) {
				continue
			}
			exempt := createOnlyRanges(fset, fn.Body)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				what := ""
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if isDiagError(fun.Sel.Name) {
						what = fun.Sel.Name
					}
				case *ast.Ident:
					if errorHelpers[fun.Name] {
						what = fun.Name + " (which raises an error diagnostic)"
					}
				}
				if what == "" {
					return true
				}
				for _, rng := range exempt {
					if call.Pos() >= rng[0] && call.End() <= rng[1] {
						return true
					}
				}
				t.Errorf("%s: %s calls %s while planning — an error diagnostic here also blocks "+
					"`terraform destroy`. Warn here and refuse in Create/Update, or guard the call "+
					"with req.State.Raw.IsNull() if it may only fire on a create.",
					fset.Position(call.Pos()), fn.Name.Name, what)
				return true
			})
		}
	}
}

func isDiagError(name string) bool {
	return name == "AddError" || name == "AddAttributeError"
}

func raisesDiagError(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && isDiagError(sel.Sel.Name) {
				found = true
			}
		}
		return !found
	})
	return found
}

// createOnlyRanges returns the source ranges of `if <...>State.Raw.IsNull() {...}`
// bodies — the create-only arm, which a destroy plan never enters.
func createOnlyRanges(fset *token.FileSet, body *ast.BlockStmt) [][2]token.Pos {
	var ranges [][2]token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		var cond strings.Builder
		if err := printer.Fprint(&cond, fset, ifStmt.Cond); err != nil {
			return true
		}
		if strings.Contains(cond.String(), "State.Raw.IsNull()") && !strings.Contains(cond.String(), "!") {
			ranges = append(ranges, [2]token.Pos{ifStmt.Body.Pos(), ifStmt.Body.End()})
		}
		return true
	})
	return ranges
}

// isPlanTimeFunc reports whether a function runs while Terraform is planning:
// a resource's ModifyPlan, or a plan modifier's PlanModify<Type> method.
func isPlanTimeFunc(name string) bool {
	return name == "ModifyPlan" || strings.HasPrefix(name, "PlanModify")
}
