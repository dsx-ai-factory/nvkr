// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package architecture

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// contextRoot names every production function that may create a root context with
// context.Background or context.TODO. Work paths receive their caller's context;
// a convenience wrapper or nil-context default would silently drop cancellation.
type contextRoot struct {
	File, Function, Symbol string
}

type contextRootException struct {
	Count  int
	Reason string
}

const (
	processRoot    = "process root: signal-bound context for the command's whole lifetime"
	boundedCleanup = "bounded cleanup: must outlive the canceled caller and owns its own deadline"
)

// contextRootExceptions is the exact inventory of allowed root contexts. Entries
// must match the tree exactly: a new site, a changed count, or a stale entry fails.
var contextRootExceptions = map[contextRoot]contextRootException{
	{"cmd/nvke-demo/main.go", "main", "Background"}:                                                                   {1, processRoot},
	{"cmd/nvkr-disk-inspect/main.go", "main", "Background"}:                                                           {1, processRoot},
	{"cmd/nvkr-site-identity-helper/main.go", "main", "Background"}:                                                   {1, processRoot},
	{"cmd/nvkr/main.go", "main", "Background"}:                                                                        {1, processRoot},
	{"internal/nvkedemo/support.go", "(*OwnedProcess).finish", "Background"}:                                          {3, boundedCleanup},
	{"pkg/nvkr/conformance/run.go", "(*Producer).produce", "Background"}:                                              {3, boundedCleanup},
	{"pkg/nvkr/factory/utility_runner.go", "executeUtilityNode", "Background"}:                                        {1, boundedCleanup},
	{"pkg/nvkr/pipeline/container_lifecycle.go", "(*reaper).cleanup", "Background"}:                                   {1, boundedCleanup},
	{"pkg/nvkr/pipeline/disk_inspection_runtime.go", "runDiskInspectionContainerWithCleanupTimeout", "Background"}:    {1, boundedCleanup},
	{"pkg/nvkr/pipeline/disk_inspection_scratch.go", "(*diskInspectionScratch).Close", "Background"}:                  {1, boundedCleanup},
	{"pkg/nvkr/pipeline/disk_inspection_scratch.go", "openDiskInspectionScratch", "Background"}:                       {1, boundedCleanup},
	{"pkg/nvkr/pipeline/packer_container_runtime.go", "(*packerContainerResources).cleanupWithTimeout", "Background"}: {2, boundedCleanup},
	{"pkg/nvkr/pipeline/tool_smoke_container.go", "runToolSmokeContainer", "Background"}:                              {1, boundedCleanup},
}

// contextRootBuildConfigs covers every build constraint used by production files.
// A file selected by none of them fails the check instead of going unscanned.
var contextRootBuildConfigs = []struct {
	goos, goarch string
	cgo          bool
}{
	{"linux", "amd64", false},
	{"darwin", "arm64", true},
	{"darwin", "arm64", false},
	{"freebsd", "amd64", false},
	{"windows", "amd64", false},
}

func TestContextRootsMatchExactInventory(t *testing.T) {
	found, err := findModuleContextRoots(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	sites := make([]contextRoot, 0, len(found)+len(contextRootExceptions))
	for site := range found {
		sites = append(sites, site)
	}
	for site := range contextRootExceptions {
		if _, ok := found[site]; !ok {
			sites = append(sites, site)
		}
	}
	sort.Slice(sites, func(i, j int) bool {
		left, right := sites[i], sites[j]
		return left.File+"\x00"+left.Function+"\x00"+left.Symbol < right.File+"\x00"+right.Function+"\x00"+right.Symbol
	})
	for _, site := range sites {
		count, present := found[site]
		allowed, ok := contextRootExceptions[site]
		if ok && allowed.Reason == "" {
			t.Errorf("%s: %s exception has no reason", site.File, site.Function)
		}
		switch {
		case !present:
			t.Errorf("%s: %s context.%s exception is stale; remove it from the inventory", site.File, site.Function, site.Symbol)
		case !ok:
			t.Errorf("%s: %s calls context.%s %d time(s); pass the caller's context instead, or add an exact bounded-cleanup or process-root exception with its reason",
				site.File, site.Function, site.Symbol, count)
		case allowed.Count != count:
			t.Errorf("%s: %s calls context.%s %d time(s); inventory allows exactly %d (%s)",
				site.File, site.Function, site.Symbol, count, allowed.Count, allowed.Reason)
		}
	}
}

func TestContextRootCheckerResolvesSymbols(t *testing.T) {
	for _, test := range []struct {
		name    string
		source  string
		extra   string // a mismatched first file makes the type checker skip source
		want    []string
		wantErr string
	}{
		{
			name: "return delegate",
			source: `package p
import "context"
func readContext(context.Context, string) error { return nil }
func Read(name string) error { return readContext(context.Background(), name) }`,
			want: []string{"Read Background"},
		},
		{
			name: "nil context default",
			source: `package p
import "context"
type Options struct{ Ctx context.Context }
func (o Options) ctx() context.Context {
	if o.Ctx == nil {
		return context.TODO()
	}
	return o.Ctx
}`,
			want: []string{"Options.ctx TODO"},
		},
		{
			name: "aliased import in closure",
			source: `package p
import stdctx "context"
type T struct{}
func (*T) Run() { go func() { _ = stdctx.Background() }() }`,
			want: []string{"(*T).Run Background"},
		},
		{
			name: "dot import",
			source: `package p
import . "context"
func Root() Context { return Background() }`,
			want: []string{"Root Background"},
		},
		{
			name: "function value",
			source: `package p
import "context"
var newRoot = context.Background`,
			want: []string{"<package-level> Background"},
		},
		{
			name: "unrelated symbols and text",
			source: `package p
import (
	"context"
	color "example.com/color"
)
type shadow struct{}
func (shadow) Background() int { return 0 }
// Never call context.Background() here.
func Use(ctx context.Context) (int, string) {
	context := shadow{}
	_ = color.Background
	return context.Background(), "context.TODO()"
}`,
		},
		{
			name: "member of unresolved value",
			source: `package p
import "example.com/remote"
func Use() { remote.New().Style.Background() }`,
		},
		{
			name: "unchecked context qualifier",
			source: `package p
import "context"
func Use() { _ = context.Background() }`,
			extra:   "package q\n",
			wantErr: "cannot resolve Background",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fset := token.NewFileSet()
			var files []*ast.File
			for name, source := range map[string]string{"extra.go": test.extra, "p.go": test.source} {
				if source == "" {
					continue
				}
				file, err := parser.ParseFile(fset, name, source, parser.SkipObjectResolution)
				if err != nil {
					t.Fatal(err)
				}
				files = append(files, file)
			}
			sort.Slice(files, func(i, j int) bool { return fset.File(files[i].Pos()).Name() < fset.File(files[j].Pos()).Name() })
			sites, err := packageContextRoots(fset, files)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("packageContextRoots() error = %v; want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(sites))
			for _, site := range sites {
				got = append(got, site.function+" "+site.symbol)
			}
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Fatalf("context roots = %q; want %q", got, test.want)
			}
		})
	}
}

type contextRootSite struct {
	pos              token.Position
	function, symbol string
}

// findModuleContextRoots scans every non-test Go file in the main module under
// each build configuration and counts distinct root-context uses per function.
func findModuleContextRoots(root string) (map[contextRoot]int, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	directories := map[string][]string{}
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if current != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			if current != root {
				if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
					return filepath.SkipDir // nested module
				}
			}
			return nil
		}
		if strings.HasSuffix(current, ".go") && !strings.HasSuffix(current, "_test.go") {
			directories[filepath.Dir(current)] = append(directories[filepath.Dir(current)], entry.Name())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	seen := map[token.Position]contextRootSite{}
	for directory, names := range directories {
		fset := token.NewFileSet()
		sources := map[string][]byte{}
		for _, name := range names {
			if sources[filepath.Join(directory, name)], err = os.ReadFile(filepath.Join(directory, name)); err != nil {
				return nil, err
			}
		}
		parsed := map[string]*ast.File{}
		covered := map[string]bool{}
		for _, config := range contextRootBuildConfigs {
			context := build.Default
			context.GOOS, context.GOARCH, context.CgoEnabled = config.goos, config.goarch, config.cgo
			context.OpenFile = func(path string) (io.ReadCloser, error) {
				source, ok := sources[path]
				if !ok {
					return nil, fmt.Errorf("unexpected build-constraint read of %s", path)
				}
				return io.NopCloser(bytes.NewReader(source)), nil
			}
			var files []*ast.File
			for _, name := range names {
				match, err := context.MatchFile(directory, name)
				if err != nil {
					return nil, err
				}
				if !match {
					continue
				}
				covered[name] = true
				if parsed[name] == nil {
					parsed[name], err = parser.ParseFile(fset, filepath.Join(directory, name), sources[filepath.Join(directory, name)], parser.SkipObjectResolution)
					if err != nil {
						return nil, err
					}
				}
				files = append(files, parsed[name])
			}
			if len(files) == 0 {
				continue
			}
			sites, err := packageContextRoots(fset, files)
			if err != nil {
				return nil, err
			}
			for _, site := range sites {
				seen[site.pos] = site
			}
		}
		for _, name := range names {
			if !covered[name] {
				return nil, fmt.Errorf("%s matches no context-root build configuration; extend contextRootBuildConfigs",
					filepath.Join(directory, name))
			}
		}
	}
	found := map[contextRoot]int{}
	for _, site := range seen {
		relative, err := filepath.Rel(root, site.pos.Filename)
		if err != nil {
			return nil, err
		}
		found[contextRoot{filepath.ToSlash(relative), site.function, site.symbol}]++
	}
	return found, nil
}

// packageContextRoots type-checks one package's files against the real context
// package. Other imports are empty stand-ins, so their type errors are ignored;
// any Background or TODO identifier that then remains unresolved is an error
// rather than an assumed non-match.
func packageContextRoots(fset *token.FileSet, files []*ast.File) ([]contextRootSite, error) {
	contextPackage, err := loadContextPackage()
	if err != nil {
		return nil, err
	}
	roots := map[types.Object]string{
		contextPackage.Scope().Lookup("Background"): "Background",
		contextPackage.Scope().Lookup("TODO"):       "TODO",
	}
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
	config := types.Config{
		Importer: contextOnlyImporter{context: contextPackage},
		Error:    func(error) {},
	}
	_, _ = config.Check(files[0].Name.Name, fset, files, info)

	var sites []contextRootSite
	for _, file := range files {
		qualifiers, dotImported := contextImportNames(file)
		selectorBase := map[*ast.Ident]ast.Expr{}
		ast.Inspect(file, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok {
				selectorBase[selector.Sel] = selector.X
			}
			return true
		})
		for _, declaration := range file.Decls {
			function := "<package-level>"
			if decl, ok := declaration.(*ast.FuncDecl); ok {
				function = functionName(decl)
			}
			var inspectErr error
			ast.Inspect(declaration, func(node ast.Node) bool {
				identifier, ok := node.(*ast.Ident)
				if inspectErr != nil || !ok || (identifier.Name != "Background" && identifier.Name != "TODO") {
					return inspectErr == nil
				}
				object := info.Uses[identifier]
				if object == nil {
					object = info.Defs[identifier]
				}
				if symbol, ok := roots[object]; ok {
					sites = append(sites, contextRootSite{fset.Position(identifier.Pos()), function, symbol})
					return true
				}
				if object != nil || !mayNameContextRoot(info, contextPackage, selectorBase[identifier], qualifiers, dotImported) {
					return true
				}
				inspectErr = fmt.Errorf("%s: cannot resolve %s; the checker cannot prove it is not a context root",
					fset.Position(identifier.Pos()), identifier.Name)
				return false
			})
			if inspectErr != nil {
				return nil, inspectErr
			}
		}
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].pos.Offset < sites[j].pos.Offset })
	return sites, nil
}

// contextImportNames returns the file's qualifiers for the context package and
// whether it dot-imports it.
func contextImportNames(file *ast.File) (map[string]bool, bool) {
	qualifiers, dotImported := map[string]bool{}, false
	for _, spec := range file.Imports {
		if spec.Path.Value != `"context"` {
			continue
		}
		switch {
		case spec.Name == nil:
			qualifiers["context"] = true
		case spec.Name.Name == ".":
			dotImported = true
		case spec.Name.Name != "_":
			qualifiers[spec.Name.Name] = true
		}
	}
	return qualifiers, dotImported
}

// mayNameContextRoot reports whether an unresolved Background or TODO identifier
// could still be the context package's function. Only a context qualifier or a dot
// import can reach it; any other selector names a different value's member.
func mayNameContextRoot(info *types.Info, contextPackage *types.Package, selectorX ast.Expr, qualifiers map[string]bool, dotImported bool) bool {
	if selectorX == nil {
		return dotImported
	}
	base, ok := selectorX.(*ast.Ident)
	if !ok || !qualifiers[base.Name] {
		return false
	}
	object := info.Uses[base]
	if object == nil {
		return true // the qualifier itself went unchecked
	}
	name, ok := object.(*types.PkgName)
	return ok && name.Imported() == contextPackage
}

func functionName(decl *ast.FuncDecl) string {
	if decl.Recv == nil || len(decl.Recv.List) == 0 {
		return decl.Name.Name
	}
	receiver := decl.Recv.List[0].Type
	if index, ok := receiver.(*ast.IndexExpr); ok {
		receiver = index.X
	}
	if index, ok := receiver.(*ast.IndexListExpr); ok {
		receiver = index.X
	}
	if star, ok := receiver.(*ast.StarExpr); ok {
		base := star.X
		if index, ok := base.(*ast.IndexExpr); ok {
			base = index.X
		}
		if index, ok := base.(*ast.IndexListExpr); ok {
			base = index.X
		}
		return "(*" + types.ExprString(base) + ")." + decl.Name.Name
	}
	return types.ExprString(receiver) + "." + decl.Name.Name
}

var loadContextPackage = sync.OnceValues(func() (*types.Package, error) {
	return importer.ForCompiler(token.NewFileSet(), "source", nil).Import("context")
})

type contextOnlyImporter struct{ context *types.Package }

func (importer contextOnlyImporter) Import(importPath string) (*types.Package, error) {
	if importPath == "context" {
		return importer.context, nil
	}
	stand := types.NewPackage(importPath, path.Base(importPath))
	stand.MarkComplete()
	return stand, nil
}
