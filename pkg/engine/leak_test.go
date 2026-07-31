package engine

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"runtime"
	"strings"
	"testing"
)

// TestNoInternalLeak is AC-B1: no internal/ type may appear in an exported
// signature of pkg/engine. The check type-checks the package from source
// and stringifies every exported object's type — any internal reference
// shows up as a github.com/xoai/sage-wiki/internal/... path.
func TestNoInternalLeak(t *testing.T) {
	fset := token.NewFileSet()
	var files []*ast.File
	names := []string{"engine.go", "lock.go", "methods.go", "search.go", "events.go"}
	// Include the platform lock implementation matching the test host so
	// the package type-checks (build tags exclude the other).
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		names = append(names, "lock_other.go")
	} else {
		names = append(names, "lock_unix.go")
	}
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
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		sig := types.TypeString(obj.Type(), qualifier)
		if strings.Contains(sig, "sage-wiki/internal/") {
			t.Errorf("exported %s leaks internal type: %s", name, sig)
		}
	}
}
