// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
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
		{name: "document marker inside block scalar", raw: "a: |\n  ---\n  still one document\n"},
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

	suffixes := map[string]string{
		"second valid document":   "\n---\n" + string(definitionRaw),
		"second invalid document": "\n---\napiVersion: broken\nkind: DefinitelyNotADefinition\n",
		"empty second document":   "\n---\n",
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
			for _, suffix := range suffixes {
				raw := append(append([]byte(nil), entry.base...), suffix...)
				requireCode(t, entry.decode(raw), e.CodeInvalidCatalog)
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
	compositePath := filepath.Join(root, "composition.yaml")
	if err := os.WriteFile(compositePath, append(append([]byte(nil), compositeRaw...), trailing...), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = LoadDefinition(t.Context(), definitionPath)
	requireCode(t, err, e.CodeInvalidCatalog)
	_, err = LoadEnvironment(t.Context(), environmentPath)
	requireCode(t, err, e.CodeInvalidCatalog)
	_, err = LoadComposite(t.Context(), root, "composition.yaml")
	requireCode(t, err, e.CodeInvalidCatalog)
}
