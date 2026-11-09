// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package architecture

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
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
	"testing"
)

// contextRootRule prefixes every finding so a failure names the guard that raised it.
const contextRootRule = "context-root"

// contextRoot names a production function that creates a root context with
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
	// The conformance producer recovers results, cleans up and observes the
	// environment after its canceled suite is joined.
	boundedRecovery = "bounded recovery and cleanup: runs after the canceled suite is joined, each step with its own deadline"
)

// contextRootExceptions is the exact inventory of allowed root contexts. Entries
// must match the tree exactly: a new site, a changed count, or a stale entry fails.
var contextRootExceptions = map[contextRoot]contextRootException{
	{"cmd/nvke-demo/main.go", "main", "Background"}:                                                                   {1, processRoot},
	{"cmd/nvkr-disk-inspect/main.go", "main", "Background"}:                                                           {1, processRoot},
	{"cmd/nvkr-results-publish/main.go", "main", "Background"}:                                                        {1, processRoot},
	{"cmd/nvkr-site-identity-helper/main.go", "main", "Background"}:                                                   {1, processRoot},
	{"cmd/nvkr/main.go", "main", "Background"}:                                                                        {1, processRoot},
	{"internal/nvkedemo/support.go", "(*OwnedProcess).finish", "Background"}:                                          {3, boundedCleanup},
	{"pkg/nvkr/conformance/run.go", "(*Producer).produce", "Background"}:                                              {3, boundedRecovery},
	{"pkg/nvkr/factory/utility_runner.go", "executeUtilityNode", "Background"}:                                        {1, boundedCleanup},
	{"pkg/nvkr/pipeline/container_lifecycle.go", "(*reaper).cleanup", "Background"}:                                   {1, boundedCleanup},
	{"pkg/nvkr/pipeline/disk_inspection_runtime.go", "runDiskInspectionContainerWithCleanupTimeout", "Background"}:    {1, boundedCleanup},
	{"pkg/nvkr/pipeline/disk_inspection_scratch.go", "(*diskInspectionScratch).Close", "Background"}:                  {1, boundedCleanup},
	{"pkg/nvkr/pipeline/disk_inspection_scratch.go", "openDiskInspectionScratch", "Background"}:                       {1, boundedCleanup},
	{"pkg/nvkr/pipeline/packer_container_runtime.go", "(*packerContainerResources).cleanupWithTimeout", "Background"}: {2, boundedCleanup},
	{"pkg/nvkr/pipeline/tool_smoke_container.go", "runToolSmokeContainer", "Background"}:                              {1, boundedCleanup},
}

// contextRootBuildConfigs covers every build constraint used by production files,
// including `ignore`-tagged generators. A file selected by none of them fails the
// check instead of going unscanned.
var contextRootBuildConfigs = []struct {
	goos, goarch string
	cgo          bool
	tags         []string
}{
	{"linux", "amd64", false, nil},
	{"darwin", "arm64", true, nil},
	{"darwin", "arm64", false, nil},
	{"freebsd", "amd64", false, nil},
	{"windows", "amd64", false, nil},
	{"linux", "amd64", false, []string{"ignore"}},
}

func TestContextRootsMatchExactInventory(t *testing.T) {
	found, err := findModuleContextRoots(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range contextRootViolations(found, contextRootExceptions) {
		t.Error(violation)
	}
}

func TestContextRootViolations(t *testing.T) {
	site := contextRoot{"pkg/p/p.go", "Run", "Background"}
	other := contextRoot{"pkg/p/q.go", "Wait", "TODO"}
	for _, test := range []struct {
		name       string
		found      map[contextRoot][]string
		exceptions map[contextRoot]contextRootException
		want       []string
	}{
		{
			name:       "exact match",
			found:      map[contextRoot][]string{site: {"pkg/p/p.go:3:9"}},
			exceptions: map[contextRoot]contextRootException{site: {1, boundedCleanup}},
		},
		{
			name:  "new site",
			found: map[contextRoot][]string{site: {"pkg/p/p.go:3:9"}},
			want:  []string{"context-root: pkg/p/p.go:3:9: Run calls context.Background 1 time(s)"},
		},
		{
			name:       "count changed",
			found:      map[contextRoot][]string{site: {"pkg/p/p.go:3:9", "pkg/p/p.go:7:9"}},
			exceptions: map[contextRoot]contextRootException{site: {1, boundedCleanup}},
			want:       []string{"context-root: pkg/p/p.go:3:9, pkg/p/p.go:7:9: Run calls context.Background 2 time(s); inventory allows exactly 1"},
		},
		{
			name:       "stale exception",
			exceptions: map[contextRoot]contextRootException{site: {1, boundedCleanup}},
			want:       []string{"context-root: pkg/p/p.go: Run context.Background exception is stale"},
		},
		{
			name:       "missing reason",
			found:      map[contextRoot][]string{site: {"pkg/p/p.go:3:9"}},
			exceptions: map[contextRoot]contextRootException{site: {1, ""}},
			want:       []string{"context-root: pkg/p/p.go: Run exception has no reason"},
		},
		{
			name:  "sorted",
			found: map[contextRoot][]string{other: {"pkg/p/q.go:4:2"}, site: {"pkg/p/p.go:3:9"}},
			want: []string{
				"context-root: pkg/p/p.go:3:9: Run calls context.Background 1 time(s)",
				"context-root: pkg/p/q.go:4:2: Wait calls context.TODO 1 time(s)",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := contextRootViolations(test.found, test.exceptions)
			if len(got) != len(test.want) {
				t.Fatalf("violations = %q; want prefixes %q", got, test.want)
			}
			for index := range got {
				if !strings.HasPrefix(got[index], test.want[index]) {
					t.Fatalf("violation %d = %q; want prefix %q", index, got[index], test.want[index])
				}
			}
		})
	}
}

// contextRootViolations compares found root contexts with the exception inventory
// and returns one sorted message per difference.
func contextRootViolations(found map[contextRoot][]string, exceptions map[contextRoot]contextRootException) []string {
	sites := make([]contextRoot, 0, len(found)+len(exceptions))
	for site := range found {
		sites = append(sites, site)
	}
	for site := range exceptions {
		if _, ok := found[site]; !ok {
			sites = append(sites, site)
		}
	}
	sort.Slice(sites, func(i, j int) bool {
		left, right := sites[i], sites[j]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.Function != right.Function {
			return left.Function < right.Function
		}
		return left.Symbol < right.Symbol
	})
	var violations []string
	for _, site := range sites {
		positions, present := found[site]
		allowed, listed := exceptions[site]
		where := strings.Join(positions, ", ")
		switch {
		case listed && allowed.Reason == "":
			violations = append(violations, fmt.Sprintf("%s: %s: %s exception has no reason", contextRootRule, site.File, site.Function))
		case !present:
			violations = append(violations, fmt.Sprintf("%s: %s: %s context.%s exception is stale; remove it from contextRootExceptions",
				contextRootRule, site.File, site.Function, site.Symbol))
		case !listed:
			violations = append(violations, fmt.Sprintf("%s: %s: %s calls context.%s %d time(s); pass the caller's context instead, "+
				"or add an exact process-root or bounded-cleanup entry with its reason to contextRootExceptions",
				contextRootRule, where, site.Function, site.Symbol, len(positions)))
		case allowed.Count != len(positions):
			violations = append(violations, fmt.Sprintf("%s: %s: %s calls context.%s %d time(s); inventory allows exactly %d (%s)",
				contextRootRule, where, site.Function, site.Symbol, len(positions), allowed.Count, allowed.Reason))
		}
	}
	return violations
}

func TestFindModuleContextRootsScansWhatGoBuilds(t *testing.T) {
	root := t.TempDir()
	write := func(name, source string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const leak = "import \"context\"\nfunc Leak() { _ = context.Background() }\n"
	write("go.mod", "module example.com/m\n")
	write("p/p.go", "package p\n"+leak)
	write("p/linux.go", "//go:build linux\n\npackage p\nimport \"context\"\nfunc Linux() { _ = context.TODO() }\n")
	write("p/generate.go", "//go:build ignore\n\npackage main\n"+leak)
	write("p/p_test.go", "package p\n"+leak)
	write("p/_draft.go", "package p\n"+leak)
	write("_attic/a.go", "package attic\n"+leak)
	write(".hidden/h.go", "package hidden\n"+leak)
	write("p/testdata/t.go", "package t\n"+leak)
	write("nested/go.mod", "module example.com/nested\n")
	write("nested/n.go", "package nested\n"+leak)
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "p", ".#p.go")); err != nil {
		t.Fatal(err)
	}
	found, err := findModuleContextRoots(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[contextRoot][]string{
		{"p/generate.go", "Leak", "Background"}: {"p/generate.go:5:27"},
		{"p/linux.go", "Linux", "TODO"}:         {"p/linux.go:5:28"},
		{"p/p.go", "Leak", "Background"}:        {"p/p.go:3:27"},
	}
	if fmt.Sprint(found) != fmt.Sprint(want) {
		t.Fatalf("found = %v; want %v", found, want)
	}

	write("p/never.go", "//go:build nvkr_never\n\npackage p\n")
	if _, err := findModuleContextRoots(root); err == nil || !strings.Contains(err.Error(), "context-root: p/never.go matches no build configuration") {
		t.Fatalf("unselected file error = %v", err)
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
			name: "unchecked dot import",
			source: `package p
import . "context"
func Use() { _ = Background() }`,
			extra:   "package q\n",
			wantErr: "cannot resolve Background",
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

// findModuleContextRoots scans every non-test Go file that the go command would
// build in the module at root, under each build configuration, and returns the
// distinct root-context positions per function.
func findModuleContextRoots(root string) (map[contextRoot][]string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	relative := func(name string) string {
		if rel, err := filepath.Rel(root, name); err == nil {
			return filepath.ToSlash(rel)
		}
		return name
	}
	directories := map[string][]string{}
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if current == root {
			return nil
		}
		// The go command ignores names starting with "." or "_" and testdata.
		ignored := strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
		if entry.IsDir() {
			if ignored || name == "testdata" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
				return filepath.SkipDir // nested module
			}
			return nil
		}
		if !ignored && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			directories[filepath.Dir(current)] = append(directories[filepath.Dir(current)], name)
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
			context.GOOS, context.GOARCH, context.CgoEnabled, context.BuildTags = config.goos, config.goarch, config.cgo, config.tags
			context.OpenFile = func(path string) (io.ReadCloser, error) {
				source, ok := sources[path]
				if !ok {
					return nil, fmt.Errorf("unexpected build-constraint read of %s", path)
				}
				return io.NopCloser(bytes.NewReader(source)), nil
			}
			// Group by package clause: an ignore-tagged generator is its own main package.
			packages := map[string][]*ast.File{}
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
				packages[parsed[name].Name.Name] = append(packages[parsed[name].Name.Name], parsed[name])
			}
			for _, files := range packages {
				sites, err := packageContextRoots(fset, files)
				if err != nil {
					return nil, err
				}
				for _, site := range sites {
					seen[site.pos] = site
				}
			}
		}
		for _, name := range names {
			if !covered[name] {
				return nil, fmt.Errorf("%s: %s matches no build configuration; add one that selects it to contextRootBuildConfigs",
					contextRootRule, relative(filepath.Join(directory, name)))
			}
		}
	}
	found := map[contextRoot][]string{}
	for _, site := range seen {
		file := relative(site.pos.Filename)
		key := contextRoot{file, site.function, site.symbol}
		found[key] = append(found[key], fmt.Sprintf("%s:%d:%d", file, site.pos.Line, site.pos.Column))
	}
	for key := range found {
		sort.Strings(found[key])
	}
	return found, nil
}

// packageContextRoots type-checks one package's files so that scoping, aliases and
// dot imports resolve each identifier. Every import is a stand-in and the
// resulting type errors are ignored; any Background or TODO identifier that then
// remains unresolved is an error rather than an assumed non-match.
func packageContextRoots(fset *token.FileSet, files []*ast.File) ([]contextRootSite, error) {
	roots := map[types.Object]string{
		contextPackage.Scope().Lookup("Background"): "Background",
		contextPackage.Scope().Lookup("TODO"):       "TODO",
	}
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
	config := types.Config{
		Importer: contextOnlyImporter{},
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

// contextPackage stands in for the standard context package. The checker needs the
// identity of its two root constructors, not the real package's types.
var contextPackage = func() *types.Package {
	pkg := types.NewPackage("context", "context")
	name := types.NewTypeName(token.NoPos, pkg, "Context", nil)
	contextType := types.NewNamed(name, types.NewInterfaceType(nil, nil).Complete(), nil)
	pkg.Scope().Insert(name)
	result := types.NewTuple(types.NewVar(token.NoPos, pkg, "", contextType))
	for _, root := range []string{"Background", "TODO"} {
		pkg.Scope().Insert(types.NewFunc(token.NoPos, pkg, root, types.NewSignatureType(nil, nil, nil, nil, result, false)))
	}
	pkg.MarkComplete()
	return pkg
}()

type contextOnlyImporter struct{}

func (contextOnlyImporter) Import(importPath string) (*types.Package, error) {
	if importPath == "context" {
		return contextPackage, nil
	}
	stand := types.NewPackage(importPath, path.Base(importPath))
	stand.MarkComplete()
	return stand, nil
}
