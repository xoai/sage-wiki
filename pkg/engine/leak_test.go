package engine

import (
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// TestNoInternalLeak is AC-B1: no internal/ type may appear in an exported
// signature of pkg/engine. The check type-checks the package from source
// and stringifies every exported object's type — any internal reference
// shows up as a github.com/xoai/sage-wiki/internal/... path.
func TestNoInternalLeak(t *testing.T) {
	// Discover the package's files with build-tag awareness (go/build) —
	// a hardcoded list would let a newly added file escape the check.
	pkgDir, err := build.Default.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("ImportDir: %v", err)
	}
	names := append(append([]string{}, pkgDir.GoFiles...), pkgDir.CgoFiles...)

	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, f)
	}

	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("github.com/xoai/sage-wiki/pkg/engine", fset, files, nil)
	if err != nil {
		t.Fatalf("type-check: %v", err)
	}

	qualifier := func(p *types.Package) string { return p.Path() }
	check := func(label string, typ types.Type) {
		t.Helper()
		sig := types.TypeString(typ, qualifier)
		if strings.Contains(sig, "sage-wiki/internal/") {
			t.Errorf("exported %s leaks internal type: %s", label, sig)
		}
	}

	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		check(name, obj.Type())

		// Named types: walk exported struct fields and method signatures —
		// TypeString of a named type does not expand them (F-052).
		named, ok := obj.Type().(*types.Named)
		if !ok {
			continue
		}
		if st, ok := named.Underlying().(*types.Struct); ok {
			for i := 0; i < st.NumFields(); i++ {
				f := st.Field(i)
				if f.Exported() {
					check(name+"."+f.Name(), f.Type())
				}
			}
		}
		ms := types.NewMethodSet(named)
		for i := 0; i < ms.Len(); i++ {
			m := ms.At(i).Obj()
			if m.Exported() {
				check(name+"."+m.Name()+" (method)", m.Type())
			}
		}
	}
}
