// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func compositeMemberFixture(name string, outputs ...string) Definition {
	definition := Definition{APIVersion: DefinitionAPIVersion, Kind: DefinitionKind, Metadata: Metadata{Name: name},
		Catalog: Catalog{Base: "../catalog"}, SBOMTool: "syft", CosignTool: "cosign"}
	for _, output := range outputs {
		definition.Outputs = append(definition.Outputs, DefinitionOutput{Name: output, Platform: "linux/amd64", Components: []string{output}, Format: OutputExecutable})
	}
	return definition
}

func writeCompositeFile(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeCompositeRaw(t, name, raw)
}

func writeCompositeRaw(t *testing.T, name string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func memberPin(t *testing.T, name, locator string, definition Definition) CompositeMember {
	t.Helper()
	digest, err := definition.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return CompositeMember{Name: name, Definition: locator, Digest: digest}
}

// compositeFixture writes two members beside defs/composite.yaml.
func compositeFixture(t *testing.T) (string, CompositeDefinition) {
	t.Helper()
	root := t.TempDir()
	alpha := compositeMemberFixture("alpha", "alpha-cli")
	beta := compositeMemberFixture("beta", "beta-cli")
	writeCompositeFile(t, filepath.Join(root, "defs", "alpha.yaml"), alpha)
	writeCompositeFile(t, filepath.Join(root, "defs", "beta.yaml"), beta)
	composite := CompositeDefinition{APIVersion: CompositeAPIVersion, Kind: CompositeKind, Metadata: Metadata{Name: "product"},
		Members: []CompositeMember{memberPin(t, "alpha", "alpha.yaml", alpha), memberPin(t, "beta", "beta.yaml", beta)}}
	return root, composite
}

func requireCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("want %s; got %v", code, err)
	}
}

func TestCompositeAdmissionContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, root string, composite *CompositeDefinition) []byte
		code   e.Code
	}{
		{name: "valid", mutate: func(*testing.T, string, *CompositeDefinition) []byte { return nil }},
		{name: "unknown field", code: e.CodeInvalidCatalog, mutate: func(t *testing.T, _ string, composite *CompositeDefinition) []byte {
			raw, err := canonical.Marshal(composite)
			if err != nil {
				t.Fatal(err)
			}
			return []byte(strings.Replace(string(raw), `"members":`, `"catalog":{"base":"catalog"},"members":`, 1))
		}},
		{name: "empty members", code: e.CodeInvalidCatalog, mutate: func(_ *testing.T, _ string, composite *CompositeDefinition) []byte {
			composite.Members = nil
			return nil
		}},
		{name: "unsorted members", code: e.CodeInvalidCatalog, mutate: func(_ *testing.T, _ string, composite *CompositeDefinition) []byte {
			composite.Members[0], composite.Members[1] = composite.Members[1], composite.Members[0]
			return nil
		}},
		{name: "seventeen members", code: e.CodeInvalidCatalog, mutate: func(_ *testing.T, _ string, composite *CompositeDefinition) []byte {
			composite.Members = nil
			for i := range MaxCompositeMembers + 1 {
				composite.Members = append(composite.Members, CompositeMember{Name: fmt.Sprintf("m%02d", i), Definition: fmt.Sprintf("m%02d.yaml", i), Digest: "sha256:" + fmt.Sprintf("%064x", i)})
			}
			return nil
		}},
		{name: "duplicate name", code: e.CodeInvalidCatalog, mutate: func(_ *testing.T, _ string, composite *CompositeDefinition) []byte {
			composite.Members[1].Name = composite.Members[0].Name
			return nil
		}},
		{name: "duplicate locator", code: e.CodeInvalidCatalog, mutate: func(_ *testing.T, _ string, composite *CompositeDefinition) []byte {
			composite.Members[1].Definition = "./" + composite.Members[0].Definition
			return nil
		}},
		{name: "duplicate digest", code: e.CodeInvalidCatalog, mutate: func(_ *testing.T, _ string, composite *CompositeDefinition) []byte {
			composite.Members[1].Digest = composite.Members[0].Digest
			return nil
		}},
		{name: "member name mismatch", code: e.CodeInvalidCatalog, mutate: func(t *testing.T, root string, composite *CompositeDefinition) []byte {
			renamed := compositeMemberFixture("gamma", "beta-cli")
			writeCompositeFile(t, filepath.Join(root, "defs", "beta.yaml"), renamed)
			composite.Members[1] = memberPin(t, "beta", "beta.yaml", renamed)
			return nil
		}},
		{name: "parent escape", code: e.CodeInvalidCatalog, mutate: func(t *testing.T, root string, composite *CompositeDefinition) []byte {
			composite.Members[1].Definition = "../../beta.yaml"
			return nil
		}},
		{name: "absolute locator", code: e.CodeInvalidCatalog, mutate: func(t *testing.T, root string, composite *CompositeDefinition) []byte {
			composite.Members[1].Definition = filepath.Join(root, "defs", "beta.yaml")
			return nil
		}},
		{name: "symlink locator", code: e.CodeInvalidCatalog, mutate: func(t *testing.T, root string, composite *CompositeDefinition) []byte {
			if err := os.Symlink("beta.yaml", filepath.Join(root, "defs", "link.yaml")); err != nil {
				t.Fatal(err)
			}
			composite.Members[1].Definition = "link.yaml"
			return nil
		}},
		{name: "symlink parent", code: e.CodeInvalidCatalog, mutate: func(t *testing.T, root string, composite *CompositeDefinition) []byte {
			if err := os.Symlink(".", filepath.Join(root, "defs", "loop")); err != nil {
				t.Fatal(err)
			}
			composite.Members[1].Definition = "loop/beta.yaml"
			return nil
		}},
		{name: "directory locator", code: e.CodeInvalidCatalog, mutate: func(t *testing.T, root string, composite *CompositeDefinition) []byte {
			if err := os.Mkdir(filepath.Join(root, "defs", "dir.yaml"), 0o755); err != nil {
				t.Fatal(err)
			}
			composite.Members[1].Definition = "dir.yaml"
			return nil
		}},
		{name: "fifo locator", code: e.CodeInvalidCatalog, mutate: func(t *testing.T, root string, composite *CompositeDefinition) []byte {
			if err := syscall.Mkfifo(filepath.Join(root, "defs", "fifo.yaml"), 0o600); err != nil {
				t.Skip("fifo unsupported:", err)
			}
			composite.Members[1].Definition = "fifo.yaml"
			return nil
		}},
		{name: "missing member", code: e.CodeInvalidCatalog, mutate: func(_ *testing.T, _ string, composite *CompositeDefinition) []byte {
			composite.Members[1].Definition = "absent.yaml"
			return nil
		}},
		{name: "nested composite", code: e.CodeInvalidCatalog, mutate: func(t *testing.T, root string, composite *CompositeDefinition) []byte {
			nested := CompositeDefinition{APIVersion: CompositeAPIVersion, Kind: CompositeKind, Metadata: Metadata{Name: "beta"}, Members: composite.Members[:1]}
			writeCompositeFile(t, filepath.Join(root, "defs", "beta.yaml"), nested)
			return nil
		}},
		{name: "self reference", code: e.CodeInvalidCatalog, mutate: func(_ *testing.T, _ string, composite *CompositeDefinition) []byte {
			composite.Members[1].Definition = "composite.yaml"
			return nil
		}},
		{name: "invalid member", code: e.CodeInvalidCatalog, mutate: func(t *testing.T, root string, _ *CompositeDefinition) []byte {
			writeCompositeRaw(t, filepath.Join(root, "defs", "beta.yaml"), []byte("apiVersion: nvkr.dev/definition/v5\nkind: Definition\nmetadata: {name: beta}\n"))
			return nil
		}},
		{name: "digest mismatch", code: e.CodeUnverifiedRef, mutate: func(t *testing.T, root string, _ *CompositeDefinition) []byte {
			writeCompositeFile(t, filepath.Join(root, "defs", "beta.yaml"), compositeMemberFixture("beta", "beta-cli", "beta-tool"))
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, composite := compositeFixture(t)
			raw := tc.mutate(t, root, &composite)
			if raw == nil {
				var err error
				if raw, err = canonical.Marshal(composite); err != nil {
					t.Fatal(err)
				}
			}
			writeCompositeRaw(t, filepath.Join(root, "defs", "composite.yaml"), raw)
			admitted, err := LoadComposite(t.Context(), root, "defs/composite.yaml")
			if tc.code != "" {
				requireCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := composite.Digest()
			if err != nil || admitted.Digest() != want {
				t.Fatalf("composite digest = %q, want %q (%v)", admitted.Digest(), want, err)
			}
			members := admitted.Members()
			if len(members) != 2 || members[0].Path != "defs/alpha.yaml" || members[1].Name != "beta" || members[1].Digest != composite.Members[1].Digest {
				t.Fatalf("admitted members = %+v", members)
			}
		})
	}
}

func TestCompositeAdmissionKeepsDigestAcrossMemberFormatting(t *testing.T) {
	root, composite := compositeFixture(t)
	formatted := "# reformatted\napiVersion: nvkr.dev/definition/v5\nkind: Definition\nmetadata:\n  name: beta\n" +
		"catalog:\n  base: ../catalog   # same catalog\nsbomTool: syft\ncosignTool: cosign\noutputs:\n" +
		"  - name: beta-cli\n    format: executable\n    platform: linux/amd64\n    components: [beta-cli]\n"
	writeCompositeRaw(t, filepath.Join(root, "defs", "beta.yaml"), []byte(formatted))
	writeCompositeFile(t, filepath.Join(root, "defs", "composite.yaml"), composite)
	if _, err := LoadComposite(t.Context(), root, "defs/composite.yaml"); err != nil {
		t.Fatalf("formatting-only member edit changed admission: %v", err)
	}
}

func TestCompositeDigestExcludesLocators(t *testing.T) {
	root, composite := compositeFixture(t)
	writeCompositeFile(t, filepath.Join(root, "defs", "composite.yaml"), composite)
	before, err := LoadComposite(t.Context(), root, "defs/composite.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "families"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "defs", "beta.yaml"), filepath.Join(root, "families", "beta.yaml")); err != nil {
		t.Fatal(err)
	}
	composite.Members[1].Definition = "../families/beta.yaml"
	writeCompositeFile(t, filepath.Join(root, "defs", "composite.yaml"), composite)
	after, err := LoadComposite(t.Context(), root, "defs/composite.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if before.Digest() != after.Digest() || after.Members()[1].Path != "families/beta.yaml" {
		t.Fatalf("moving a member changed composite identity: %s != %s", before.Digest(), after.Digest())
	}
	recorded, err := CompositeDigest(composite.Metadata, composite.MemberIdentities())
	if err != nil || recorded != after.Digest() {
		t.Fatalf("recorded composite digest = %q, want %q (%v)", recorded, after.Digest(), err)
	}
	renamed := composite.Clone()
	renamed.Metadata.Name = "other"
	other, err := renamed.Digest()
	if err != nil || other == after.Digest() {
		t.Fatalf("composite metadata is not identity: %q %v", other, err)
	}
	family, err := after.Members()[0].Definition.Digest()
	if err != nil || family == after.Digest() {
		t.Fatal("composite and member digests share a family")
	}
}

func TestCompositeAdmissionCancellationReadsNoMember(t *testing.T) {
	root, composite := compositeFixture(t)
	writeCompositeFile(t, filepath.Join(root, "defs", "composite.yaml"), composite)
	raw, err := os.ReadFile(filepath.Join(root, "defs", "composite.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reads := 0
	reader := func(context.Context, string) ([]byte, error) { reads++; return nil, errors.New("unexpected read") }
	_, err = AdmitComposite(ctx, "defs/composite.yaml", raw, reader)
	requireCode(t, err, e.CodeTimeout)
	if reads != 0 {
		t.Fatalf("canceled admission read %d members", reads)
	}
	_, err = LoadComposite(ctx, root, "defs/composite.yaml")
	requireCode(t, err, e.CodeTimeout)

	// Cancellation observed between members also maps to timeout.
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	inner := RootReader(root)
	reader = func(ctx context.Context, name string) ([]byte, error) {
		reads++
		raw, err := inner(ctx, name)
		cancel()
		return raw, err
	}
	_, err = AdmitComposite(ctx, "defs/composite.yaml", raw, reader)
	requireCode(t, err, e.CodeTimeout)
	if reads != 1 {
		t.Fatalf("admission continued after cancellation: %d reads", reads)
	}
}

func TestDocumentKindDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, raw, kind string
	}{
		{"definition", "apiVersion: nvkr.dev/definition/v5\nkind: Definition\n", DefinitionKind},
		{"composite", "apiVersion: nvkr.dev/composite-definition/v1\nkind: CompositeDefinition\n", CompositeKind},
		{"future composite", "apiVersion: nvkr.dev/composite-definition/v2\nkind: CompositeDefinition\n", ""},
		{"old definition", "apiVersion: nvkr.dev/definition/v4\nkind: Definition\n", ""},
		{"environment", "apiVersion: nvkr.dev/environment/v1\nkind: Environment\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, err := DocumentKind([]byte(tc.raw))
			if tc.kind == "" {
				requireCode(t, err, e.CodeInvalidCatalog)
				return
			}
			if err != nil || kind != tc.kind {
				t.Fatalf("kind = %q (%v), want %q", kind, err, tc.kind)
			}
		})
	}
}
