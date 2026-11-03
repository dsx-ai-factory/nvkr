// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package architecture hosts the ADR-02 §4 static purity guard: identity-producing
// packages must not reach wall-clock reads, randomness, UUID generation, hostname, or
// host-environment reads, because a leak there would make buildDigest host-dependent.
// The guard type-checks those packages and inspects resolved references, so import
// aliases, dot imports and function values cannot hide a forbidden source and comments
// cannot trip it. It does not follow calls into other packages. The empirical
// invariance test lives in pkg/nvkr/ir (TestDigestHostInvariance).
package architecture

import (
	"bytes"
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

// identityPackages are the package trees whose output flows into a digest-addressed
// identity (buildDigest / builder-profile / trust objects). They MUST be pure.
var identityPackages = []string{
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph/...",
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical/...",
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir/...",
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target/...",
}

// forbiddenImports are nondeterministic packages; importing one under any name, including
// a blank import, is a violation.
var forbiddenImports = map[string]bool{
	"crypto/rand":            true,
	"github.com/google/uuid": true,
	"math/rand":              true,
	"math/rand/v2":           true,
}

// forbiddenFuncs are package-level functions that read the clock or the host. Any
// resolved reference is a violation, whether called or taken as a value.
var forbiddenFuncs = map[string]bool{
	"os.Environ":   true,
	"os.Getenv":    true,
	"os.Hostname":  true,
	"os.LookupEnv": true,
	"time.Now":     true,
	"time.Since":   true,
	"time.Until":   true,
}

// purityException permits one forbidden symbol or import path in one file of one package.
type purityException struct {
	pkg, file, symbol, reason string
}

// purityExceptions is the only allowlist. Each entry needs a reason and must still match
// a finding; there is no inline source marker.
var purityExceptions []purityException

type purityFinding struct {
	pkg, file, symbol string
	pos               token.Position
}

func TestIdentityPathsArePure(t *testing.T) {
	listed := goListExports(t, filepath.Join("..", ".."), identityPackages...)
	fset := token.NewFileSet()
	imp := exportImporter(fset, listed)
	var findings []purityFinding
	checked := map[string]bool{}
	for _, pkg := range listed {
		if pkg.DepOnly {
			continue
		}
		if len(pkg.IgnoredGoFiles) > 0 || len(pkg.CgoFiles) > 0 {
			t.Errorf("%s: files outside the checked build configuration: ignored %v, cgo %v", pkg.ImportPath, pkg.IgnoredGoFiles, pkg.CgoFiles)
		}
		var files []*ast.File
		for _, name := range pkg.GoFiles {
			file, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, file)
		}
		pkgFindings, err := purityFindings(pkg.ImportPath, fset, files, imp)
		if err != nil {
			t.Fatal(err)
		}
		findings = append(findings, pkgFindings...)
		checked[pkg.ImportPath] = true
	}
	for _, pattern := range identityPackages {
		if root := strings.TrimSuffix(pattern, "/..."); !checked[root] {
			t.Errorf("identity package %s was not checked", root)
		}
	}
	for _, problem := range applyPurityExceptions(findings, purityExceptions) {
		t.Error(problem)
	}
}

// TestPurityRuleMatrix pins the ADR-02 guard against the counterexamples that defeated
// the previous line-substring matcher, plus the exception contract.
func TestPurityRuleMatrix(t *testing.T) {
	const pkgPath = "example.test/p"
	exception := func(symbol, reason string) []purityException {
		return []purityException{{pkg: pkgPath, file: "p.go", symbol: symbol, reason: reason}}
	}
	tests := []struct {
		name       string
		src        string
		exceptions []purityException
		want       []string
	}{
		{name: "direct call", src: `import "time"; var x = time.Now()`, want: []string{"p.go:2:29: time.Now"}},
		{name: "import alias", src: `import clock "time"; var x = clock.Now()`, want: []string{"p.go:2:36: time.Now"}},
		{name: "function value", src: `import "time"; var now = time.Now; var x = now()`, want: []string{"p.go:2:31: time.Now"}},
		{name: "dot import", src: `import . "time"; var x = Now()`, want: []string{"p.go:2:26: time.Now"}},
		{name: "host environment", src: `import "os"; var x, _ = os.LookupEnv("HOME")`, want: []string{"p.go:2:28: os.LookupEnv"}},
		{name: "aliased forbidden import", src: `import r "math/rand"; var x = r.Int()`, want: []string{"p.go:2:10: math/rand"}},
		{name: "blank forbidden import", src: `import _ "crypto/rand"`, want: []string{"p.go:2:10: crypto/rand"}},
		{name: "retired inline marker", src: `import "time"; var x = time.Now() //nvkr:allow-nondeterminism`, want: []string{"p.go:2:29: time.Now"}},
		{name: "comment only", src: "\n// Never call time.Now() in identity code."},
		{name: "shadowed identifier", src: `type clock struct{}; func (clock) Now() int { return 0 }; var time clock; var x = time.Now()`},
		{name: "deterministic time API", src: `import "time"; var x = time.Unix(0, 0).UTC()`},
		{name: "exact exception", src: `import "time"; var x = time.Now()`, exceptions: exception("time.Now", "fixture reason")},
		{name: "exception without reason", src: `import "time"; var x = time.Now()`, exceptions: exception("time.Now", " "),
			want: []string{`exception example.test/p p.go time.Now has no reason`}},
		{name: "unused exception", src: `var x = 1`, exceptions: exception("time.Now", "fixture reason"),
			want: []string{`exception example.test/p p.go time.Now matches no finding`}},
		{name: "exception for another symbol", src: `import "time"; var x = time.Since(time.Time{})`, exceptions: exception("time.Now", "fixture reason"),
			want: []string{"p.go:2:29: time.Since", `exception example.test/p p.go time.Now matches no finding`}},
	}
	listed := goListExports(t, ".", "crypto/rand", "math/rand", "os", "time")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "p.go", "package p\n"+tc.src, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			findings, err := purityFindings(pkgPath, fset, []*ast.File{file}, exportImporter(fset, listed))
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

// purityFindings type-checks one package and returns its forbidden imports and resolved
// references to forbidden functions, sorted by position.
func purityFindings(pkgPath string, fset *token.FileSet, files []*ast.File, imp types.Importer) ([]purityFinding, error) {
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
			if forbiddenImports[path] {
				add(spec.Path.Pos(), path)
			}
		}
	}
	for ident, obj := range info.Uses {
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil || fn.Signature().Recv() != nil {
			continue
		}
		if symbol := fn.Pkg().Path() + "." + fn.Name(); forbiddenFuncs[symbol] {
			add(ident.Pos(), symbol)
		}
	}
	slices.SortFunc(findings, func(a, b purityFinding) int {
		return strings.Compare(a.pos.String(), b.pos.String())
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
			problems = append(problems, fmt.Sprintf("%s: %s in an identity package (ADR-02 §4)", finding.pos, finding.symbol))
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
// input go/types needs to resolve imported objects.
func goListExports(t *testing.T, dir string, patterns ...string) []listedPackage {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	args := append([]string{"list", "-deps", "-export", "-json=ImportPath,Dir,Export,GoFiles,CgoFiles,IgnoredGoFiles,DepOnly,Error"}, patterns...)
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := ownedcmd.Run(ctx, command); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
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
