// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// outcomePackages describe and publish qualification outcomes. Their times come
// from the outcome, the event or a caller's clock, and their environment from the
// caller, so a second publish writes the same bytes. They may import time and os
// for deterministic APIs and bounded file reads; only the symbol rule applies.
var outcomePackages = []string{
	modulePath + "/pkg/nvkr/qualificationoutcome",
	modulePath + "/pkg/nvkr/resultspublish",
}

func TestOutcomePackagesObserveNoClockOrHost(t *testing.T) {
	listed := goListExports(t, filepath.Join("..", ".."), outcomePackages...)
	fset := token.NewFileSet()
	imp := exportImporter(fset, listed)
	checked := map[string]bool{}
	for _, pkg := range listed {
		if pkg.DepOnly {
			continue
		}
		var files []*ast.File
		for _, name := range pkg.GoFiles {
			file, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, file)
		}
		findings, err := purityFindings(pkg.ImportPath, fset, files, imp, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, finding := range findings {
			if !strings.HasPrefix(finding.symbol, "import ") {
				t.Errorf("%s: %s observes the clock or the host", finding.pos, finding.symbol)
			}
		}
		checked[pkg.ImportPath] = true
	}
	for _, pkg := range outcomePackages {
		if !checked[pkg] {
			t.Errorf("outcome package %s was not checked", pkg)
		}
	}
}
