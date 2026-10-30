package mobile

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// magpieSurface is the set of methods gomobile exports for Magpie. Both clients
// call these by name from another language, where a rename is not a compile
// error but a runtime surprise; adding or removing one has to be a deliberate
// edit to this list and to the clients.
var magpieSurface = []string{
	"Auth",
	"Authorize",
	"Call",
	"CancelSignIn",
	"ChatCancel",
	"ChatNext",
	"ChatStart",
	"Checkin",
	"Close",
	"LoadPackage",
	"Models",
	"PlanRoute",
	"Poll",
	"ReadPackageArchive",
	"SavedAuth",
	"SetAuth",
	"SetPluginOptions",
	"Usage",
	"UsageSummary",
}

func TestMagpieSurfaceIsWhatClientsCall(t *testing.T) {
	fset := token.NewFileSet()
	// Every file in the package, not just magpie.go: a method added to a second
	// file is just as much a gomobile export, and would otherwise slip past the
	// one check that exists to notice it.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			if !exported(fn) || fn.Name.Name == "NewMagpie" {
				continue
			}
			// Only methods on *Magpie are the gomobile surface.
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if ident, ok := star.X.(*ast.Ident); !ok || ident.Name != "Magpie" {
				continue
			}
			got = append(got, fn.Name.Name)
		}
	}
	sort.Strings(got)
	want := append([]string(nil), magpieSurface...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the Magpie surface changed.\n got: %v\nwant: %v\n\nA new method needs a client call site; a removed one breaks a built\nframework, so change this list on purpose.", got, want)
	}
}

func exported(fn *ast.FuncDecl) bool {
	return fn.Name.IsExported()
}
