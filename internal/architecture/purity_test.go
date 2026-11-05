// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package architecture hosts the ADR-02 §4 static purity guard: identity-producing
// packages must not reach wall-clock reads, randomness, UUID generation, hostname, or
// host-environment reads, because a leak there would make buildDigest host-dependent.
// The guard type-checks those packages. Their imports must be on an exact allowlist,
// and resolved references to clock and host functions are rejected even when an
// allowlisted import would otherwise admit them, so import aliases, dot imports and
// function values cannot hide a forbidden source and comments cannot trip it. It does
// not follow calls into imported packages. The empirical invariance test lives in
// pkg/nvkr/ir (TestDigestHostInvariance).
package architecture

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
)

const modulePath = "github.com/dsx-ai-factory/nvkr"

// identityPackages are the roots of the package trees whose output flows into a
// digest-addressed identity (buildDigest / builder-profile / trust objects). Every
// package in those trees MUST be pure.
var identityPackages = []string{
	modulePath + "/pkg/nvkr/buildgraph",
	modulePath + "/pkg/nvkr/canonical",
	modulePath + "/pkg/nvkr/internal/sourceenv",
	modulePath + "/pkg/nvkr/ir",
	modulePath + "/pkg/nvkr/target",
}

// allowedIdentityImports is the exact set of packages identity packages may import.
// Clock, randomness, UUID, host and unsafe packages are absent. Adding an entry is a
// reviewed decision; it does not prove the imported package pure.
var allowedIdentityImports = map[string]bool{
	"bytes":            true,
	"crypto/sha256":    true,
	"encoding/hex":     true,
	"encoding/json":    true,
	"fmt":              true,
	"io":               true,
	"io/fs":            true,
	"maps":             true,
	"math/big":         true,
	"net/url":          true,
	"path":             true,
	"reflect":          true,
	"regexp":           true,
	"slices":           true,
	"sort":             true,
	"strconv":          true,
	"strings":          true,
	"unicode":          true,
	"unicode/utf16":    true,
	"unicode/utf8":     true,
	"sigs.k8s.io/yaml": true,
	"github.com/google/go-containerregistry/pkg/name": true,
	modulePath + "/pkg/nvkr/canonical":                true,
	modulePath + "/pkg/nvkr/catalog":                  true,
	modulePath + "/pkg/nvkr/definition":               true,
	modulePath + "/pkg/nvkr/errors":                   true,
	modulePath + "/pkg/nvkr/execution":                true,
	modulePath + "/pkg/nvkr/internal/packertransport": true,
	modulePath + "/pkg/nvkr/internal/safetext":        true,
	modulePath + "/pkg/nvkr/internal/sourceenv":       true,
	modulePath + "/pkg/nvkr/ir":                       true,
	modulePath + "/pkg/nvkr/material":                 true,
	modulePath + "/pkg/nvkr/target":                   true,
}

// forbiddenSymbols are package-level functions and variables that observe the clock or
// the host. Any resolved reference is a violation, whether called or taken as a value,
// even if its package is later allowlisted. Methods such as Time.Local are kept out by
// the import allowlist instead.
var forbiddenSymbols = map[string]bool{
	"os.Environ":      true,
	"os.ExpandEnv":    true,
	"os.Getenv":       true,
	"os.Getwd":        true,
	"os.Hostname":     true,
	"os.LookupEnv":    true,
	"os.TempDir":      true,
	"os.UserHomeDir":  true,
	"syscall.Environ": true,
	"syscall.Getenv":  true,
	"time.After":      true,
	"time.AfterFunc":  true,
	"time.Local":      true,
	"time.NewTicker":  true,
	"time.NewTimer":   true,
	"time.Now":        true,
	"time.Since":      true,
	"time.Tick":       true,
	"time.Until":      true,
}

// purityException permits one finding ("import <path>" or a forbidden symbol) in one
// file of one package.
type purityException struct {
	pkg, file, symbol, reason string
}

// purityExceptions is the only per-site allowlist. Each entry needs a reason and must
// still match a finding; there is no inline source marker.
var purityExceptions []purityException

type purityFinding struct {
	pkg, file, symbol string
	pos               token.Position
}

func TestIdentityPathsArePure(t *testing.T) {
	root := filepath.Join("..", "..")
	packages, err := identityPackagePaths(root, identityPackages)
	if err != nil {
		t.Fatal(err)
	}
	listed := goListExports(t, root, packages...)
	fset := token.NewFileSet()
	imp := exportImporter(fset, listed)
	var findings []purityFinding
	checked := map[string]bool{}
	for _, pkg := range listed {
		if pkg.DepOnly {
			continue
		}
		// Test files are not checked, so only excluded non-test files leave a gap.
		ignored := slices.DeleteFunc(slices.Clone(pkg.IgnoredGoFiles), func(name string) bool { return strings.HasSuffix(name, "_test.go") })
		if len(ignored) > 0 || len(pkg.CgoFiles) > 0 {
			t.Errorf("%s: files outside the checked build configuration: ignored %v, cgo %v", pkg.ImportPath, ignored, pkg.CgoFiles)
		}
		var files []*ast.File
		for _, name := range pkg.GoFiles {
			file, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, file)
		}
		pkgFindings, err := purityFindings(pkg.ImportPath, fset, files, imp, allowedIdentityImports)
		if err != nil {
			t.Fatal(err)
		}
		findings = append(findings, pkgFindings...)
		checked[pkg.ImportPath] = true
	}
	for _, pkg := range uncheckedPackages(slices.Concat(identityPackages, packages), checked) {
		t.Errorf("identity package %s was not checked", pkg)
	}
	for _, problem := range applyPurityExceptions(findings, purityExceptions) {
		t.Error(problem)
	}
}

// uncheckedPackages returns the required packages absent from checked, once each in
// first-seen order; a root that is also a discovered package is required twice.
func uncheckedPackages(required []string, checked map[string]bool) []string {
	var missing []string
	for _, pkg := range required {
		if !checked[pkg] && !slices.Contains(missing, pkg) {
			missing = append(missing, pkg)
		}
	}
	return missing
}

func TestUncheckedPackages(t *testing.T) {
	required := []string{"root", "root/sub", "root"}
	tests := []struct {
		name    string
		checked map[string]bool
		want    []string
	}{
		{name: "complete inventory", checked: map[string]bool{"root": true, "root/sub": true}},
		{name: "discovered package missing", checked: map[string]bool{"root": true, "other": true}, want: []string{"root/sub"}},
		{name: "root missing", checked: map[string]bool{"root/sub": true}, want: []string{"root"}},
		{name: "nothing checked", want: []string{"root", "root/sub"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uncheckedPackages(required, tt.checked); !slices.Equal(got, tt.want) {
				t.Errorf("uncheckedPackages = %q, want %q", got, tt.want)
			}
		})
	}
}

// identityPackagePaths walks each tree under the module root and returns the import
// path of every directory holding a non-test Go file, sorted. go list runs in a child
// the test cache cannot observe; walking here makes an added file or package invalidate
// a cached pass. Listing explicit packages instead of "tree/..." patterns also keeps go
// list from needing every module in the build list.
func identityPackagePaths(root string, trees []string) ([]string, error) {
	var packages []string
	for _, tree := range trees {
		dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(tree, modulePath+"/")))
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := entry.Name()
			// Match the go tool's "..." rules: skip testdata and _ or . prefixed names.
			if path != dir && (name == "testdata" || strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".")) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			if pkg := modulePath + "/" + filepath.ToSlash(rel); !slices.Contains(packages, pkg) {
				packages = append(packages, pkg)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(packages)
	return packages, nil
}

func TestIdentityPackageDiscovery(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"pkg/a/a.go",
		"pkg/a/a2.go",
		"pkg/a/sub/sub.go",
		"pkg/a/sub/deeper/deeper.go",
		"pkg/a/onlytests/x_test.go",
		"pkg/a/notgo/README.md",
		"pkg/a/testdata/fixture.go",
		"pkg/a/_hidden/h.go",
		"pkg/a/.dot/d.go",
		"pkg/a/skipfile/_skip.go",
		"pkg/a/skipfile/.skip.go",
		"pkg/b/b_windows.go",
		"pkg/c/c.go",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := identityPackagePaths(root, []string{modulePath + "/pkg/b", modulePath + "/pkg/a"})
	if err != nil {
		t.Fatal(err)
	}
	// pkg/b is listed so go list reports its build-constrained file as an error.
	want := []string{modulePath + "/pkg/a", modulePath + "/pkg/a/sub", modulePath + "/pkg/a/sub/deeper", modulePath + "/pkg/b"}
	if !slices.Equal(got, want) {
		t.Errorf("packages = %q, want %q", got, want)
	}
	if _, err := identityPackagePaths(root, []string{modulePath + "/pkg/missing"}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing tree error = %v, want fs.ErrNotExist", err)
	}
}

// TestPurityRuleMatrix pins the ADR-02 guard against the counterexamples that defeated
// the previous line-substring matcher, plus the import allowlist and exception contract.
func TestPurityRuleMatrix(t *testing.T) {
	const pkgPath = "example.test/p"
	exception := func(symbol, reason string) []purityException {
		return []purityException{{pkg: pkgPath, file: "p.go", symbol: symbol, reason: reason}}
	}
	type matrixCase struct {
		name       string
		src        string
		allowed    []string // nil admits time and os, isolating the symbol rule
		exceptions []purityException
		want       []string
	}
	tests := []matrixCase{
		{name: "direct call", src: `import "time"; var x = time.Now()`, want: []string{"p.go:2:29: time.Now"}},
		{name: "import alias", src: `import clock "time"; var x = clock.Now()`, want: []string{"p.go:2:36: time.Now"}},
		{name: "function value", src: `import "time"; var now = time.Now; var x = now()`, want: []string{"p.go:2:31: time.Now"}},
		{name: "dot import", src: `import . "time"; var x = Now()`, want: []string{"p.go:2:26: time.Now"}},
		{name: "host environment", src: `import "os"; var x, _ = os.LookupEnv("HOME")`, want: []string{"p.go:2:28: os.LookupEnv"}},
		{name: "retired inline marker", src: `import "time"; var x = time.Now() //nvkr:allow-nondeterminism`, want: []string{"p.go:2:29: time.Now"}},
		{name: "comment only", src: "\n// Never call time.Now() in identity code."},
		{name: "shadowed identifier", src: `type clock struct{}; func (clock) Now() int { return 0 }; var time clock; var x = time.Now()`},
		{name: "deterministic time API", src: `import "time"; var x = time.Unix(0, 0).UTC()`},
		{name: "import outside allowlist", src: `import "time"; var x = time.Unix(0, 0).Local()`, allowed: []string{},
			want: []string{"p.go:2:8: import time"}},
		{name: "aliased import outside allowlist", src: `import r "math/rand"; var x = r.Int()`, allowed: []string{},
			want: []string{"p.go:2:10: import math/rand"}},
		{name: "blank import outside allowlist", src: `import _ "crypto/rand"`, allowed: []string{},
			want: []string{"p.go:2:10: import crypto/rand"}},
		{name: "allowlisted import", src: `import "strings"; var x = strings.ToUpper("a")`, allowed: []string{"strings"}},
		{name: "exact exception", src: `import "time"; var x = time.Now()`, exceptions: exception("time.Now", "fixture reason")},
		{name: "import exception", src: `import "time"; var x = time.Unix(0, 0)`, allowed: []string{},
			exceptions: exception("import time", "fixture reason")},
		{name: "exception without reason", src: `import "time"; var x = time.Now()`, exceptions: exception("time.Now", " "),
			want: []string{`exception example.test/p p.go time.Now has no reason`}},
		{name: "unused exception", src: `var x = 1`, exceptions: exception("time.Now", "fixture reason"),
			want: []string{`exception example.test/p p.go time.Now matches no finding`}},
		{name: "exception for another symbol", src: `import "time"; var x = time.Since(time.Time{})`, exceptions: exception("time.Now", "fixture reason"),
			want: []string{"p.go:2:29: time.Since", `exception example.test/p p.go time.Now matches no finding`}},
	}
	// Every forbidden name must resolve to a real package-level object; a misspelled key
	// would otherwise disable itself silently.
	for _, symbol := range slices.Sorted(maps.Keys(forbiddenSymbols)) {
		pkg, name, _ := strings.Cut(symbol, ".")
		src := fmt.Sprintf("import %q; var _ = %s.%s", pkg, pkg, name)
		tests = append(tests, matrixCase{name: "resolves " + symbol, src: src, allowed: []string{pkg},
			want: []string{fmt.Sprintf("p.go:2:%d: %s", len(src)-len(name)+1, symbol)}})
	}
	listed := goListExports(t, ".", "crypto/rand", "math/rand", "os", "strings", "syscall", "time")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			allowed := map[string]bool{"time": true, "os": true}
			if tc.allowed != nil {
				allowed = map[string]bool{}
				for _, path := range tc.allowed {
					allowed[path] = true
				}
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "p.go", "package p\n"+tc.src, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			findings, err := purityFindings(pkgPath, fset, []*ast.File{file}, exportImporter(fset, listed), allowed)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, problem := range applyPurityExceptions(findings, tc.exceptions) {
				got = append(got, strings.SplitN(problem, " in an identity package", 2)[0])
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("problems = %q, want %q", got, tc.want)
			}
		})
	}
}

// purityFindings type-checks one package and returns its imports outside allowed and
// its resolved references to forbidden symbols, sorted by position.
func purityFindings(pkgPath string, fset *token.FileSet, files []*ast.File, imp types.Importer, allowed map[string]bool) ([]purityFinding, error) {
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	if _, err := (&types.Config{Importer: imp}).Check(pkgPath, fset, files, info); err != nil {
		return nil, fmt.Errorf("type-check %s: %w", pkgPath, err)
	}
	var findings []purityFinding
	add := func(pos token.Pos, symbol string) {
		position := fset.Position(pos)
		findings = append(findings, purityFinding{pkg: pkgPath, file: filepath.Base(position.Filename), symbol: symbol, pos: position})
	}
	for _, file := range files {
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return nil, err
			}
			if !allowed[path] {
				add(spec.Path.Pos(), "import "+path)
			}
		}
	}
	for ident, obj := range info.Uses {
		if obj.Pkg() == nil || obj.Parent() != obj.Pkg().Scope() {
			continue // local, universe, or method object
		}
		if symbol := obj.Pkg().Path() + "." + obj.Name(); forbiddenSymbols[symbol] {
			add(ident.Pos(), symbol)
		}
	}
	slices.SortFunc(findings, func(a, b purityFinding) int {
		return cmp.Or(strings.Compare(a.pos.Filename, b.pos.Filename), cmp.Compare(a.pos.Line, b.pos.Line), cmp.Compare(a.pos.Column, b.pos.Column))
	})
	return findings, nil
}

// applyPurityExceptions returns every finding no exception covers, then every exception
// that lacks a reason or covers nothing.
func applyPurityExceptions(findings []purityFinding, exceptions []purityException) []string {
	used := make([]bool, len(exceptions))
	var problems []string
	for _, finding := range findings {
		i := slices.IndexFunc(exceptions, func(e purityException) bool {
			return e.pkg == finding.pkg && e.file == finding.file && e.symbol == finding.symbol
		})
		if i < 0 {
			problems = append(problems, fmt.Sprintf("%s: %s in an identity package (ADR-02 §4; see allowedIdentityImports and purityExceptions in internal/architecture/purity_test.go)", finding.pos, finding.symbol))
			continue
		}
		used[i] = true
	}
	for i, e := range exceptions {
		if strings.TrimSpace(e.reason) == "" {
			problems = append(problems, fmt.Sprintf("exception %s %s %s has no reason", e.pkg, e.file, e.symbol))
		}
		if !used[i] {
			problems = append(problems, fmt.Sprintf("exception %s %s %s matches no finding", e.pkg, e.file, e.symbol))
		}
	}
	return problems
}

type listedPackage struct {
	ImportPath, Dir, Export           string
	GoFiles, CgoFiles, IgnoredGoFiles []string
	DepOnly                           bool
	Error                             *struct{ Err string }
}

// goListExports lists patterns and their dependencies with compiler export data, the
// input go/types needs to resolve imported objects. It runs offline (GOPROXY=off), so
// the module cache must already hold every module the patterns transitively depend on.
// "go list ./..." in CI, "go test ./..." or "go mod download" fills it; running only
// this package against an empty module cache does not.
func goListExports(t *testing.T, dir string, patterns ...string) []listedPackage {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	args := append([]string{"list", "-deps", "-export", "-json=ImportPath,Dir,Export,GoFiles,CgoFiles,IgnoredGoFiles,DepOnly,Error"}, patterns...)
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := ownedcmd.Run(ctx, command); err != nil {
		t.Fatalf("go %s: %v\n%s%s", strings.Join(args, " "), err, stderr.String(), goListFailureHint(ctx))
	}
	var listed []listedPackage
	for decoder := json.NewDecoder(&stdout); ; {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
			return listed
		} else if err != nil {
			t.Fatal(err)
		}
		if pkg.Error != nil {
			t.Fatalf("go list %s: %s", pkg.ImportPath, pkg.Error.Err)
		}
		listed = append(listed, pkg)
	}
}

// goListFailureHint returns module-cache advice for an ordinary go list failure, and
// nothing once ctx is canceled or past its deadline, where that advice would mislead.
func goListFailureHint(ctx context.Context) string {
	if ctx.Err() != nil {
		return ""
	}
	return "(offline: run \"go mod download\" if modules are missing)"
}

func TestGoListFailureHint(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, stop := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer stop()
	tests := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{name: "ordinary failure", ctx: t.Context(), want: true},
		{name: "canceled", ctx: canceled},
		{name: "deadline exceeded", ctx: expired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strings.Contains(goListFailureHint(tt.ctx), "go mod download"); got != tt.want {
				t.Errorf("hint mentions go mod download = %v, want %v", got, tt.want)
			}
		})
	}
}

func exportImporter(fset *token.FileSet, listed []listedPackage) types.Importer {
	exports := map[string]string{}
	for _, pkg := range listed {
		exports[pkg.ImportPath] = pkg.Export
	}
	return importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		export := exports[path]
		if export == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(export)
	})
}
