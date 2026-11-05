// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRequireSingleDocument(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "single document", raw: "apiVersion: nvkr.dev/definition/v5\nkind: Definition\n"},
		{name: "single document with end marker and comment", raw: "a: 1\n...\n# trailing comment\n"},
		{name: "json document", raw: `{"apiVersion":"nvkr.dev/definition/v5","kind":"Definition"}`},
		{name: "leading document marker", raw: "---\na: 1\n"},
		{name: "document marker inside block scalar", raw: "a: |\n  ---\n  still one document\n"},
		{name: "trailing comment", raw: "a: 1\n# trailing comment\n"},
		{name: "second valid document", raw: "a: 1\n---\nb: 2\n", wantErr: true},
		{name: "second invalid document", raw: "a: 1\n---\n[unclosed\n", wantErr: true},
		{name: "empty second document", raw: "a: 1\n---\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requireSingleDocument([]byte(tc.raw), "test")
			if tc.wantErr {
				requireCode(t, err, e.CodeInvalidCatalog)
				return
			}
			if err != nil {
				t.Fatalf("single document rejected: %v", err)
			}
		})
	}
}

func TestDecodeRejectsTrailingDocuments(t *testing.T) {
	definitionRaw, err := os.ReadFile("../../../docs/examples/nvke-tutorial/definition.yaml")
	if err != nil {
		t.Fatal(err)
	}
	environmentRaw, err := os.ReadFile("../../../inputs/environments/github-release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, composite := compositeFixture(t)
	compositeRaw, err := canonical.Marshal(composite)
	if err != nil {
		t.Fatal(err)
	}

	suffixes := []struct {
		name   string
		suffix string
	}{
		{name: "second valid document", suffix: "\n---\n" + string(definitionRaw)},
		{name: "second invalid document", suffix: "\n---\napiVersion: broken\nkind: DefinitelyNotADefinition\n"},
		{name: "empty second document", suffix: "\n---\n"},
	}

	for _, entry := range []struct {
		name   string
		base   []byte
		decode func([]byte) error
	}{
		{name: "definition", base: definitionRaw, decode: func(raw []byte) error {
			_, err := DecodeDefinition(raw)
			return err
		}},
		{name: "environment", base: environmentRaw, decode: func(raw []byte) error {
			_, err := DecodeEnvironment(raw)
			return err
		}},
		{name: "composite", base: compositeRaw, decode: func(raw []byte) error {
			_, err := DecodeComposite(raw)
			return err
		}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			if err := entry.decode(entry.base); err != nil {
				t.Fatalf("single document rejected: %v", err)
			}
			for _, tc := range suffixes {
				t.Run(tc.name, func(t *testing.T) {
					raw := append(append([]byte(nil), entry.base...), tc.suffix...)
					requireCode(t, entry.decode(raw), e.CodeInvalidCatalog)
				})
			}
		})
	}
}

func TestLoadRejectsTrailingDocuments(t *testing.T) {
	definitionRaw, err := os.ReadFile("../../../docs/examples/nvke-tutorial/definition.yaml")
	if err != nil {
		t.Fatal(err)
	}
	environmentRaw, err := os.ReadFile("../../../inputs/environments/github-release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	root, composite := compositeFixture(t)
	compositeRaw, err := canonical.Marshal(composite)
	if err != nil {
		t.Fatal(err)
	}

	trailing := []byte("\n---\napiVersion: broken\nkind: DefinitelyNotADefinition\n")
	dir := t.TempDir()
	definitionPath := filepath.Join(dir, "definition.yaml")
	if err := os.WriteFile(definitionPath, append(append([]byte(nil), definitionRaw...), trailing...), 0o600); err != nil {
		t.Fatal(err)
	}
	environmentPath := filepath.Join(dir, "environment.yaml")
	if err := os.WriteFile(environmentPath, append(append([]byte(nil), environmentRaw...), trailing...), 0o600); err != nil {
		t.Fatal(err)
	}
	compositePath := filepath.Join(root, "defs", "composite.yaml")
	if err := os.WriteFile(compositePath, compositeRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	// The unchanged composite must load cleanly, so the member locators
	// resolve and the rejections below can only come from the trailing document.
	if _, err := LoadComposite(t.Context(), root, "defs/composite.yaml"); err != nil {
		t.Fatalf("clean composite rejected: %v", err)
	}
	if err := os.WriteFile(compositePath, append(append([]byte(nil), compositeRaw...), trailing...), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, entry := range []struct {
		name string
		load func() error
	}{
		{name: "definition", load: func() error {
			_, err := LoadDefinition(t.Context(), definitionPath)
			return err
		}},
		{name: "environment", load: func() error {
			_, err := LoadEnvironment(t.Context(), environmentPath)
			return err
		}},
		{name: "composite", load: func() error {
			_, err := LoadComposite(t.Context(), root, "defs/composite.yaml")
			return err
		}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			requireCode(t, entry.load(), e.CodeInvalidCatalog)
		})
	}

	// A trailing document must be refused before any member is read.
	reads := 0
	reader := func(context.Context, string) ([]byte, error) { reads++; return nil, errors.New("unexpected read") }
	_, err = AdmitComposite(t.Context(), "defs/composite.yaml", append(append([]byte(nil), compositeRaw...), trailing...), reader)
	requireCode(t, err, e.CodeInvalidCatalog)
	if reads != 0 {
		t.Fatalf("trailing-document composite read %d members before rejection", reads)
	}
}

func TestDocumentKindRejectsTrailingDocuments(t *testing.T) {
	_, composite := compositeFixture(t)
	compositeRaw, err := canonical.Marshal(composite)
	if err != nil {
		t.Fatal(err)
	}
	raw := append(append([]byte(nil), compositeRaw...), "\n---\n"...)

	_, err = DocumentKind(raw)
	requireCode(t, err, e.CodeInvalidCatalog)

	path := filepath.Join(t.TempDir(), "composite.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadDefinition(t.Context(), path)
	requireCode(t, err, e.CodeInvalidCatalog)
}
