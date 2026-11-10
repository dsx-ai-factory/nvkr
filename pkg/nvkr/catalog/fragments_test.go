// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func fragmentFixture(t *testing.T) (string, *registryDoc, []byte, *Catalog) {
	t.Helper()
	provider, err := BindContext(t.Context(), Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := readRegistry(provider)
	if err != nil {
		t.Fatal(err)
	}
	want, err := LoadContext(t.Context(), Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	fragment := registryFragment{APIVersion: "nvkr.dev/catalog-fragment/v1", Kind: "CatalogFragment", Sources: reg.Sources, Components: reg.Components}
	raw, err := yaml.Marshal(fragment)
	if err != nil {
		t.Fatal(err)
	}
	imp := fragmentImport{Path: "../shared.yaml", SHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), Sources: map[string]string{}, Components: map[string]string{}}
	for name := range reg.Sources {
		imp.Sources[name] = name
	}
	for name := range reg.Components {
		imp.Components[name] = name
	}
	reg.Sources = nil
	reg.Components = nil
	reg.Imports = []fragmentImport{imp}
	root := t.TempDir()
	dir := filepath.Join(root, "consumer")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFragmentFixture(t, filepath.Join(root, "shared.yaml"), raw)
	lock, err := provider.ReadFile(LockFile)
	if err != nil {
		t.Fatal(err)
	}
	writeFragmentFixture(t, filepath.Join(dir, LockFile), lock)
	writeFragmentRegistry(t, dir, reg)
	return dir, reg, raw, want
}

func writeFragmentFixture(t *testing.T, name string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFragmentRegistry(t *testing.T, dir string, reg *registryDoc) {
	t.Helper()
	raw, err := yaml.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	writeFragmentFixture(t, filepath.Join(dir, RegistryFile), raw)
}

func TestFragmentsPreserveResolvedCatalogAndPinOwnership(t *testing.T) {
	dir, _, _, want := fragmentFixture(t)
	got, err := LoadContext(t.Context(), Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := want.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	gotDigest, err := got.SnapshotDigest()
	if err != nil || wantDigest != gotDigest {
		t.Fatalf("catalog changed: %s / %s: %v", wantDigest, gotDigest, err)
	}
	reg, lock, err := pinUpdateInputs(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reg.Sources, want.Sources) {
		t.Fatal("pin updater did not resolve fragment sources")
	}
	before := clonePinLock(lock)
	if err := WriteLockContext(t.Context(), dir, lock); err != nil {
		t.Fatal(err)
	}
	after, err := LoadLockContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Pins, after.Pins) || !reflect.DeepEqual(before.Artifacts, after.Artifacts) {
		t.Fatal("pin values changed")
	}
}

func TestFragmentsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*registryDoc, *[]byte)
	}{
		{"wrong hash", func(r *registryDoc, _ *[]byte) { r.Imports[0].SHA256 = strings.Repeat("0", 64) }},
		{"unknown source", func(r *registryDoc, _ *[]byte) { r.Imports[0].Sources["missing"] = "missing" }},
		{"unknown component", func(r *registryDoc, _ *[]byte) { r.Imports[0].Components["missing"] = "missing" }},
		{"source collision", func(r *registryDoc, _ *[]byte) {
			for n := range r.Imports[0].Sources {
				r.Sources = map[string]Source{n: {}}
				break
			}
		}},
		{"component collision", func(r *registryDoc, _ *[]byte) {
			for n := range r.Imports[0].Components {
				r.Components = map[string]rawComponent{n: {}}
				break
			}
		}},
		{"unused binding", func(r *registryDoc, _ *[]byte) { r.Imports[0].SourceNames = map[string]string{"missing": "source"} }},
		{"nested import", func(_ *registryDoc, raw *[]byte) { *raw = append(*raw, []byte("imports: []\n")...) }},
		{"empty selection", func(r *registryDoc, _ *[]byte) { r.Imports[0].Sources = nil; r.Imports[0].Components = nil }},
		{"absolute path", func(r *registryDoc, _ *[]byte) { r.Imports[0].Path = "/etc/passwd" }},
		{"noncanonical path", func(r *registryDoc, _ *[]byte) { r.Imports[0].Path = "../a/../shared.yaml" }},
		{"too many imports", func(r *registryDoc, _ *[]byte) {
			for len(r.Imports) < 65 {
				r.Imports = append(r.Imports, r.Imports[0])
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, reg, raw, _ := fragmentFixture(t)
			original := append([]byte(nil), raw...)
			tc.mutate(reg, &raw)
			if !bytes.Equal(original, raw) {
				reg.Imports[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
			}
			writeFragmentFixture(t, filepath.Join(dir, "../shared.yaml"), raw)
			writeFragmentRegistry(t, dir, reg)
			_, err := LoadContext(t.Context(), Dir(dir))
			if catalogErrorCode(err) != e.CodeInvalidCatalog {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestFragmentMutationCannotAuthorizeLockWrite(t *testing.T) {
	dir, _, raw, _ := fragmentFixture(t)
	lock, err := LoadLockContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, LockFile))
	if err != nil {
		t.Fatal(err)
	}
	writeFragmentFixture(t, filepath.Join(dir, "../shared.yaml"), append(raw, '\n'))
	if err := WriteLockContext(t.Context(), dir, lock); catalogErrorCode(err) != e.CodeInvalidCatalog {
		t.Fatalf("got %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, LockFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed write changed lock")
	}
}

func TestFragmentProviderAdmissionAndBudget(t *testing.T) {
	dir, reg, raw, _ := fragmentFixture(t)
	registry, err := yaml.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadContext(t.Context(), bytesProvider{RegistryFile: registry}); catalogErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("got %v", err)
	}
	bound, err := BindContext(t.Context(), Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	p := bound.(*boundContextProvider)
	p.totalBytes = MaxCatalogTotalBytes - int64(len(raw)) + 1
	if _, err := p.readFragment("../shared.yaml"); catalogErrorCode(err) != e.CodeInvalidCatalog {
		t.Fatalf("got %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := LoadContext(ctx, Dir(dir)); catalogErrorCode(err) != e.CodeTimeout {
		t.Fatalf("got %v", err)
	}
}

func TestFragmentAliasesResolveAgainstConsumerPins(t *testing.T) {
	dir, reg, _, want := fragmentFixture(t)
	// An imported source and producer can use consumer-local names. Only the
	// explicit source binding changes; no commands or material edges are rewritten.
	imp := &reg.Imports[0]
	imp.Sources["local-kubernetes"] = imp.Sources["kubernetes"]
	delete(imp.Sources, "kubernetes")
	imp.SourceNames = map[string]string{"kubernetes": "local-kubernetes"}
	lock, err := LoadLockContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	lock.Pins["local-kubernetes"] = lock.Pins["kubernetes"]
	delete(lock.Pins, "kubernetes")
	raw, err := serializePinLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	writeFragmentRegistry(t, dir, reg)
	writeFragmentFixture(t, filepath.Join(dir, LockFile), raw)
	got, err := LoadContext(t.Context(), Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Components, want.Components) {
		t.Fatal("source alias changed resolved producers")
	}
}

func TestFragmentRejectsSymlink(t *testing.T) {
	dir, reg, _, _ := fragmentFixture(t)
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(filepath.Join(dir, "../shared.yaml"), link); err != nil {
		t.Fatal(err)
	}
	reg.Imports[0].Path = "link.yaml"
	writeFragmentRegistry(t, dir, reg)
	if _, err := LoadContext(t.Context(), Dir(dir)); catalogErrorCode(err) != e.CodeInvalidCatalog {
		t.Fatalf("got %v", err)
	}
}

type canceledFragmentProvider struct {
	bytesProvider
	cancel context.CancelFunc
}

func (p canceledFragmentProvider) ReadFragmentContext(ctx context.Context, _ string) ([]byte, error) {
	p.cancel()
	return nil, ctx.Err()
}

func TestFragmentReadCancellationCode(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	bound, err := BindContext(ctx, canceledFragmentProvider{bytesProvider: bytesProvider{}, cancel: cancel})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.(*boundContextProvider).readFragment("../shared.yaml"); catalogErrorCode(err) != e.CodeTimeout {
		t.Fatalf("fragment read cancellation: %v", err)
	}
}
